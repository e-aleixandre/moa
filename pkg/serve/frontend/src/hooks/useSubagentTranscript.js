import { useEffect } from "preact/hooks";
import { hydrateSubagentTranscript } from "../data/subagent-transcript.js";

// useSubagentTranscript — backfills the child's earlier history when a
// subagent view opens. A subagent launched while this conversation was already
// on screen only ever received live deltas, so its entry starts after the
// delegated task instead of at it (see data/subagent-transcript.js).
//
// The cancellation flag is not an abort of the request but of its APPLICATION:
// a view closed mid-flight must not have a merged transcript written under it.
export function useSubagentTranscript(sessionId, jobId) {
  useEffect(() => {
    if (!sessionId || !jobId) return;
    let active = true;
    hydrateSubagentTranscript(sessionId, jobId, () => active).catch(() => {});
    return () => { active = false; };
  }, [sessionId, jobId]);
}
