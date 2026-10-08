// api.js — fetch helpers + centralized WS manager

import {
  handleWsInit, handleWsTextDelta, handleWsThinkingDelta,
  handleWsMessageStart,
  handleWsMessageEnd, handleWsToolStart, handleWsToolUpdate, handleWsToolEnd,
  handleWsToolCallStart, handleWsToolCallDelta,
  handleWsStateChange, handleWsPermissionRequest,
  handleWsPermissionResolved, handleWsAskResolved,
  handleWsConfigChange,
	 handleWsProviderExecution,
  handleWsSubagentCount, handleWsSubagentComplete, handleWsRunEnd,
  handleWsSubagentStart, handleWsSubagentEvent, handleWsSubagentEnd, handleWsSubagentUsage, handleWsSubagentTitle,
  handleWsBashJobStart, handleWsBashJobOutput, handleWsBashJobEnd, handleWsBashComplete,
  handleWsCommand, handleWsTasksUpdate,
  handleWsGoalChange, handleWsGoalIteration, handleWsGoalVerify, handleWsGoalEnd,
  handleWsAskUser, handleWsContextUpdate, handleWsSteer, handleWsSteersCanceled,
  handleWsUserMessage,
  handleWsMcpChange,
  handleWsCommandQueued, handleWsCommandDequeued,
  handleWsSessionCost, handleWsCacheUsage,
  handleWsRunTokens,
  handleWsAutoVerifyStart, handleWsAutoVerifyEnd, handleWsRateLimit,
  handleWsCompactionStart, handleWsCompactionEnd, handleWsBackgroundCompactionState, handleWsContextTrim,
  attentionNamespaceFromInit, attentionNamespaceTransition, adoptAttentionNamespace,
} from './ws-handlers.js';
import { store, updateSession } from './store.js';
import { watchPresence } from './presence.js';
import {
  beginHistoryHydration, canAppendHistoryDelta, confirmHistoryHydrationInit,
  finishHistoryHydration, lastDurableHistoryAnchor,
} from './history-hydration.js';
import { recoverNativeAuthorization } from './native-auth.js';

export const REQUEST_HEADERS = Object.freeze({ 'Content-Type': 'application/json', 'X-Moa-Request': '1' });
export const DEFAULT_API_TIMEOUT_MS = 15000;
// An MCP restart tears the old process tree down and then dials the new one,
// which the backend allows up to serverStartTimeout (15s) for the dial alone,
// on top of graceful teardown. The default 15s client deadline would abort a
// slow-but-valid restart and mislabel it as failed, so restart uses a longer,
// coherent timeout.
export const MCP_RESTART_TIMEOUT_MS = 30000;
// MCP OAuth start/finish talk to the remote authorization server (up to 45s on
// the backend) and finish then waits up to 20s more for the reconnect.
export const MCP_OAUTH_TIMEOUT_MS = 75000;
// A live socket normally sends init immediately, but on a weak mobile link a
// large one takes far longer than any fixed deadline that still catches a dead
// socket quickly: a 12 s deadline from socket creation cut every attempt short
// and restarted the download from zero, forever. The server therefore sends a
// large init as parts (init_chunks=1), and this deadline restarts on every sign
// of progress (open, announcement, part). It only expires when nothing at all
// arrives for this long — a proxy or half-open transport that swallowed the
// init — and then keeps the cached transcript legible but marked stale. Only an
// authoritative init may acknowledge its attention.
export const INIT_IDLE_TIMEOUT_MS = 20000;

export async function api(method, path, body, { timeoutMs = DEFAULT_API_TIMEOUT_MS, cache, headers } = {}) {
	const controller = timeoutMs > 0 ? new AbortController() : null;
	const opts = { method, headers: { ...REQUEST_HEADERS, ...headers } };
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
    if (!r.ok) {
      if (r.status === 401) void recoverNativeAuthorization();
      // Carry the HTTP status on the error: a caller can then tell a REJECTION
      // the server actually answered (409 busy, 503 queue full) from a request
      // that never got an answer (aborted fetch, network failure), which proves
      // nothing about the operation's outcome.
      const body = await r.text();
      const error = new Error(`${r.status}: ${body}`);
      error.status = r.status;
      attachStructuredError(error, body);
      throw error;
    }
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

// attachStructuredError — additive: a JSON error body of the shape
// {"error": "<copy>", "error_detail": {provider, class, action}} also lands on
// the error as `userMessage` and `detail`. The message keeps its old
// "<status>: <body>" form, so plain-text callers read exactly what they did.
export function attachStructuredError(error, body) {
  if (typeof body !== 'string' || body.trimStart()[0] !== '{') return error;
  let parsed;
  try { parsed = JSON.parse(body); } catch { return error; }
  if (!parsed || typeof parsed !== 'object') return error;
  if (typeof parsed.error === 'string' && parsed.error) error.userMessage = parsed.error;
  const detail = parsed.error_detail;
  if (detail && typeof detail === 'object' && typeof detail.class === 'string') {
    error.detail = {
      provider: typeof detail.provider === 'string' ? detail.provider : '',
      class: detail.class,
      action: typeof detail.action === 'string' ? detail.action : '',
    };
  }
  return error;
}

export function getVersion() {
  return api('GET', '/api/version', null, { cache: 'no-store' });
}

// --- Centralized WS Manager ---

const connections = new Map();    // sessionId → socket entry (see openWs)
const pendingTimers = new Map();  // sessionId → timeoutId (for reconnects awaiting retry)
const hydrationTimers = new Map(); // sessionId → timeoutId (waiting for WS init)
const wantedIds = new Set();      // sessions that should have a connection
const forceFullInit = new Set();  // session IDs whose cached delta base was absent
watchPresence({
  connections: () => Array.from(connections, ([id, entry]) => [id, entry.ws]),
  getState: store.get,
  subscribe: store.subscribe,
});
const attentionAcknowledgements = new Map(); // occurrence → confirmed POST
// Delay before the next automatic retry. It survives replacements that open a
// socket at once (foreground, network return, Retry now), so a link that keeps
// failing still backs off; only an accepted init resets it.
const retryBackoff = new Map(); // sessionId → ms
const INITIAL_BACKOFF = 1000;
const MAX_BACKOFF = 16000;
// Work that should not compete with a pending init for a weak link's
// bandwidth (roster, inbox, catalog…): run once no visible socket is waiting
// for its init. Any failed or timed-out attempt releases it at once, even with
// other sockets still pending, so staggered failing retries cannot hold it
// back indefinitely.
const afterInits = new Map(); // key → callback

function cursorAcknowledgementKey(sessionId, seq, namespace) {
  return `cursor:${sessionId}:${seq}:${namespace}`;
}

export function syncConnections(visibleIds) {
  wantedIds.clear();
  for (const id of visibleIds) wantedIds.add(id);

  // Close connections and cancel pending reconnects for sessions no longer visible
  for (const [id, entry] of connections) {
    if (!wantedIds.has(id)) settleAndClose(id, entry);
  }
  for (const [id, timer] of pendingTimers) {
    if (!wantedIds.has(id)) {
      clearTimeout(timer);
      pendingTimers.delete(id);
    }
  }
  // A conversation opened again later starts with a quick first retry.
  for (const id of retryBackoff.keys()) {
    if (!wantedIds.has(id)) retryBackoff.delete(id);
  }
  // Open connections for newly visible sessions (that aren't already connecting/pending)
  for (const id of visibleIds) {
    if (!connections.has(id) && !pendingTimers.has(id)) {
      openWs(id);
    }
  }
  flushAfterInits();
}

function initsPending() {
  for (const entry of connections.values()) {
    if (!entry.initDone) return true;
  }
  return false;
}

// afterPendingInits runs fn now, or once every visible socket has its init
// (or one of them has given up on it). Registering the same key again replaces
// the previous callback, so a repeated poll tick runs once. Callers re-check
// their own preconditions (visibility, polling) when fn finally runs.
export function afterPendingInits(key, fn) {
  if (!initsPending()) {
    afterInits.delete(key);
    fn();
    return;
  }
  afterInits.set(key, fn);
}

function flushAfterInits({ failed = false } = {}) {
  if (afterInits.size === 0 || (!failed && initsPending())) return;
  const callbacks = [...afterInits.values()];
  afterInits.clear();
  for (const fn of callbacks) fn();
}

// reconnectAll tears down every live socket and reopens the wanted ones
// immediately, keeping the retry backoff earned by earlier failures. Call it
// when the app returns to the foreground or regains network: a socket may be
// silently half-open (no close event ever fired), so the normal
// onclose→backoff path would never trigger and the session would sit frozen
// until a manual reload.
export function reconnectAll() {
  const ids = [...wantedIds];
  // Remove ownership before close so an asynchronous onclose cannot schedule
  // a competing retry. Explicitly settle first: a superseded socket's close
  // handler deliberately bails out, but its hydration/timer must not leak
  // into the replacement socket's fresh grace window.
  for (const [id, entry] of connections) settleAndClose(id, entry);
  for (const [, timer] of pendingTimers) clearTimeout(timer);
  pendingTimers.clear();
  for (const id of ids) openWs(id);
}

export function acknowledgeVisibleAttentionThrough(sessionId, throughSeq, namespace = '') {
  const session = store.get().sessions[sessionId];
  const hidden = typeof document !== 'undefined' && document.hidden;
  if (!session || hidden || !namespace) return Promise.resolve(false);
  if (session.attentionNamespace && session.attentionNamespace !== namespace) return Promise.resolve(false);
  if ((session.ackedThroughSeq || 0) >= throughSeq) {
    commitCursorAcknowledgement(sessionId, throughSeq, namespace);
    return Promise.resolve(true);
  }
  const key = cursorAcknowledgementKey(sessionId, throughSeq, namespace);
  const inFlight = attentionAcknowledgements.get(key);
  if (inFlight) return inFlight;
  const acknowledgement = api(
    'POST',
    `/api/sessions/${sessionId}/read?through_seq=${throughSeq}&attention_namespace=${encodeURIComponent(namespace)}`,
  )
    .then(() => {
      commitCursorAcknowledgement(sessionId, throughSeq, namespace);
      return true;
    })
    .finally(() => attentionAcknowledgements.delete(key));
  attentionAcknowledgements.set(key, acknowledgement);
  return acknowledgement;
}

function commitCursorAcknowledgement(sessionId, throughSeq, namespace) {
  const session = store.get().sessions[sessionId];
  if (!session || session.attentionNamespace !== namespace) return;
  const ackedThroughSeq = Math.max(session.ackedThroughSeq || 0, throughSeq);
  const unseen = (session.unseenSeq || 0) > ackedThroughSeq ? session.unseen : false;
  if (session.ackedThroughSeq === ackedThroughSeq && session.unseen === unseen) return;
  updateSession(sessionId, {
    ackedThroughSeq,
    unseen,
  });
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

function scheduleReconnect(sessionId) {
  if (!wantedIds.has(sessionId) || pendingTimers.has(sessionId)) return;
  const delay = retryBackoff.get(sessionId) ?? INITIAL_BACKOFF;
  retryBackoff.set(sessionId, Math.min(delay * 2, MAX_BACKOFF));
  const timer = setTimeout(() => {
    pendingTimers.delete(sessionId);
    if (wantedIds.has(sessionId) && !connections.has(sessionId)) {
      openWs(sessionId);
    }
  }, delay);
  pendingTimers.set(sessionId, timer);
}

// Retry an explicitly stale transcript immediately instead of waiting for the
// normal reconnect backoff. Closing the old socket makes any late event from it
// harmless: its handlers no longer own the entry in connections.
export function retryHistoryHydration(sessionId, { fullInit = false } = {}) {
  if (!wantedIds.has(sessionId)) return false;
  if (fullInit) forceFullInit.add(sessionId);
  const timer = pendingTimers.get(sessionId);
  if (timer !== undefined) {
    clearTimeout(timer);
    pendingTimers.delete(sessionId);
  }
  const entry = connections.get(sessionId);
  // This close is intentionally superseded, so it cannot settle hydration
  // through onclose. Clear its boundary before opening the replacement.
  if (entry) settleAndClose(sessionId, entry);
  openWs(sessionId);
  return true;
}

// settleAndClose removes a socket's ownership, settles its hydration boundary
// and timer, and closes it. Once ownership is removed the socket's own
// onclose/onmessage handlers bail out, so nothing from it can leak into a
// replacement connection.
function settleAndClose(sessionId, entry) {
  connections.delete(sessionId);
  clearHistoryHydrationTimer(sessionId);
  finishHistoryHydration(sessionId);
  try { entry.ws.close(); } catch (_) { /* a replacement or absence is fine */ }
}

// (Re)start the init deadline: called when the socket is created and on every
// sign that its init is still arriving. On expiry the socket is abandoned
// right away rather than on its onclose, which a browser may only fire after a
// close handshake stuck behind the same slow link.
function noteInitProgress(sessionId, entry) {
  clearHistoryHydrationTimer(sessionId);
  hydrationTimers.set(sessionId, setTimeout(() => {
    if (connections.get(sessionId) !== entry) return;
    connections.delete(sessionId);
    failHistoryHydration(sessionId);
    try { entry.ws.close(); } catch (_) { /* already closing */ }
    scheduleReconnect(sessionId);
    flushAfterInits({ failed: true });
  }, INIT_IDLE_TIMEOUT_MS));
}

// readInitTransport turns what arrives before init into the init event. A
// large init comes as an init_begin announcement plus binary parts that
// concatenate to its JSON; each part is progress. Returns the event to route,
// or null while the init is still incomplete or the socket was closed for a
// malformed transfer.
function readInitTransport(sessionId, entry, data) {
  const reject = () => { entry.ws.close(); return null; };
  const parts = entry.initParts;
  if (typeof data !== 'string') {
    const bytes = new Uint8Array(data);
    if (!parts || bytes.byteLength === 0 || parts.received + bytes.byteLength > parts.bytes) return reject();
    parts.chunks.push(bytes);
    parts.received += bytes.byteLength;
    if (parts.chunks.length < parts.expected) {
      noteInitProgress(sessionId, entry);
      return null;
    }
    entry.initParts = null;
    if (parts.received !== parts.bytes) return reject();
    const joined = new Uint8Array(parts.bytes);
    let offset = 0;
    for (const chunk of parts.chunks) {
      joined.set(chunk, offset);
      offset += chunk.byteLength;
    }
    const evt = JSON.parse(new TextDecoder().decode(joined));
    return evt?.type === 'init' ? evt : reject();
  }
  const evt = JSON.parse(data);
  if (evt.type === 'init_begin') {
    const expected = evt.data?.parts;
    const total = evt.data?.bytes;
    if (parts || !Number.isInteger(expected) || expected < 1 || !Number.isInteger(total) || total < expected) return reject();
    entry.initParts = { expected, bytes: total, received: 0, chunks: [] };
    noteInitProgress(sessionId, entry);
    return null;
  }
  // Nothing may interleave with the parts of an init.
  return parts ? reject() : evt;
}

function openWs(sessionId) {
  pendingTimers.delete(sessionId);
  const cached = store.get().sessions[sessionId]?.messages || [];
  const cachedBase = lastDurableHistoryAnchor(cached)?.id;
  // delete() reports whether a full init was forced, consuming the flag.
  const skipDelta = forceFullInit.delete(sessionId);
  const useDeltaResume = !skipDelta && !!cachedBase;
  beginHistoryHydration(sessionId, { deltaResume: useDeltaResume });
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  let ws;
  try {
    const params = new URLSearchParams();
    if (useDeltaResume) params.set('since_msg', cachedBase);
    params.set('init_chunks', '1');
    const query = params.size > 0 ? `?${params}` : '';
    ws = new WebSocket(`${proto}//${location.host}/api/sessions/${sessionId}/ws${query}`);
  } catch (_) {
    // Preserve the forced-full decision for the retry: the flag was consumed
    // above but this attempt never reached the server.
    if (skipDelta) forceFullInit.add(sessionId);
    failHistoryHydration(sessionId);
    scheduleReconnect(sessionId);
    flushAfterInits({ failed: true });
    return;
  }
  ws.binaryType = 'arraybuffer';
  const entry = {
    ws,
    lastSeq: 0,
    attentionNamespace: '',
    initDone: false,
    initParts: null,
  };
  connections.set(sessionId, entry);
  noteInitProgress(sessionId, entry);

  ws.onopen = () => {
    if (connections.get(sessionId) === entry && !entry.initDone) noteInitProgress(sessionId, entry);
  };

  ws.onmessage = (e) => {
    if (connections.get(sessionId)?.ws !== ws) return;
    let evt;
    if (entry.initDone) {
      if (typeof e.data !== 'string') {
        ws.close();
        return;
      }
      evt = JSON.parse(e.data);
    } else {
      evt = readInitTransport(sessionId, entry, e.data);
      if (!evt) return;
    }
    if (evt.type === 'init') {
      const namespace = attentionNamespaceFromInit(evt.data);
      if (!namespace) {
        ws.close();
        return;
      }
      const namespaceTransition = attentionNamespaceTransition(
        store.get().sessions[sessionId], namespace, { allowCrossProcess: false },
      );
      if (!namespaceTransition.accepted) {
        ws.close();
        return;
      }
      // Only roster snapshots can choose between unordered server processes.
      // A live socket may still advance an ordered incarnation in its process.
      adoptAttentionNamespace(sessionId, namespace);
      // A server only emits delta_base after validating its tree path, but a
      // client may have evicted or locally rewritten that prefix. Never append
      // a suffix to a different transcript: retry once without a resume token.
      if (evt.data?.delta_base && !canAppendHistoryDelta(store.get().sessions[sessionId]?.messages || [], evt.data.delta_base)) {
        forceFullInit.add(sessionId);
        ws.close();
        return;
      }
      entry.lastSeq = evt.data?.last_seq ?? evt.seq ?? 0;
      entry.attentionNamespace = namespace;
      clearHistoryHydrationTimer(sessionId);
      confirmHistoryHydrationInit(sessionId, { deltaBase: !!evt.data?.delta_base });
      handleWsInit(sessionId, evt.data);
      entry.initDone = true;
      retryBackoff.delete(sessionId);
      flushAfterInits();
      return;
    }
    // A socket's init stamps every later frame with the runtime incarnation
    // that emitted it. Never let an old socket's bus sequence be interpreted
    // against a cursor namespace adopted from a newer roster snapshot.
    if (store.get().sessions[sessionId]?.attentionNamespace !== entry.attentionNamespace) {
      const namespaceTransition = attentionNamespaceTransition(
        store.get().sessions[sessionId], entry.attentionNamespace, { allowCrossProcess: false },
      );
      if (!namespaceTransition.accepted) {
        ws.close();
        return;
      }
      adoptAttentionNamespace(sessionId, entry.attentionNamespace);
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
    failHistoryHydration(sessionId);
    scheduleReconnect(sessionId);
    flushAfterInits({ failed: true });
  };

  ws.onerror = () => {
    ws.close(); // triggers onclose → reconnect
  };
}

function routeEvent(sessionId, evt) {
  switch (evt.type) {
	case 'provider_execution':
	  handleWsProviderExecution(sessionId, evt.data);
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
      handleWsMessageEnd(sessionId, evt.data.text, evt.data.msg_id, evt.data.timestamp, evt.data.provider_source);
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
      handleWsStateChange(sessionId, evt.data, evt.seq);
      break;
    case 'permission_request':
      handleWsPermissionRequest(sessionId, evt.data, evt.seq);
      break;
    case 'ask_user':
      handleWsAskUser(sessionId, evt.data, evt.seq);
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
    case 'subagent_title':
      handleWsSubagentTitle(sessionId, evt.data);
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
      handleWsRunEnd(sessionId, evt.data, evt.seq);
      break;
    case 'command':
      handleWsCommand(sessionId, evt.data);
      break;
    case 'tasks_update':
      handleWsTasksUpdate(sessionId, evt.data);
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
      handleWsSteersCanceled(sessionId, evt.data?.discarded_steer_ids, evt.data?.stop_id || evt.data?.recall_id);
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
    case 'cache_usage':
      handleWsCacheUsage(sessionId, evt.data);
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
      handleWsCompactionEnd(sessionId, evt.data);
      break;
    case 'background_compaction_state':
      handleWsBackgroundCompactionState(sessionId, evt.data);
      break;
    case 'context_trim':
      handleWsContextTrim(sessionId, evt.data);
      break;
  }
}
