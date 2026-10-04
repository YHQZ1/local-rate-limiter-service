#!/usr/bin/env bash
# Black-box checks against a running ratelimiter.
# Usage: scripts/smoke.sh [base-url] [expected-version]
#   base-url          default http://localhost:8080
#   expected-version  if given, /version must report it
# Works with any limits as long as the rate is low enough that a burst of
# burst+5 back-to-back requests overruns the bucket (CI uses 1/s, burst 3).
set -u

BASE=${1:-http://localhost:8080}
WANT_VERSION=${2:-}
FAILS=0

pass() { echo "  ok   $1"; }
fail() { echo "  FAIL $1"; FAILS=$((FAILS + 1)); }
check() { # check <description> <command...>
  local desc=$1; shift
  if "$@" >/dev/null 2>&1; then pass "$desc"; else fail "$desc"; fi
}

echo "smoke test against $BASE"

check "GET /health reports ok" bash -c "curl -sf '$BASE/health' | grep -q '\"status\":\"ok\"'"
if [ -n "$WANT_VERSION" ]; then
  check "GET /version is $WANT_VERSION" bash -c "curl -sf '$BASE/version' | grep -q '\"version\":\"$WANT_VERSION\"'"
fi
check "GET / serves the UI" bash -c "curl -sf '$BASE/' | grep -q '<title>ratelimiter</title>'"

burst=$(curl -sf "$BASE/config" | sed -n 's/.*"burst":\([0-9]*\).*/\1/p')
if [ -z "$burst" ]; then fail "GET /config returns a burst size"; burst=3; else pass "GET /config returns a burst size ($burst)"; fi

key="smoke-$$-$RANDOM"
first=""; denied=0; retry_ok=0
for i in $(seq 1 $((burst + 5))); do
  out=$(curl -s -i -X POST "$BASE/check" -d "{\"key\":\"$key\"}")
  code=$(printf '%s' "$out" | head -1 | awk '{print $2}')
  [ -z "$first" ] && first=$code
  if [ "$code" = "429" ]; then
    denied=$((denied + 1))
    printf '%s' "$out" | grep -qi '^Retry-After: [0-9]' && retry_ok=1
  fi
done
[ "$first" = "200" ] && pass "first request for a new key is allowed" || fail "first request for a new key is allowed (got $first)"
[ "$denied" -ge 1 ] && pass "burst is eventually denied with 429 ($denied denied)" || fail "burst is eventually denied with 429"
[ "$retry_ok" = 1 ] && pass "429 carries a Retry-After header" || fail "429 carries a Retry-After header"

code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/check" -d '{"nope":1}')
[ "$code" = "400" ] && pass "malformed body is rejected with 400" || fail "malformed body is rejected with 400 (got $code)"

check "metrics count the denied checks" bash -c "curl -sf '$BASE/metrics' | grep -qE 'ratelimiter_checks_total\{result=\"denied\"\} [1-9]'"
check "metrics expose latency histogram" bash -c "curl -sf '$BASE/metrics' | grep -q 'ratelimiter_http_request_duration_seconds_bucket'"

if [ "$FAILS" -eq 0 ]; then echo "all checks passed"; else echo "$FAILS check(s) failed"; exit 1; fi
