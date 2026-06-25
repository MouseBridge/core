#!/bin/zsh
set -euo pipefail

if [[ $# -lt 4 ]]; then
  cat >&2 <<'EOF'
usage: verify/local-loop-safety.sh <controller-base-url> <receiver-base-url> <controller-helper-log> <receiver-helper-log> [text]

example:
  verify/local-loop-safety.sh \
    http://127.0.0.1:39273 \
    http://127.0.0.1:39272 \
    /tmp/mb-helper-controller.log \
    /tmp/mb-helper-receiver.log \
    "MouseBridge anti-loop probe"

What it checks:
  1. controller -> receiver session exists (connect/pair if needed)
  2. with capture still enabled on both helpers, sends move + click + text over the real session
  3. receiver helper log shows injected actions
  4. controller helper log must NOT show new "sent input kind=" lines caused by the receiver injection

Before running:
  - both daemons and both helpers must already be running
  - both helpers should be started with MB_HELPER_VERBOSE_INPUT=1 and their stderr redirected to the log paths you pass in
  - focus a safe text field or a fresh TextEdit document on the local Mac
EOF
  exit 1
fi

CONTROLLER_URL="${1%/}"
RECEIVER_URL="${2%/}"
CONTROLLER_LOG="$3"
RECEIVER_LOG="$4"
TEXT="${5:-MouseBridge anti-loop probe}"

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

json_quote() {
  swift -e 'import Foundation; let data = FileHandle.standardInput.readDataToEndOfFile(); let value = String(decoding: data, as: UTF8.self); let encoded = try! JSONEncoder().encode(value); print(String(decoding: encoded, as: UTF8.self))'
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

echo "[loop] ensuring controller -> receiver session"
receiver_hostport="${RECEIVER_URL#http://}"
receiver_host="${receiver_hostport%:*}"
receiver_port="${receiver_hostport##*:}"

device_id="$(current_session_device_id "$CONTROLLER_URL")"
if [[ -z "$device_id" ]]; then
  curl -fsS -X POST "$CONTROLLER_URL/api/connect" \
    -H 'Content-Type: application/json' \
    -d "{\"host\":\"$receiver_host\",\"port\":$receiver_port}" >/dev/null
else
  echo "[loop] session already active, skipping new connect"
fi

device_id="$(current_session_device_id "$CONTROLLER_URL")"
if [[ -z "$device_id" ]]; then
  echo "[loop] waiting for receiver PIN"
  pin="$(wait_for_pin "$RECEIVER_URL")"
  echo "[loop] PIN acquired: $pin"

  controller_status="$(json_get "$CONTROLLER_URL/api/status")"
  pairing_id="$(printf '%s' "$controller_status" | sed -n 's/.*"pairing_id":"\([^"]*\)".*/\1/p')"
  if [[ -z "$pairing_id" ]]; then
    echo "controller has no pending pairing_id" >&2
    exit 1
  fi

  echo "[loop] confirming pair on controller"
  curl -fsS -X POST "$CONTROLLER_URL/api/pair/pin" \
    -H 'Content-Type: application/json' \
    -d "{\"pairing_id\":\"$pairing_id\",\"pin\":\"$pin\"}" >/dev/null
fi

echo "[loop] waiting for active session"
device_id="$(wait_for_session_device_id "$CONTROLLER_URL")"
echo "[loop] remote session device_id: $device_id"

controller_start_line=$(( $(line_count "$CONTROLLER_LOG") + 1 ))
receiver_start_line=$(( $(line_count "$RECEIVER_LOG") + 1 ))

echo "[loop] sending move/click/text probe with capture still enabled"
payload=$(cat <<EOF
{
  "device_id": "$device_id",
  "step_delay_ms": 20,
  "inputs": [
    {"kind":"mouse_move","dx":24,"dy":-8},
    {"kind":"mouse_button","button":"left","pressed":true},
    {"kind":"mouse_button","button":"left","pressed":false},
    {"kind":"text","text":$(printf '%s' "$TEXT" | json_quote)}
  ]
}
EOF
)
curl -fsS -X POST "$CONTROLLER_URL/api/session/input/batch" \
  -H 'Content-Type: application/json' \
  -d "$payload" >/dev/null

sleep 1

controller_delta="$(tail_from_line "$CONTROLLER_LOG" "$controller_start_line")"
receiver_delta="$(tail_from_line "$RECEIVER_LOG" "$receiver_start_line")"

echo "[loop] checking receiver injection log"
printf '%s\n' "$receiver_delta" | grep -q 'injected mouse_move' || {
  echo "receiver log missing injected mouse_move" >&2
  printf '%s\n' "$receiver_delta" >&2
  exit 1
}
printf '%s\n' "$receiver_delta" | grep -q 'injected mouse_button' || {
  echo "receiver log missing injected mouse_button" >&2
  printf '%s\n' "$receiver_delta" >&2
  exit 1
}
printf '%s\n' "$receiver_delta" | grep -q 'injected text len=' || {
  echo "receiver log missing injected text" >&2
  printf '%s\n' "$receiver_delta" >&2
  exit 1
}

echo "[loop] checking controller echo"
if printf '%s\n' "$controller_delta" | grep -q 'sent input kind='; then
  echo "controller helper echoed injected input back into the session" >&2
  printf '%s\n' "$controller_delta" >&2
  exit 1
fi

echo "[loop] pass"
echo "[loop] receiver injected the probe and controller helper produced no new sent input events"
