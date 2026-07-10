# Goal state

## Objective
Implement all actionable work in `tmp/review-5-6-master-plan.md`.

## Done (published)
- Review hardening through `19c280a`; consult master plan for the complete commit/status list.
- F05/F06 stages 1–3: stable MsgID, tree sync identity and compaction boundary IDs.
- `3f253c1 include compaction usage in lifecycle events`: P1 groundwork; automatic and manual compaction publish their provider usage.

## Current iteration
- Finished isolated P1 groundwork: compaction usage propagation.

## Next
- P1: implement per-RunGen stats in SessionContext/bridge/startRun and adversarial regressions.
- Then P2 WS reconciliation, P3 session restore convergence, P4 async Bash jobs, P5/P6 hardening, and parity backlog.

## Discarded
- Do not retry empty Sonnet jobs as evidence of progress; inspect actual diffs/tests.
