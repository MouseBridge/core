#!/bin/zsh
set -euo pipefail

if [[ $# -lt 2 ]]; then
  echo "usage: $0 <daemon-url> <device-id> [count]" >&2
  exit 1
fi

BASE_URL="$1"
DEVICE_ID="$2"
COUNT="${3:-1000}"

for ((i = 1; i <= COUNT; i++)); do
  curl -fsS -X POST "$BASE_URL/api/session/input" \
    -H 'Content-Type: application/json' \
    -d "{\"device_id\":\"$DEVICE_ID\",\"kind\":\"mouse_move\",\"dx\":1,\"dy\":0}" >/dev/null
done

echo "sent $COUNT mouse_move events to $DEVICE_ID via $BASE_URL"
