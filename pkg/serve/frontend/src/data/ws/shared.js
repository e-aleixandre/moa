// Mutable WebSocket batching state shared by event-topic modules.

export const wsState = {
  pendingTextDeltas: {},
  pendingThinkingDeltas: {},
  pendingToolDeltas: {},
  pendingBashDeltas: {}, // sessionId → { jobId → { delta, ownerAgentId } }
  pendingToolCallBuffers: {}, // sessionId → { toolCallId → { args } }
  materializedTextDuringMessage: {},
  flushScheduled: false,
  subagentBuffers: {}, // "sessionId:jobId" → reducer buffers
  pendingSubagentEvents: {}, // sessionId → [{ jobId, evt }]
  subagentFlushScheduled: false,
};

// normalizeBackgroundCompaction maps the wire object to session state; anything
// that is not a well-formed object means "none" (older servers omit it).
export function normalizeBackgroundCompaction(data) {
  if (!data || typeof data !== 'object') return null;
  if (!Number.isFinite(data.revision)) return null;
  return {
    jobId: data.job_id,
    revision: data.revision,
    active: data.active === true,
    waiting: data.waiting === true,
  };
}
