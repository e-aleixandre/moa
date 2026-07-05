#!/usr/bin/env bash
# Full -race suite with up to 4 attempts, tolerating a small allowlist of
# PRE-EXISTING timing-flaky tests that flake under CPU contention in this VM and
# are unrelated to the attachments change set:
#   - TestManagerShutdown_WaitsForActiveRun (asserts a >=100ms wait vs a mock delay)
#   - TestWebSocket_* (10s WebSocket read-timeout tests; the whole family shares
#     the same root cause and is unrelated to attachments)
# All are verified to pass in isolation and not touch attachment code. Any
# failure of a test OUTSIDE this allowlist aborts immediately (no masking of
# real regressions).
set -uo pipefail
cd /home/ealeixandre/dev/moa/main
KNOWN_FLAKY='TestManagerShutdown_WaitsForActiveRun|TestWebSocket_[A-Za-z0-9_]+'
for attempt in 1 2 3 4; do
  echo "### test attempt $attempt/4"
  out="$(go test -race -count=1 ./... 2>&1)"; rc=$?
  echo "$out"
  if [[ $rc -eq 0 ]]; then
    echo "### test: PASS on attempt $attempt"
    exit 0
  fi
  # Collect the names of failing tests.
  failed="$(echo "$out" | grep -E '^--- FAIL' | sed -E 's/^--- FAIL: ([^ ]+).*/\1/' | sort -u)"
  echo "### failing tests: $(echo "$failed" | tr '\n' ' ')"
  # If any failing test is NOT in the known-flaky allowlist, fail immediately.
  unexpected="$(echo "$failed" | grep -vE "^(${KNOWN_FLAKY})$" || true)"
  if [[ -n "$unexpected" ]]; then
    echo "### test: FAIL — unexpected (non-flaky) failure: $unexpected"
    exit 1
  fi
  echo "### only known pre-existing timing flakes failed; retrying..."
done
echo "### test: FAIL — known flakes did not settle in 4 attempts"
exit 1
