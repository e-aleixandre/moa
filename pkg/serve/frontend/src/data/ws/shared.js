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
