#!/bin/zsh
set -euo pipefail

if [[ $# -lt 2 ]]; then
  cat >&2 <<'EOF'
usage: verify/local-session-smoke.sh <controller-base-url> <receiver-base-url> [text]

example:
  verify/local-session-smoke.sh http://127.0.0.1:39273 http://127.0.0.1:39272 "MouseBridge smoke"

What it does:
  1. connects controller -> receiver
  2. auto-pairs using the receiver PIN
  3. finds the active remote session device_id
  4. sends a scripted remote sequence: move, click, type text

Before running:
  - both daemons and both helpers must already be running
  - focus a safe text field or a fresh TextEdit document on the local Mac
EOF
  exit 1
fi

CONTROLLER_URL="${1%/}"
RECEIVER_URL="${2%/}"
TEXT="${3:-MouseBridge local smoke}"

restore_controller_capture="true"
restore_receiver_capture="true"

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

trap restore_capture_best_effort EXIT

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

read_mouse_location() {
  swift -e 'import Cocoa; let p = NSEvent.mouseLocation; print("\(Int(p.x)) \(Int(p.y))")'
}

json_quote() {
  swift -e 'import Foundation; let data = FileHandle.standardInput.readDataToEndOfFile(); let value = String(decoding: data, as: UTF8.self); let encoded = try! JSONEncoder().encode(value); print(String(decoding: encoded, as: UTF8.self))'
}

echo "[smoke] connecting controller -> receiver"
receiver_hostport="${RECEIVER_URL#http://}"
receiver_host="${receiver_hostport%:*}"
receiver_port="${receiver_hostport##*:}"
device_id="$(current_session_device_id "$CONTROLLER_URL")"
if [[ -z "$device_id" ]]; then
  curl -fsS -X POST "$CONTROLLER_URL/api/connect" \
    -H 'Content-Type: application/json' \
    -d "{\"host\":\"$receiver_host\",\"port\":$receiver_port}" >/dev/null
else
  echo "[smoke] session already active, skipping new connect"
fi

restore_controller_capture="$(json_get "$CONTROLLER_URL/api/status" | extract_capture_enabled)"
restore_receiver_capture="$(json_get "$RECEIVER_URL/api/status" | extract_capture_enabled)"

echo "[smoke] disabling local capture on both sides for deterministic local verification"
set_capture_state "$CONTROLLER_URL" false
set_capture_state "$RECEIVER_URL" false
wait_for_capture_state "$CONTROLLER_URL" false
wait_for_capture_state "$RECEIVER_URL" false

device_id="$(current_session_device_id "$CONTROLLER_URL")"
if [[ -z "$device_id" ]]; then
  echo "[smoke] waiting for receiver PIN"
  pin="$(wait_for_pin "$RECEIVER_URL")"
  echo "[smoke] PIN acquired: $pin"

  controller_status="$(json_get "$CONTROLLER_URL/api/status")"
  pairing_id="$(printf '%s' "$controller_status" | sed -n 's/.*"pairing_id":"\([^"]*\)".*/\1/p')"
  if [[ -z "$pairing_id" ]]; then
    echo "controller has no pending pairing_id" >&2
    exit 1
  fi

  echo "[smoke] confirming pair on controller"
  curl -fsS -X POST "$CONTROLLER_URL/api/pair/pin" \
    -H 'Content-Type: application/json' \
    -d "{\"pairing_id\":\"$pairing_id\",\"pin\":\"$pin\"}" >/dev/null
else
  echo "[smoke] remembered session connected without PIN"
fi

echo "[smoke] waiting for active session"
device_id="$(wait_for_session_device_id "$CONTROLLER_URL")"
echo "[smoke] remote session device_id: $device_id"

location=(${(ps: :)$(read_mouse_location)})
current_x="${location[1]}"
current_y="${location[2]}"
target_x=$(( current_x + 80 ))
target_y=$(( current_y - 20 ))
dx=$(( target_x - current_x ))
dy=$(( target_y - current_y ))

echo "[smoke] sending remote move/click/text sequence"
payload=$(cat <<EOF
{
  "device_id": "$device_id",
  "step_delay_ms": 35,
  "inputs": [
    {"kind":"mouse_move","dx":$dx,"dy":$dy},
    {"kind":"mouse_button","button":"left","pressed":true},
    {"kind":"mouse_button","button":"left","pressed":false},
    {"kind":"text","text":$(printf '%s' "$TEXT" | json_quote)}
  ]
}
EOF
)
curl -fsS -X POST "$CONTROLLER_URL/api/session/input/batch" \
  -H 'Content-Type: application/json' \
  -d "$payload"

echo "[smoke] restoring capture state"
restore_capture
trap - EXIT

echo
echo "[smoke] complete"
echo "[smoke] expected visible effect: cursor moves slightly, clicks, then types: $TEXT"
