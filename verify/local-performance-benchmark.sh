#!/bin/zsh
set -euo pipefail
set +x
set +v

if [[ $# -lt 3 ]]; then
  cat >&2 <<'EOF'
usage: verify/local-performance-benchmark.sh <controller-base-url> <receiver-base-url> <receiver-helper-log> [count] [batch-size] [samples] [interval-seconds]

example:
  verify/local-performance-benchmark.sh \
    http://127.0.0.1:39272 \
    http://127.0.0.1:39273 \
    /tmp/mb-helper-receiver.log \
    240 40 12 0.1

What it does:
  1. ensures controller -> receiver session exists (connect/pair if needed)
  2. temporarily disables capture on both sides for deterministic measurement
  3. sends a burst of remote mouse_move events using session/input/batch
  4. samples avg_latency_ms from /api/status
  5. parses receiver helper logs for injected/coalesced mouse_move counts
  6. restores capture state before exit

Before running:
  - both daemons must already be running
  - receiver helper must already be running with MB_HELPER_VERBOSE_INPUT=1 and stderr redirected to the log path you pass in
EOF
  exit 1
fi

if ! command -v jq >/dev/null 2>&1; then
  echo "jq is required" >&2
  exit 1
fi

CONTROLLER_URL="${1%/}"
RECEIVER_URL="${2%/}"
RECEIVER_LOG="$3"
COUNT="${4:-240}"
BATCH_SIZE="${5:-40}"
SAMPLES="${6:-12}"
INTERVAL="${7:-0.1}"

restore_controller_capture="true"
restore_receiver_capture="true"

json_get() {
  local url="$1"
  curl -fsS "$url"
}

extract_pin() {
  sed -n 's/.*"display_pin":"\([0-9][0-9][0-9][0-9][0-9][0-9]\)".*/\1/p'
}

extract_first_session_device_id() {
  sed -n 's/.*"sessions":\[[^]]*"device_id":"\([^"]*\)".*/\1/p'
}

extract_capture_enabled() {
  sed -nE 's/.*"capture_enabled":(true|false).*/\1/p'
}

wait_for_pin() {
  local url="$1"
  for _ in {1..50}; do
    local pin
    pin="$(json_get "$url/api/status" | extract_pin)"
    if [[ -n "$pin" ]]; then
      echo "$pin"
      return 0
    fi
    sleep 0.2
  done
  echo "timed out waiting for pairing PIN from $url" >&2
  return 1
}

wait_for_session_device_id() {
  local url="$1"
  for _ in {1..50}; do
    local device_id
    device_id="$(json_get "$url/api/status" | extract_first_session_device_id)"
    if [[ -n "$device_id" ]]; then
      echo "$device_id"
      return 0
    fi
    sleep 0.2
  done
  echo "timed out waiting for active session at $url" >&2
  return 1
}

current_session_device_id() {
  json_get "$1/api/status" | extract_first_session_device_id
}

set_capture_state() {
  local base_url="$1"
  local enabled="$2"
  curl -fsS -X PUT "$base_url/api/control/capture" \
    -H 'Content-Type: application/json' \
    -d "{\"enabled\":$enabled}" >/dev/null
}

wait_for_capture_state() {
  local base_url="$1"
  local expected="$2"
  for _ in {1..25}; do
    local actual
    actual="$(json_get "$base_url/api/status" | extract_capture_enabled)"
    if [[ "$actual" == "$expected" ]]; then
      return 0
    fi
    sleep 0.1
  done
  echo "timed out waiting for capture_enabled=$expected at $base_url" >&2
  return 1
}

restore_capture_best_effort() {
  set_capture_state "$CONTROLLER_URL" "$restore_controller_capture" >/dev/null 2>&1 || true
  set_capture_state "$RECEIVER_URL" "$restore_receiver_capture" >/dev/null 2>&1 || true
}

restore_capture() {
  set_capture_state "$CONTROLLER_URL" "$restore_controller_capture"
  set_capture_state "$RECEIVER_URL" "$restore_receiver_capture"
  wait_for_capture_state "$CONTROLLER_URL" "$restore_controller_capture"
  wait_for_capture_state "$RECEIVER_URL" "$restore_receiver_capture"
}

line_count() {
  local file="$1"
  if [[ ! -f "$file" ]]; then
    echo "missing log file: $file" >&2
    exit 1
  fi
  wc -l < "$file" | tr -d '[:space:]'
}

tail_from_line() {
  local file="$1"
  local start_line="$2"
  sed -n "${start_line},\$p" "$file"
}

sample_latencies() {
  local base_url="$1"
  local samples="$2"
  local interval="$3"
  local values=()

  for ((i = 1; i <= samples; i++)); do
    values+=( "$(curl -fsS "$base_url/api/status" | jq -r '.sessions[]?.avg_latency_ms' | head -n1)" )
    if [[ "${values[-1]}" == "null" ]]; then
      values=("${values[@]:0:${#values[@]}-1}")
    elif [[ -z "${values[-1]:-}" ]]; then
      values=("${values[@]:0:${#values[@]}-1}")
    fi
    sleep "$interval"
  done

  if [[ ${#values[@]} -eq 0 ]]; then
    echo "no latency samples collected" >&2
    return 1
  fi

  local tmpfile
  tmpfile="$(mktemp /tmp/mousebridge-bench-latency.XXXXXX)"
  printf '%s\n' "${values[@]}" | sort -n > "$tmpfile"
  local count="${#values[@]}"
  local min max p50 p95
  min="$(head -n1 "$tmpfile")"
  max="$(tail -n1 "$tmpfile")"
  p50="$(sed -n "$(( (count + 1) / 2 ))p" "$tmpfile")"
  p95="$(sed -n "$(( (count * 95 + 99) / 100 ))p" "$tmpfile")"
  rm -f "$tmpfile"

  typeset -g LATENCY_COUNT="$count"
  typeset -g LATENCY_MIN="$min"
  typeset -g LATENCY_P50="$p50"
  typeset -g LATENCY_P95="$p95"
  typeset -g LATENCY_MAX="$max"
}

trap restore_capture_best_effort EXIT

echo "[bench] ensuring controller -> receiver session"
receiver_hostport="${RECEIVER_URL#http://}"
receiver_host="${receiver_hostport%:*}"
receiver_port="${receiver_hostport##*:}"

device_id="$(current_session_device_id "$CONTROLLER_URL")"
if [[ -z "$device_id" ]]; then
  curl -fsS -X POST "$CONTROLLER_URL/api/connect" \
    -H 'Content-Type: application/json' \
    -d "{\"host\":\"$receiver_host\",\"port\":$receiver_port}" >/dev/null
fi

device_id="$(current_session_device_id "$CONTROLLER_URL")"
if [[ -z "$device_id" ]]; then
  echo "[bench] waiting for receiver PIN"
  pin="$(wait_for_pin "$RECEIVER_URL")"
  controller_status="$(json_get "$CONTROLLER_URL/api/status")"
  pairing_id="$(printf '%s' "$controller_status" | sed -n 's/.*"pairing_id":"\([^"]*\)".*/\1/p')"
  if [[ -z "$pairing_id" ]]; then
    echo "controller has no pending pairing_id" >&2
    exit 1
  fi
  curl -fsS -X POST "$CONTROLLER_URL/api/pair/pin" \
    -H 'Content-Type: application/json' \
    -d "{\"pairing_id\":\"$pairing_id\",\"pin\":\"$pin\"}" >/dev/null
fi

device_id="$(wait_for_session_device_id "$CONTROLLER_URL")"
echo "[bench] remote session device_id: $device_id"

restore_controller_capture="$(json_get "$CONTROLLER_URL/api/status" | extract_capture_enabled)"
restore_receiver_capture="$(json_get "$RECEIVER_URL/api/status" | extract_capture_enabled)"

echo "[bench] disabling local capture for deterministic burst"
set_capture_state "$CONTROLLER_URL" false
set_capture_state "$RECEIVER_URL" false
wait_for_capture_state "$CONTROLLER_URL" false
wait_for_capture_state "$RECEIVER_URL" false

receiver_start_line=$(( $(line_count "$RECEIVER_LOG") + 1 ))

echo "[bench] sending mouse_move burst count=$COUNT batch_size=$BATCH_SIZE"
"$(dirname "$0")/session-input-burst.sh" "$CONTROLLER_URL" "$device_id" "$COUNT" "$BATCH_SIZE" >/dev/null

sleep 1

echo "[bench] sampling avg_latency_ms"
sample_latencies "$RECEIVER_URL" "$SAMPLES" "$INTERVAL"

receiver_delta="$(tail_from_line "$RECEIVER_LOG" "$receiver_start_line")"
injected_count="$(printf '%s\n' "$receiver_delta" | grep -c 'injected mouse_move' || true)"
coalesced_lines="$(printf '%s\n' "$receiver_delta" | grep -c 'coalesced incoming mouse_move' || true)"
coalesced_events="$(printf '%s\n' "$receiver_delta" | awk '
  /coalesced incoming mouse_move count=/ {
    for (i = 1; i <= NF; i++) {
      if ($i ~ /^count=/) {
        split($i, pair, "=")
        sum += pair[2]
      }
    }
  }
  END { print sum + 0 }
')"

restore_capture
trap - EXIT

echo "[bench] summary"
echo "  sent_mouse_move_events=$COUNT"
echo "  batch_size=$BATCH_SIZE"
echo "  receiver_injected_mouse_move=$injected_count"
echo "  receiver_coalesced_log_lines=$coalesced_lines"
echo "  receiver_coalesced_input_events=$coalesced_events"
echo "  compression_ratio=$(awk "BEGIN { if ($injected_count == 0) print \"inf\"; else printf \"%.2f\", $COUNT / $injected_count }")"
echo "  latency_samples=$LATENCY_COUNT"
echo "  latency_min_ms=$LATENCY_MIN"
echo "  latency_p50_ms=$LATENCY_P50"
echo "  latency_p95_ms=$LATENCY_P95"
echo "  latency_max_ms=$LATENCY_MAX"
