// api.js — fetch helpers + centralized WS manager

import {
  handleWsInit, handleWsTextDelta, handleWsThinkingDelta,
  handleWsMessageStart,
  handleWsMessageEnd, handleWsToolStart, handleWsToolUpdate, handleWsToolEnd,
  handleWsToolCallStart, handleWsToolCallDelta,
  handleWsStateChange, handleWsPermissionRequest,
  handleWsPermissionResolved, handleWsAskResolved,
  handleWsConfigChange,
  handleWsSubagentCount, handleWsSubagentComplete, handleWsRunEnd,
  handleWsSubagentStart, handleWsSubagentEvent, handleWsSubagentEnd, handleWsSubagentUsage,
  handleWsBashJobStart, handleWsBashJobOutput, handleWsBashJobEnd, handleWsBashComplete,
  handleWsCommand, handleWsTasksUpdate, handleWsPlanMode,
  handleWsGoalChange, handleWsGoalIteration, handleWsGoalVerify, handleWsGoalEnd,
  handleWsAskUser, handleWsContextUpdate, handleWsSteer, handleWsSteersCanceled,
  handleWsCommandQueued, handleWsCommandDequeued,
  handleWsSessionCost,
  handleWsAutoVerifyStart, handleWsAutoVerifyEnd, handleWsRateLimit,
  handleWsCompactionStart, handleWsCompactionEnd,
} from './ws-handlers.js';

export const REQUEST_HEADERS = Object.freeze({ 'Content-Type': 'application/json', 'X-Moa-Request': '1' });
export const DEFAULT_API_TIMEOUT_MS = 15000;

export async function api(method, path, body, { timeoutMs = DEFAULT_API_TIMEOUT_MS } = {}) {
  const controller = timeoutMs > 0 ? new AbortController() : null;
  const opts = { method, headers: REQUEST_HEADERS };
  if (body) opts.body = JSON.stringify(body);
  if (controller) opts.signal = controller.signal;

  let timedOut = false;
  let timer = null;
  if (controller) {
    timer = setTimeout(() => {
      timedOut = true;
      controller.abort();
    }, timeoutMs);
  }

  try {
    const r = await fetch(path, opts);
    if (timedOut) throw new Error('request aborted');
    if (!r.ok) throw new Error(`${r.status}: ${await r.text()}`);
    if (r.status === 204) return null;
    const text = await r.text();
    if (!text) return null;
    return JSON.parse(text);
  } catch (e) {
    if (timedOut) {
      const error = new Error(`Request timed out after ${timeoutMs}ms: ${method} ${path}`);
      error.name = 'TimeoutError';
      throw error;
    }
    throw e;
  } finally {
    if (timer !== null) clearTimeout(timer);
  }
}

export function getVersion() {
  return api('GET', '/api/version');
}

// --- Centralized WS Manager ---

const connections = new Map();    // sessionId → { ws, backoff, timer }
const pendingTimers = new Map();  // sessionId → timeoutId (for reconnects awaiting retry)
const wantedIds = new Set();      // sessions that should have a connection
const MAX_BACKOFF = 16000;

export function syncConnections(visibleIds) {
  wantedIds.clear();
  for (const id of visibleIds) wantedIds.add(id);

  // Close connections and cancel pending reconnects for sessions no longer visible
  for (const [id, entry] of connections) {
    if (!wantedIds.has(id)) {
      entry.ws.close();
      connections.delete(id);
    }
  }
  for (const [id, timer] of pendingTimers) {
    if (!wantedIds.has(id)) {
      clearTimeout(timer);
      pendingTimers.delete(id);
    }
  }

  // Open connections for newly visible sessions (that aren't already connecting/pending)
  for (const id of visibleIds) {
    if (!connections.has(id) && !pendingTimers.has(id)) {
      openWs(id, 1000);
    }
  }
}

// reconnectAll tears down every live socket and reopens the wanted ones
// immediately with a fresh backoff. Call it when the app returns to the
// foreground or regains network: a socket may be silently half-open (no close
// event ever fired), so the normal onclose→backoff path would never trigger and
// the session would sit frozen until a manual reload.
export function reconnectAll() {
  const ids = [...wantedIds];
  for (const [, entry] of connections) entry.ws.close();
  connections.clear();
  for (const [, timer] of pendingTimers) clearTimeout(timer);
  pendingTimers.clear();
  for (const id of ids) openWs(id, 1000);
}

function openWs(sessionId, initialBackoff) {
  pendingTimers.delete(sessionId);
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  const ws = new WebSocket(`${proto}//${location.host}/api/sessions/${sessionId}/ws`);
  const entry = { ws, backoff: initialBackoff, lastSeq: 0 };
  connections.set(sessionId, entry);

  ws.onmessage = (e) => {
    if (connections.get(sessionId)?.ws !== ws) return;
    const evt = JSON.parse(e.data);
    if (evt.type === 'init') {
      entry.backoff = 1000; // reset on successful handshake
      entry.lastSeq = evt.data?.last_seq || evt.seq || 0;
    }
    if (evt.type !== 'init' && evt.seq > 0) {
      if (evt.seq <= entry.lastSeq) return;
      entry.lastSeq = evt.seq;
    }
    routeEvent(sessionId, evt);
  };

  ws.onclose = () => {
    if (connections.get(sessionId)?.ws !== ws) return; // superseded
    connections.delete(sessionId);
    if (!wantedIds.has(sessionId)) return; // intentionally removed
    // Reconnect with exponential backoff (read from entry — may have been reset by init)
    const delay = entry.backoff;
    const nextBackoff = Math.min(delay * 2, MAX_BACKOFF);
    const timer = setTimeout(() => {
      pendingTimers.delete(sessionId);
      if (wantedIds.has(sessionId) && !connections.has(sessionId)) {
        openWs(sessionId, nextBackoff);
      }
    }, delay);
    pendingTimers.set(sessionId, timer);
  };

  ws.onerror = () => {
    ws.close(); // triggers onclose → reconnect
  };
}

function routeEvent(sessionId, evt) {
  switch (evt.type) {
    case 'init':
      handleWsInit(sessionId, evt.data);
      break;
    case 'text_delta':
      handleWsTextDelta(sessionId, evt.data.delta);
      break;
    case 'thinking_delta':
      handleWsThinkingDelta(sessionId, evt.data.delta);
      break;
    case 'message_start':
      handleWsMessageStart(sessionId);
      break;
    case 'message_end':
      handleWsMessageEnd(sessionId, evt.data.text, evt.data.msg_id, evt.data.input_tokens, evt.data.output_tokens);
      break;
    case 'tool_call_start':
      handleWsToolCallStart(sessionId, evt.data);
      break;
    case 'tool_call_delta':
      handleWsToolCallDelta(sessionId, evt.data);
      break;
    case 'tool_start':
      handleWsToolStart(sessionId, evt.data);
      break;
    case 'tool_update':
      handleWsToolUpdate(sessionId, evt.data);
      break;
    case 'tool_end':
      handleWsToolEnd(sessionId, evt.data);
      break;
    case 'state_change':
      handleWsStateChange(sessionId, evt.data);
      break;
    case 'permission_request':
      handleWsPermissionRequest(sessionId, evt.data);
      break;
    case 'ask_user':
      handleWsAskUser(sessionId, evt.data);
      break;
    case 'permission_resolved':
      handleWsPermissionResolved(sessionId, evt.data);
      break;
    case 'ask_resolved':
      handleWsAskResolved(sessionId, evt.data);
      break;
    case 'config_change':
      handleWsConfigChange(sessionId, evt.data);
      break;
    case 'subagent_count':
      handleWsSubagentCount(sessionId, evt.data.count);
      break;
    case 'subagent_complete':
      handleWsSubagentComplete(sessionId, evt.data);
      break;
    case 'subagent_start':
      handleWsSubagentStart(sessionId, evt.data);
      break;
    case 'subagent_event':
      handleWsSubagentEvent(sessionId, evt.data);
      break;
    case 'subagent_end':
      handleWsSubagentEnd(sessionId, evt.data);
      break;
    case 'subagent_usage':
      handleWsSubagentUsage(
        sessionId,
        evt.data.job_id,
        evt.data.input_tokens,
        evt.data.output_tokens,
        evt.data.cost_usd,
      );
      break;
    case 'bash_job_start':
      handleWsBashJobStart(sessionId, evt.data);
      break;
    case 'bash_job_output':
      handleWsBashJobOutput(sessionId, evt.data);
      break;
    case 'bash_job_end':
      handleWsBashJobEnd(sessionId, evt.data);
      break;
    case 'bash_complete':
      handleWsBashComplete(sessionId, evt.data);
      break;
    case 'run_end':
      handleWsRunEnd(sessionId);
      break;
    case 'command':
      handleWsCommand(sessionId, evt.data);
      break;
    case 'tasks_update':
      handleWsTasksUpdate(sessionId, evt.data);
      break;
    case 'plan_mode':
      handleWsPlanMode(sessionId, evt.data);
      break;
    case 'goal_change':
      handleWsGoalChange(sessionId, evt.data);
      break;
    case 'goal_iteration':
      handleWsGoalIteration(sessionId, evt.data);
      break;
    case 'goal_verify':
      handleWsGoalVerify(sessionId, evt.data);
      break;
    case 'goal_end':
      handleWsGoalEnd(sessionId, evt.data);
      break;
    case 'steer':
      handleWsSteer(sessionId, evt.data);
      break;
    case 'steers_canceled':
      handleWsSteersCanceled(sessionId);
      break;
    case 'command_queued':
      handleWsCommandQueued(sessionId, evt.data);
      break;
    case 'command_dequeued':
      handleWsCommandDequeued(sessionId, evt.data);
      break;
    case 'context_update':
      handleWsContextUpdate(sessionId, evt.data);
      break;
    case 'session_cost':
      handleWsSessionCost(sessionId, evt.data);
      break;
    case 'ratelimit':
      handleWsRateLimit(sessionId, evt.data);
      break;
    case 'auto_verify_start':
      handleWsAutoVerifyStart(sessionId);
      break;
    case 'auto_verify_end':
      handleWsAutoVerifyEnd(sessionId, evt.data);
      break;
    case 'compaction_start':
      handleWsCompactionStart(sessionId);
      break;
    case 'compaction_end':
      handleWsCompactionEnd(sessionId);
      break;
  }
}
