// ws-handlers.js — WebSocket event handler public barrel.

export { normalizeHistory, appendNormalizedHistoryDelta, normalizeConversationProjection } from './ws/history.js';
export { attentionNamespaceFromInit, attentionNamespaceTransition, adoptAttentionNamespace, handleWsAskUser, handleWsPermissionRequest, handleWsPermissionResolved, handleWsAskResolved } from './ws/attention.js';
export { handleWsInit } from './ws/init.js';
export { handleWsMessageStart, handleWsTextDelta, handleWsThinkingDelta, handleWsMessageEnd, handleWsRunTokens, handleWsRunEnd, handleWsUserMessage, handleWsSteer, handleWsSteersCanceled } from './ws/stream.js';
export { handleWsToolCallStart, handleWsToolCallDelta, handleWsToolStart, handleWsToolUpdate, handleWsToolEnd } from './ws/tools.js';
export { handleWsSubagentTitle, handleWsSubagentCount, handleWsSubagentComplete, handleWsSubagentStart, handleWsSubagentUsage, handleWsSubagentEvent, handleWsSubagentEnd, upsertTerminalSubagentOutcome } from './ws/subagents.js';
export { handleWsBashComplete, handleWsBashJobStart, handleWsBashJobOutput, handleWsBashJobEnd } from './ws/bash.js';
export { handleWsStateChange, handleWsConfigChange, handleWsContextUpdate, handleWsMcpChange, handleWsSessionCost, handleWsRateLimit, handleWsCommandQueued, handleWsCommandDequeued, handleWsTasksUpdate, handleWsCommand, handleWsGoalChange, handleWsGoalIteration, handleWsGoalVerify, handleWsGoalEnd, handleWsAutoVerifyStart, handleWsAutoVerifyEnd, handleWsCompactionStart, handleWsCompactionEnd } from './ws/session.js';
