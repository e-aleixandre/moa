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
  handleWsUserMessage,
  handleWsMcpChange,
  handleWsCommandQueued, handleWsCommandDequeued,
  handleWsSessionCost,
  handleWsRunTokens,
  handleWsAutoVerifyStart, handleWsAutoVerifyEnd, handleWsRateLimit,
  handleWsCompactionStart, handleWsCompactionEnd,
} from './ws-handlers.js';
import { store, updateSession, visibleSessionIds } from './store.js';
import { beginHistoryHydration, finishHistoryHydration } from './history-hydration.js';

export const REQUEST_HEADERS = Object.freeze({ 'Content-Type': 'application/json', 'X-Moa-Request': '1' });
export const DEFAULT_API_TIMEOUT_MS = 15000;
// An MCP restart tears the old process tree down and then dials the new one,
// which the backend allows up to serverStartTimeout (15s) for the dial alone,
// on top of graceful teardown. The default 15s client deadline would abort a
// slow-but-valid restart and mislabel it as failed, so restart uses a longer,
// coherent timeout.
export const MCP_RESTART_TIMEOUT_MS = 30000;
// A live socket normally sends init immediately. If a proxy or half-open
// transport swallows it, keep the cached transcript legible but explicitly
// marked stale. Only an authoritative init may acknowledge its attention.
export const HISTORY_HYDRATION_TIMEOUT_MS = 12000;

export async function api(method, path, body, { timeoutMs = DEFAULT_API_TIMEOUT_MS, cache } = {}) {
  const controller = timeoutMs > 0 ? new AbortController() : null;
  const opts = { method, headers: REQUEST_HEADERS };
  if (body) opts.body = JSON.stringify(body);
  if (cache) opts.cache = cache;
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
  return api('GET', '/api/version', null, { cache: 'no-store' });
}

// --- Centralized WS Manager ---

const connections = new Map();    // sessionId → { ws, backoff, timer }
const pendingTimers = new Map();  // sessionId → timeoutId (for reconnects awaiting retry)
const hydrationTimers = new Map(); // sessionId → timeoutId (waiting for WS init)
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
      clearHistoryHydrationTimer(id);
      finishHistoryHydration(id);
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

// acknowledgeVisibleAttention is intentionally the single path that clears a
// roster dot and POSTs /read for an already-known occurrence. Its generation
// is the one rendered by this init snapshot, not the session's current roster
// value: MarkSessionRead accepts gen >= current, so borrowing a newer value
// here could acknowledge content the snapshot never showed. Live events have
// their own immediate acknowledgement path because their content is rendered
// incrementally; cached history must wait for init.
export function acknowledgeVisibleAttention(sessionId, shownGeneration, shownInstance = '', { renderedLive = false } = {}) {
  const state = store.get();
  const session = state.sessions[sessionId];
  const acknowledgementInstance = shownInstance || session?.serverInstance || '';
  const hidden = typeof document !== 'undefined' && document.hidden;
  if (!session || hidden || !visibleSessionIds(state).includes(sessionId)) return false;
  if (!renderedLive && (!session.unseen || !session.historyHydrated)) return false;
  if (!shownGeneration || (!renderedLive && (session.unseenGen || 0) > shownGeneration)) return false;
  if (shownInstance && session.serverInstance && shownInstance !== session.serverInstance) return false;
  // A roster response captured before this POST can restore unseen:true for
  // the same occurrence. Keep the acknowledgement with the session rather
  // than trusting that optimistic clear, while still allowing a newer
  // generation from this server instance through.
  if (session.lastAckedUnseenInstance === acknowledgementInstance &&
      (session.lastAckedUnseenGen || 0) >= shownGeneration) return false;
  updateSession(sessionId, {
    unseen: false,
    unseenGen: shownGeneration,
    lastAckedUnseenGen: shownGeneration,
    lastAckedUnseenInstance: acknowledgementInstance,
  });
  const instanceQuery = acknowledgementInstance
    ? `&server_instance=${encodeURIComponent(acknowledgementInstance)}`
    : '';
  api('POST', `/api/sessions/${sessionId}/read?unseen_gen=${shownGeneration}${instanceQuery}`).catch(() => {});
  return true;
}

function clearHistoryHydrationTimer(sessionId) {
  const timer = hydrationTimers.get(sessionId);
  if (timer !== undefined) clearTimeout(timer);
  hydrationTimers.delete(sessionId);
}

function failHistoryHydration(sessionId) {
  clearHistoryHydrationTimer(sessionId);
  finishHistoryHydration(sessionId, { stale: true });
}

function scheduleReconnect(sessionId, entry) {
  if (!wantedIds.has(sessionId) || pendingTimers.has(sessionId)) return;
  const delay = entry.backoff;
  const nextBackoff = Math.min(delay * 2, MAX_BACKOFF);
  const timer = setTimeout(() => {
    pendingTimers.delete(sessionId);
    if (wantedIds.has(sessionId) && !connections.has(sessionId)) {
      openWs(sessionId, nextBackoff);
    }
  }, delay);
  pendingTimers.set(sessionId, timer);
}

// Retry an explicitly stale transcript immediately instead of waiting for the
// normal reconnect backoff. Closing the old socket makes any late event from it
// harmless: its handlers no longer own the entry in connections.
export function retryHistoryHydration(sessionId) {
  if (!wantedIds.has(sessionId)) return false;
  const timer = pendingTimers.get(sessionId);
  if (timer !== undefined) {
    clearTimeout(timer);
    pendingTimers.delete(sessionId);
  }
  const entry = connections.get(sessionId);
  if (entry) {
    connections.delete(sessionId);
    clearHistoryHydrationTimer(sessionId);
    try { entry.ws.close(); } catch (_) { /* a fresh connection below is enough */ }
  }
  openWs(sessionId, 1000);
  return true;
}

function openWs(sessionId, initialBackoff) {
  pendingTimers.delete(sessionId);
  beginHistoryHydration(sessionId);
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  let ws;
  try {
    ws = new WebSocket(`${proto}//${location.host}/api/sessions/${sessionId}/ws`);
  } catch (_) {
    failHistoryHydration(sessionId);
    scheduleReconnect(sessionId, { backoff: initialBackoff });
    return;
  }
  const entry = {
    ws,
    backoff: initialBackoff,
    lastSeq: 0,
    // A known roster identity is the only proof an init belongs to this socket
    // after a process transition. An empty value is deliberately not a match.
    serverInstanceAtOpen: store.get().sessions[sessionId]?.serverInstance || '',
  };
  connections.set(sessionId, entry);
  clearHistoryHydrationTimer(sessionId);
  hydrationTimers.set(sessionId, setTimeout(() => {
    if (connections.get(sessionId)?.ws === ws) {
      failHistoryHydration(sessionId);
      ws.close(); // onclose schedules the normal backoff retry
    }
  }, HISTORY_HYDRATION_TIMEOUT_MS));

  ws.onmessage = (e) => {
    if (connections.get(sessionId)?.ws !== ws) return;
    const evt = JSON.parse(e.data);
    if (evt.type === 'init') {
      const currentInstance = store.get().sessions[sessionId]?.serverInstance || '';
      const initInstance = evt.data?.server_instance || '';
      const initMatchesCurrent = !!initInstance && !!currentInstance && initInstance === currentInstance;
      const initMatchesOpen = !!entry.serverInstanceAtOpen && initInstance === entry.serverInstanceAtOpen;
      const trustedForAck = initMatchesCurrent || (!currentInstance && initMatchesOpen);
      if (currentInstance && !initMatchesCurrent) {
        // The roster has already moved us to another server process. Do not
        // render or acknowledge this old socket's snapshot; reconnect for an
        // init from the process the roster named.
        connections.delete(sessionId);
        failHistoryHydration(sessionId);
        try { ws.close(); } catch (_) { /* reconnect below is sufficient */ }
        scheduleReconnect(sessionId, entry);
        return;
      }
      entry.lastSeq = evt.data?.last_seq ?? evt.seq ?? 0;
      clearHistoryHydrationTimer(sessionId);
      routeEvent(sessionId, evt, { trustedForAck });
      // A missing bound is intentionally not acknowledged: it means the server
      // could not prove which attention occurrence this snapshot contains.
      // Keep the rendered snapshot, close this socket, and retry through the
      // normal bounded backoff until a fenced init can clear the roster dot.
      if (evt.data?.attention_bound === false) {
        entry.attentionBoundRecovery = true;
        ws.close();
      } else {
        // Only a safely bounded snapshot is a successful recovery. Resetting
        // here on an unbounded init would reconnect every visible client once
        // a second while the server is contended.
        entry.backoff = 1000;
      }
      return;
    }
    // Bus sequences intentionally retain ordinary numeric ordering here. A
    // uint64 wrap cannot occur in a plausible server-process lifetime, and
    // JSON Numbers lose integer precision far before it, so a modular client
    // comparison would not be correct without a protocol-wide BigInt/string
    // migration. Zero is the ambiguous wrap value: fail closed and reconnect
    // rather than rendering it as an unsequenced live event.
    if (evt.type !== 'init' && evt.seq === 0) {
      ws.close();
      return;
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
    if (entry.attentionBoundRecovery) {
      entry.attentionBoundRecovery = false;
      // This was a valid transcript with only its acknowledgement boundary
      // unavailable, not a failed hydration. Do not mark it stale while the
      // retry obtains a safe bound.
      clearHistoryHydrationTimer(sessionId);
      finishHistoryHydration(sessionId, { shown: true });
      scheduleReconnect(sessionId, entry);
      return;
    }
    failHistoryHydration(sessionId);
    // Reconnect with exponential backoff (read from entry — may have been reset by init).
    scheduleReconnect(sessionId, entry);
  };

  ws.onerror = () => {
    ws.close(); // triggers onclose → reconnect
  };
}

function routeEvent(sessionId, evt, { trustedForAck = true } = {}) {
  switch (evt.type) {
    case 'init':
      handleWsInit(sessionId, evt.data, { trustedForAck });
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
      handleWsMessageEnd(sessionId, evt.data.text, evt.data.msg_id);
      break;
    case 'run_tokens':
      handleWsRunTokens(sessionId, evt.data);
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
      handleWsSubagentUsage(sessionId, evt.data);
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
      handleWsRunEnd(sessionId, evt.data);
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
    case 'user_message':
      handleWsUserMessage(sessionId, evt.data);
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
    case 'mcp_change':
      handleWsMcpChange(sessionId, evt.data);
      break;
    case 'session_cost':
      handleWsSessionCost(sessionId, evt.data);
      break;
    case 'ratelimit':
      handleWsRateLimit(sessionId, evt.data);
      break;
    case 'auto_verify_start':
      handleWsAutoVerifyStart(sessionId, evt.data);
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
