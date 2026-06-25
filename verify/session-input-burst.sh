#!/bin/zsh
set -euo pipefail

if [[ $# -lt 2 ]]; then
  echo "usage: $0 <daemon-url> <device-id> [count] [batch-size]" >&2
  exit 1
fi

BASE_URL="$1"
DEVICE_ID="$2"
COUNT="${3:-1000}"
BATCH_SIZE="${4:-50}"

sent=0
while (( sent < COUNT )); do
  remaining=$(( COUNT - sent ))
  chunk_size=$BATCH_SIZE
  if (( remaining < chunk_size )); then
    chunk_size=$remaining
  fi

  inputs=""
  for ((i = 1; i <= chunk_size; i++)); do
    if [[ -n "$inputs" ]]; then
      inputs+=","
    fi
    inputs+='{"kind":"mouse_move","dx":1,"dy":0}'
  done

  curl -fsS -X POST "$BASE_URL/api/session/input/batch" \
    -H 'Content-Type: application/json' \
    -d "{\"device_id\":\"$DEVICE_ID\",\"step_delay_ms\":0,\"inputs\":[$inputs]}" >/dev/null

  sent=$(( sent + chunk_size ))
done

echo "sent $COUNT mouse_move events to $DEVICE_ID via $BASE_URL using batch_size=$BATCH_SIZE"
