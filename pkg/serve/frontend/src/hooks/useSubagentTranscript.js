import { useEffect } from "preact/hooks";
import { hydrateSubagentTranscript } from "../data/subagent-transcript.js";

// useSubagentTranscript — backfills the child's earlier history when a
// subagent view opens. A subagent launched while this conversation was already
// on screen only ever received live deltas, so its entry starts after the
// delegated task instead of at it (see data/subagent-transcript.js).
//
// The cancellation flag is not an abort of the request but of its APPLICATION:
// a view closed mid-flight must not have a merged transcript written under it.
// The lifecycle flag changes when a reconnect snapshot no longer lists the
// viewed job. It deliberately retriggers this effect even when the transcript
// body was already hydrated, so a missed terminal event is reconciled from the
// persisted summary.
export function useSubagentTranscript(sessionId, jobId, lifecycleUnverified = false) {
  useEffect(() => {
    if (!sessionId || !jobId) return;
    let active = true;
    hydrateSubagentTranscript(sessionId, jobId, () => active).catch(() => {});
    return () => { active = false; };
  }, [sessionId, jobId, lifecycleUnverified]);
}
