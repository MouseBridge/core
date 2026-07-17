#!/bin/zsh
set -euo pipefail

CORE_DIR="$(cd "$(dirname "$0")/.." && pwd)"
WORKSPACE_DIR="$(cd "$CORE_DIR/.." && pwd)"
HELPER_DIR="$WORKSPACE_DIR/helper"

BASE_DIR="${MB_LOCAL_LAB_BASE_DIR:-/tmp/mousebridge-local-lab}"
CONTROLLER_DIR="$BASE_DIR/controller"
RECEIVER_DIR="$BASE_DIR/receiver"

CONTROLLER_URL="http://127.0.0.1:39273"
RECEIVER_URL="http://127.0.0.1:39272"

DAEMON_BIN="/tmp/mousebridge-local-lab-daemon"
CONTROLLER_DAEMON_LOG="$BASE_DIR/controller-daemon.log"
RECEIVER_DAEMON_LOG="$BASE_DIR/receiver-daemon.log"
CONTROLLER_HELPER_LOG="$BASE_DIR/controller-helper.log"
RECEIVER_HELPER_LOG="$BASE_DIR/receiver-helper.log"
SUMMARY_LOG="${MB_LOCAL_LAB_SUMMARY_PATH:-$BASE_DIR/local-lab-summary.log}"

CONTROLLER_DAEMON_PID=""
RECEIVER_DAEMON_PID=""
CONTROLLER_HELPER_PID=""
RECEIVER_HELPER_PID=""

log() {
  echo "[lab] $*"
}

cleanup() {
  for pid in "$CONTROLLER_HELPER_PID" "$RECEIVER_HELPER_PID" "$CONTROLLER_DAEMON_PID" "$RECEIVER_DAEMON_PID"; do
    if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
      kill "$pid" >/dev/null 2>&1 || true
      wait "$pid" >/dev/null 2>&1 || true
    fi
  done
}

trap cleanup EXIT INT TERM

write_config() {
  local dir="$1"
  local port="$2"
  local name="$3"
  mkdir -p "$dir"
  cat > "$dir/config.json" <<EOF
{
  "listen_host": "127.0.0.1",
  "port": $port,
  "unsafe_http_lan": false,
  "remembered_enabled": true,
  "remembered_auto_connect_enabled": false,
  "pairing_pin_ttl_seconds": 120,
  "pairing_pin_max_attempts": 3,
  "connect_timeout_seconds": 5,
  "json_body_limit_bytes": 65536,
  "device_name": "$name",
  "hotkeys": {
    "switch_next": "ctrl+alt+right",
    "switch_prev": "ctrl+alt+left",
    "switch_to_host": "ctrl+alt+escape",
    "disconnect_all": "",
    "toggle_pause": ""
  }
}
EOF
}

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
    if curl -fsS "$url/api/status" | sed -nE 's/.*"connected":(true|false).*/\1/p' | grep -q true; then
      return 0
    fi
    sleep 0.2
  done
  echo "timed out waiting for helper at $url" >&2
  return 1
}

extract_pin() {
  sed -n 's/.*"display_pin":"\([0-9][0-9][0-9][0-9][0-9][0-9]\)".*/\1/p'
}

extract_pairing_id() {
  sed -n 's/.*"pairing_id":"\([^"]*\)".*/\1/p'
}

extract_first_session_device_id() {
  sed -n 's/.*"sessions":\[[^]]*"device_id":"\([^"]*\)".*/\1/p'
}

wait_for_pin() {
  local url="$1"
  for _ in {1..50}; do
    local pin
    pin="$(curl -fsS "$url/api/status" | extract_pin)"
    if [[ -n "$pin" ]]; then
      echo "$pin"
      return 0
    fi
    sleep 0.2
  done
  echo "timed out waiting for pin at $url" >&2
  return 1
}

wait_for_pairing_id() {
  local url="$1"
  for _ in {1..50}; do
    local pairing_id
    pairing_id="$(curl -fsS "$url/api/status" | extract_pairing_id)"
    if [[ -n "$pairing_id" ]]; then
      echo "$pairing_id"
      return 0
    fi
    sleep 0.2
  done
  echo "timed out waiting for pairing id at $url" >&2
  return 1
}

wait_for_session() {
  local url="$1"
  for _ in {1..50}; do
    local device_id
    device_id="$(curl -fsS "$url/api/status" | extract_first_session_device_id)"
    if [[ -n "$device_id" ]]; then
      echo "$device_id"
      return 0
    fi
    sleep 0.2
  done
  echo "timed out waiting for session at $url" >&2
  return 1
}

start_daemon() {
  local runtime_dir="$1"
  local log_file="$2"
  (
    "$DAEMON_BIN" daemon --data-dir "$runtime_dir" >"$log_file" 2>&1
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

mkdir -p "$BASE_DIR"
rm -f "$SUMMARY_LOG" "$CONTROLLER_DAEMON_LOG" "$RECEIVER_DAEMON_LOG" "$CONTROLLER_HELPER_LOG" "$RECEIVER_HELPER_LOG"
rm -rf "$CONTROLLER_DIR" "$RECEIVER_DIR"

log "building core and helper"
(
  cd "$CORE_DIR"
  env GOCACHE="$CORE_DIR/.cache/go-build" go build -o "$DAEMON_BIN" ./cmd/mousebridge >/dev/null
)
(
  cd "$HELPER_DIR"
  swift build >/dev/null
)

write_config "$CONTROLLER_DIR" 39273 "MouseBridge-Controller"
write_config "$RECEIVER_DIR" 39272 "MouseBridge-Receiver"

log "starting controller daemon"
CONTROLLER_DAEMON_PID="$(start_daemon "$CONTROLLER_DIR" "$CONTROLLER_DAEMON_LOG")"
log "starting receiver daemon"
RECEIVER_DAEMON_PID="$(start_daemon "$RECEIVER_DIR" "$RECEIVER_DAEMON_LOG")"

wait_for_http "$CONTROLLER_URL"
wait_for_http "$RECEIVER_URL"

log "starting controller helper"
CONTROLLER_HELPER_PID="$(start_helper "$CONTROLLER_DIR" "$CONTROLLER_HELPER_LOG")"
log "starting receiver helper"
RECEIVER_HELPER_PID="$(start_helper "$RECEIVER_DIR" "$RECEIVER_HELPER_LOG")"

wait_for_helper "$CONTROLLER_URL"
wait_for_helper "$RECEIVER_URL"

log "connecting controller -> receiver"
curl -fsS -X POST "$CONTROLLER_URL/api/connect" \
  -H 'Content-Type: application/json' \
  -d '{"host":"127.0.0.1","port":39272}' >/dev/null

pin="$(wait_for_pin "$RECEIVER_URL")"
pairing_id="$(wait_for_pairing_id "$CONTROLLER_URL")"

log "confirming pair"
curl -fsS -X POST "$CONTROLLER_URL/api/pair/pin" \
  -H 'Content-Type: application/json' \
  -d "{\"pairing_id\":\"$pairing_id\",\"pin\":\"$pin\"}" >/dev/null

remote_session_device_id="$(wait_for_session "$CONTROLLER_URL")"

cat > "$SUMMARY_LOG" <<EOF
state=ready
controller_url=$CONTROLLER_URL
receiver_url=$RECEIVER_URL
remote_session_device_id=$remote_session_device_id
controller_daemon_log=$CONTROLLER_DAEMON_LOG
receiver_daemon_log=$RECEIVER_DAEMON_LOG
controller_helper_log=$CONTROLLER_HELPER_LOG
receiver_helper_log=$RECEIVER_HELPER_LOG
EOF

log "local lab ready controller_url=$CONTROLLER_URL receiver_url=$RECEIVER_URL remote_session_device_id=$remote_session_device_id"

while true; do
  sleep 5
done
