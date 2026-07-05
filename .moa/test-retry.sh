#!/usr/bin/env bash
# Full -race suite with up to 3 attempts, tolerating known PRE-EXISTING timing
# flakes (TestManagerShutdown_WaitsForActiveRun, TestWebSocket_PermissionDenied*)
# that flake under CPU contention in this VM and are unrelated to the change set.
# Fails only if a run fails for a DIFFERENT reason, or all 3 attempts fail.
set -uo pipefail
cd /home/ealeixandre/dev/moa/main
KNOWN_FLAKY='TestManagerShutdown_WaitsForActiveRun|TestWebSocket_PermissionDenied_OrdersToolStartBeforePromptAndMarksRejected'
for attempt in 1 2 3; do
  echo "### test attempt $attempt/3"
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
echo "### test: FAIL — known flakes did not settle in 3 attempts"
exit 1
