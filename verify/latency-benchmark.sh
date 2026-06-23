#!/bin/zsh
set -euo pipefail

if [[ $# -lt 1 ]]; then
  echo "usage: $0 <daemon-url> [samples] [interval-seconds]" >&2
  exit 1
fi

if ! command -v jq >/dev/null 2>&1; then
  echo "jq is required" >&2
  exit 1
fi

BASE_URL="$1"
SAMPLES="${2:-30}"
INTERVAL="${3:-1}"

values=()

for ((i = 1; i <= SAMPLES; i++)); do
  sample="$(curl -fsS "$BASE_URL/api/status" | jq -r '.sessions[]?.avg_latency_ms' | head -n1)"
  if [[ -n "${sample:-}" ]]; then
    values+=("$sample")
    echo "[$i/$SAMPLES] avg_latency_ms=$sample"
  else
    echo "[$i/$SAMPLES] no active session"
  fi
  sleep "$INTERVAL"
done

if [[ ${#values[@]} -eq 0 ]]; then
  echo "no latency samples collected" >&2
  exit 1
fi

printf '%s\n' "${values[@]}" | sort -n > /tmp/mousebridge-latency-samples.txt
count="${#values[@]}"
min="$(head -n1 /tmp/mousebridge-latency-samples.txt)"
max="$(tail -n1 /tmp/mousebridge-latency-samples.txt)"
p50_index=$(( (count + 1) / 2 ))
p95_index=$(( (count * 95 + 99) / 100 ))
p50="$(sed -n "${p50_index}p" /tmp/mousebridge-latency-samples.txt)"
p95="$(sed -n "${p95_index}p" /tmp/mousebridge-latency-samples.txt)"

echo "summary: count=$count min=$min p50=$p50 p95=$p95 max=$max"
