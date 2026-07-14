#!/usr/bin/env bash
# End-to-end smoke test for flagstead: builds the binary, generates a flag
# file, exercises every CLI subcommand, then runs a real HTTP server on a
# loopback port and asserts on ETag polling, evaluation, hot reload and
# the broken-edit safety net. No network beyond 127.0.0.1, idempotent,
# finishes in seconds.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKDIR="$(mktemp -d)"
SERVER_PID=""
cleanup() {
  [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null || true
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

fail() {
  echo "SMOKE FAIL: $*" >&2
  exit 1
}

BIN="$WORKDIR/flagstead"
FLAGS="$WORKDIR/flags.toml"

echo "1. build"
(cd "$ROOT" && go build -o "$BIN" ./cmd/flagstead) || fail "go build failed"

echo "2. version matches manifest"
"$BIN" version | grep -qx "flagstead 0.1.0" || fail "version mismatch"

echo "3. init writes a starter file that passes check"
"$BIN" init "$FLAGS" >/dev/null || fail "init failed"
"$BIN" check --file "$FLAGS" | grep -q "OK (3 flags" || fail "starter fails check"
if "$BIN" init "$FLAGS" >/dev/null 2>&1; then
  fail "init must refuse to overwrite"
fi

echo "4. check rejects a broken file with the line number, exit 1"
printf '[flags.bad]\nenabled = tru\n' > "$WORKDIR/bad.toml"
set +e
OUT="$("$BIN" check --file "$WORKDIR/bad.toml" 2>&1)"
CODE=$?
set -e
[ "$CODE" -eq 1 ] || fail "check on broken file exited $CODE, want 1"
echo "$OUT" | grep -q "line 2" || fail "check should point at line 2"

echo "5. list and get read the file"
"$BIN" list --file "$FLAGS" | grep -q "new-checkout" || fail "list missing flag"
"$BIN" get new-checkout --file "$FLAGS" | grep -q '"rollout": 25' || fail "get wrong"

echo "6. eval is deterministic and honors rules"
E1="$("$BIN" eval new-checkout --file "$FLAGS" --key user-42 --format json)"
E2="$("$BIN" eval new-checkout --file "$FLAGS" --key user-42 --format json)"
[ "$E1" = "$E2" ] || fail "eval is not deterministic"
"$BIN" eval new-checkout --file "$FLAGS" --key user-42 --attr country=JP \
  | grep -q "reason   rule" || fail "country rule did not match"

echo "7. serve binds loopback and reports readiness"
"$BIN" serve --file "$FLAGS" --addr 127.0.0.1:0 > "$WORKDIR/serve.log" &
SERVER_PID=$!
ADDR=""
for _ in $(seq 1 50); do
  ADDR="$(sed -n 's|.*on http://||p' "$WORKDIR/serve.log")"
  [ -n "$ADDR" ] && break
  sleep 0.1
done
[ -n "$ADDR" ] || fail "server never printed its address"
BASE="http://$ADDR"

echo "8. healthz and flag snapshot"
curl -fsS "$BASE/healthz" | grep -q '"status": "ok"' || fail "healthz not ok"
curl -fsS "$BASE/v1/flags" | grep -q '"new-checkout"' || fail "flags missing"

echo "9. ETag polling: second request is a 304"
ETAG="$(curl -fsSI "$BASE/v1/flags" | tr -d '\r' | sed -n 's/^ETag: //Ip')"
[ -n "$ETAG" ] || fail "no ETag header"
CODE="$(curl -s -o /dev/null -w '%{http_code}' -H "If-None-Match: $ETAG" "$BASE/v1/flags")"
[ "$CODE" = "304" ] || fail "expected 304 on matching ETag, got $CODE"

echo "10. server-side evaluation over HTTP"
curl -fsS "$BASE/v1/eval/new-checkout?key=user-42&attr.country=JP" \
  | grep -q '"enabled": true' || fail "HTTP eval wrong"
curl -fsS -X POST -d '{"key":"user-42"}' "$BASE/v1/eval" \
  | grep -q '"results"' || fail "batch eval wrong"

echo "11. remote config endpoint"
curl -fsS "$BASE/v1/config/api/timeout_ms" | grep -q '"value": 500' \
  || fail "config lookup wrong"

echo "12. hot reload: edit the file, ETag changes, new flag appears"
printf '\n[flags.smoke-new]\nenabled = true\n' >> "$FLAGS"
curl -fsS "$BASE/v1/flags" | grep -q '"smoke-new"' || fail "reload missed new flag"
CODE="$(curl -s -o /dev/null -w '%{http_code}' -H "If-None-Match: $ETAG" "$BASE/v1/flags")"
[ "$CODE" = "200" ] || fail "stale ETag should now yield 200, got $CODE"

echo "13. broken edit keeps serving, healthz degrades, fix recovers"
cp "$FLAGS" "$WORKDIR/good.toml"
printf 'enabled = broken [\n' > "$FLAGS"
curl -fsS "$BASE/v1/flags" | grep -q '"smoke-new"' || fail "last good snapshot lost"
curl -fsS "$BASE/healthz" | grep -q '"status": "degraded"' || fail "healthz not degraded"
cp "$WORKDIR/good.toml" "$FLAGS"
curl -fsS "$BASE/healthz" | grep -q '"status": "ok"' || fail "healthz did not recover"

echo "14. unknown flag is a JSON 404"
CODE="$(curl -s -o /dev/null -w '%{http_code}' "$BASE/v1/flags/ghost")"
[ "$CODE" = "404" ] || fail "unknown flag gave $CODE, want 404"

echo "SMOKE OK"
