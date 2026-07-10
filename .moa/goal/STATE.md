# Goal state

## Objective
Implement all actionable work in `tmp/review-5-6-master-plan.md`.

## Done (published)
- Review hardening through `19c280a`; consult master plan for the complete commit/status list.
- F05/F06 stages 1–3: stable MsgID, tree sync identity and compaction boundary IDs.

## Current iteration
- P1 F05: add compaction usage to `CompactionPayload` so the forthcoming run-level cost accumulator can count compaction calls without rereading agent history.

## Next
- Finish P1 run-level stats after this isolated payload change.
- Then P2 WS reconciliation, P3 session restore convergence, P4 async Bash jobs, P5/P6 hardening, and parity backlog.

## Discarded
- Do not retry empty Sonnet jobs as evidence of progress; inspect actual diffs/tests.
