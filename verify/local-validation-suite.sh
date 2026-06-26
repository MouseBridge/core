#!/bin/zsh
set -euo pipefail
set +x
set +v

CORE_DIR="$(cd "$(dirname "$0")/.." && pwd)"
WORKSPACE_DIR="$(cd "$CORE_DIR/.." && pwd)"
HELPER_DIR="$WORKSPACE_DIR/helper"
RUNTIME_A="$WORKSPACE_DIR/runtime-data-a"
RUNTIME_B="$WORKSPACE_DIR/runtime-data-b"

CONTROLLER_URL="http://127.0.0.1:39273"
RECEIVER_URL="http://127.0.0.1:39272"

DAEMON_A_LOG="/tmp/mousebridge-suite-daemon-a.log"
DAEMON_B_LOG="/tmp/mousebridge-suite-daemon-b.log"
HELPER_A_LOG="/tmp/mousebridge-suite-helper-a.log"
HELPER_B_LOG="/tmp/mousebridge-suite-helper-b.log"
SUMMARY_LOG="${MB_VALIDATION_SUMMARY_PATH:-/tmp/mousebridge-local-validation-summary.log}"
CORE_DAEMON_BIN="/tmp/mousebridge-validation-daemon"

TEXT="${1:-MouseBridge local suite}"
BURST_COUNT="${2:-240}"
BURST_BATCH_SIZE="${3:-40}"
LATENCY_SAMPLES="${4:-12}"
LATENCY_INTERVAL="${5:-0.1}"

DAEMON_A_PID=""
DAEMON_B_PID=""
HELPER_A_PID=""
HELPER_B_PID=""

usage() {
  cat <<'EOF'
usage: verify/local-validation-suite.sh [text] [burst-count] [burst-batch-size] [latency-samples] [latency-interval]

example:
  verify/local-validation-suite.sh "MouseBridge local suite" 240 40 12 0.1

What it does:
  1. builds core/helper if needed
  2. starts two local daemons and two helpers
  3. opens TextEdit with a fresh document
  4. runs:
     - local-session-smoke.sh
     - local-loop-safety.sh
     - local-performance-benchmark.sh
  5. prints a single summary and stops everything it started

Outputs:
  /tmp/mousebridge-local-validation-summary.log
  /tmp/mousebridge-suite-daemon-a.log
  /tmp/mousebridge-suite-daemon-b.log
  /tmp/mousebridge-suite-helper-a.log
  /tmp/mousebridge-suite-helper-b.log
EOF
}

if [[ "${1:-}" == "--help" || "${1:-}" == "-h" ]]; then
  usage
  exit 0
fi

log() {
  echo "[suite] $*"
}

cleanup() {
  for pid in "$HELPER_A_PID" "$HELPER_B_PID" "$DAEMON_A_PID" "$DAEMON_B_PID"; do
    if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
      kill "$pid" >/dev/null 2>&1 || true
      wait "$pid" >/dev/null 2>&1 || true
    fi
  done
}

trap cleanup EXIT

wait_for_http() {
  local url="$1"
  for _ in {1..50}; do
    if curl -fsS "$url/api/status" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.2
  done
  echo "timed out waiting for daemon at $url" >&2
  return 1
}

wait_for_helper() {
  local url="$1"
  for _ in {1..50}; do
    if curl -fsS "$url/api/status" | jq -e '.helper_runtime.connected == true' >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.2
  done
  echo "timed out waiting for helper at $url" >&2
  return 1
}

build_if_needed() {
  log "building core and helper"
  (
    cd "$CORE_DIR"
    env GOCACHE="$CORE_DIR/.cache/go-build" go build -o "$CORE_DAEMON_BIN" ./cmd/mousebridge >/dev/null
  )
  (
    cd "$HELPER_DIR"
    swift build >/dev/null
  )
}

start_daemon() {
  local runtime_dir="$1"
  local log_file="$2"
  (
    "$CORE_DAEMON_BIN" daemon --data-dir "$runtime_dir" >"$log_file" 2>&1
  ) &
  echo $!
}

start_helper() {
  local runtime_dir="$1"
  local log_file="$2"
  (
    cd "$HELPER_DIR"
    env MB_HELPER_VERBOSE_INPUT=1 ./.build/debug/mousebridge-helper run --data-dir "$runtime_dir" >"$log_file" 2>&1
  ) &
  echo $!
}

prepare_textedit() {
  log "opening TextEdit"
  open -a TextEdit >/dev/null 2>&1 || true
  osascript \
    -e 'tell application "TextEdit" to activate' \
    -e 'tell application "System Events" to keystroke "n" using command down' \
    >/dev/null 2>&1 || true
}

run_and_capture() {
  local label="$1"
  shift
  log "running $label"
  {
    echo "=== $label ==="
    "$@"
    echo
  } | tee -a "$SUMMARY_LOG"
}

rm -f "$SUMMARY_LOG" "$DAEMON_A_LOG" "$DAEMON_B_LOG" "$HELPER_A_LOG" "$HELPER_B_LOG"

build_if_needed

log "starting controller daemon at $CONTROLLER_URL"
DAEMON_B_PID="$(start_daemon "$RUNTIME_B" "$DAEMON_B_LOG")"
log "starting receiver daemon at $RECEIVER_URL"
DAEMON_A_PID="$(start_daemon "$RUNTIME_A" "$DAEMON_A_LOG")"

wait_for_http "$CONTROLLER_URL"
wait_for_http "$RECEIVER_URL"

log "starting controller helper"
HELPER_B_PID="$(start_helper "$RUNTIME_B" "$HELPER_B_LOG")"
log "starting receiver helper"
HELPER_A_PID="$(start_helper "$RUNTIME_A" "$HELPER_A_LOG")"

wait_for_helper "$CONTROLLER_URL"
wait_for_helper "$RECEIVER_URL"

prepare_textedit

echo "MouseBridge local validation suite" >"$SUMMARY_LOG"
echo >>"$SUMMARY_LOG"

run_and_capture "smoke" \
  "$CORE_DIR/verify/local-session-smoke.sh" \
  "$CONTROLLER_URL" \
  "$RECEIVER_URL" \
  "$TEXT"

run_and_capture "anti-loop" \
  "$CORE_DIR/verify/local-loop-safety.sh" \
  "$CONTROLLER_URL" \
  "$RECEIVER_URL" \
  "$HELPER_B_LOG" \
  "$HELPER_A_LOG" \
  "$TEXT anti-loop"

run_and_capture "benchmark" \
  "$CORE_DIR/verify/local-performance-benchmark.sh" \
  "$CONTROLLER_URL" \
  "$RECEIVER_URL" \
  "$HELPER_A_LOG" \
  "$BURST_COUNT" \
  "$BURST_BATCH_SIZE" \
  "$LATENCY_SAMPLES" \
  "$LATENCY_INTERVAL"

log "validation suite complete"
log "summary written to $SUMMARY_LOG"
