#!/usr/bin/env bash
# Minimal ETag-polling client in ~20 lines of shell + curl.
#
# Usage: bash examples/poll.sh [base-url] [interval-seconds]
#
# Fetches /v1/flags once, then revalidates with If-None-Match on every
# tick. While nothing changes, each poll is a bodyless 304; when you edit
# the flag file the next poll gets the full new snapshot. This is exactly
# what an SDK's background refresher does.
set -euo pipefail

BASE="${1:-http://127.0.0.1:4949}"
INTERVAL="${2:-2}"
ETAG=""

while true; do
  if [ -n "$ETAG" ]; then
    RESPONSE="$(curl -fsS -D - -H "If-None-Match: $ETAG" "$BASE/v1/flags")"
  else
    RESPONSE="$(curl -fsS -D - "$BASE/v1/flags")"
  fi
  STATUS="$(printf '%s' "$RESPONSE" | head -n1 | awk '{print $2}')"
  if [ "$STATUS" = "304" ]; then
    echo "$(date +%T) 304 — unchanged"
  else
    ETAG="$(printf '%s' "$RESPONSE" | tr -d '\r' | sed -n 's/^[Ee][Tt]ag: //p')"
    echo "$(date +%T) 200 — snapshot changed, new ETag $ETAG"
    printf '%s\n' "$RESPONSE" | sed '1,/^\r\{0,1\}$/d' | head -n 6
  fi
  sleep "$INTERVAL"
done
