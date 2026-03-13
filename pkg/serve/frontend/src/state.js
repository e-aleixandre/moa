// state.js — immutable snapshot store with pub/sub

import { api, syncConnections } from './api.js';
import { triggerAttention, triggerDone } from './notifications.js';
import {
  createTile, initIds, allTileIds, allSessionIds, findTile, tileCount,
  splitTileNode, removeTileNode, setTileSession, swapSessions,
  clearSession, setRatioAtPath, presetTree,
} from './tileTree.js';

const STORAGE_KEY = 'moa-ui-state';

function loadPersistedState() {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (raw) return JSON.parse(raw);
  } catch (_) { /* ignore */ }
  return {};
}

function persistState(s) {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify({
      tileTree: s.tileTree,
      focusedTile: s.focusedTile,
      soundEnabled: s.soundEnabled,
    }));
  } catch (_) { /* ignore */ }
}

const persisted = loadPersistedState();

// Migrate from old format or restore tree
let initialTree;
if (persisted.tileTree) {
  initialTree = persisted.tileTree;
  initIds(initialTree);
} else {
  // Old format or fresh start
  initialTree = createTile();
}

// Validate focusedTile: must be a tile ID in the tree
const initialIds = allTileIds(initialTree);
const initialFocused = initialIds.includes(persisted.focusedTile)
  ? persisted.focusedTile
  : initialIds[0] || 1;

let state = {
  sessions: {},

  tileTree: initialTree,
  focusedTile: initialFocused,
  soundEnabled: persisted.soundEnabled || false,

  isMobile: false,

  activeSession: null,
};

let listeners = new Set();

export const store = {
  get() { return state; },
  subscribe(fn) {
    listeners.add(fn);
    return () => listeners.delete(fn);
  },
};

function setState(patch) {
  const next = typeof patch === 'function' ? patch(state) : patch;
  state = { ...state, ...next };
  persistState(state);
  listeners.forEach(fn => fn(state));
}

// --- Derived selectors ---

export function visibleSessionIds(s) {
  if (s.isMobile) {
    return s.activeSession ? [s.activeSession] : [];
  }
  return allSessionIds(s.tileTree);
}

export function isSessionInTile(s, sessionId) {
  return allSessionIds(s.tileTree).includes(sessionId);
}

export function sessionsByGroup(s) {
  const groups = {};
  for (const sess of Object.values(s.sessions)) {
    const key = sess.cwd || 'Unknown';
    if (!groups[key]) groups[key] = [];
    groups[key].push(sess);
  }
  for (const arr of Object.values(groups)) {
    arr.sort((a, b) => (b.updated || 0) - (a.updated || 0));
  }
  return groups;
}

export function attentionCount(s) {
  return Object.values(s.sessions).filter(
    sess => sess.state === 'permission' || sess.state === 'error'
  ).length;
}

export function getTileCount() {
  return tileCount(state.tileTree);
}

// --- Session data actions ---

let pollTimer = null;

export async function loadSessions() {
  try {
    const list = await api('GET', '/api/sessions');
    const prev = state.sessions;
    const sessions = {};
    for (const info of list) {
      const existing = prev[info.id];
      sessions[info.id] = {
        id: info.id,
        title: info.title,
        state: info.state,
        model: info.model,
        thinking: info.thinking || '',
        cwd: info.cwd,
        error: info.error || null,
        untrustedMcp: info.untrusted_mcp || false,
        messages: existing ? existing.messages : [],
        pendingPerm: existing ? existing.pendingPerm : null,
        streamingText: existing ? existing.streamingText : null,
        thinkingText: existing ? existing.thinkingText : null,
        subagentCount: existing ? existing.subagentCount : 0,
        tasks: existing ? existing.tasks : [],
        planMode: info.plan_mode || (existing ? existing.planMode : 'off'),
        planFile: info.plan_file || (existing ? existing.planFile : null),
      };
    }
    // Detect attention transitions (hidden sessions only)
    for (const [id, sess] of Object.entries(sessions)) {
      const prevSess = prev[id];
      if (prevSess && prevSess.state !== sess.state) {
        if (sess.state === 'permission' || sess.state === 'error') {
          const visible = visibleSessionIds(state);
          if (!visible.includes(id)) {
            triggerAttention(sess, null, state.soundEnabled);
          }
        }
      }
    }
    setState({ sessions });
    // Clean deleted sessions from tile tree
    const validIds = new Set(Object.keys(sessions));
    let tree = state.tileTree;
    let changed = false;
    for (const sid of allSessionIds(tree)) {
      if (!validIds.has(sid)) {
        tree = clearSession(tree, sid);
        changed = true;
      }
    }
    if (changed) setState({ tileTree: tree });
    afterVisibilityChange();
  } catch (e) {
    console.error('loadSessions failed:', e);
  }
}

export function startPolling() {
  stopPolling();
  pollTimer = setInterval(loadSessions, 3000);
}

export function stopPolling() {
  if (pollTimer) { clearInterval(pollTimer); pollTimer = null; }
}

// --- WS event handlers (called by api.js only) ---

function updateSession(id, patch) {
  const sess = state.sessions[id];
  if (!sess) return;
  setState({
    sessions: { ...state.sessions, [id]: { ...sess, ...patch } },
  });
}

export function handleWsInit(id, data) {
  delete pendingTextDeltas[id];
  delete pendingThinkingDeltas[id];
  delete pendingToolDeltas[id];
  updateSession(id, {
    messages: normalizeHistory(data.messages || []),
    state: data.state || 'idle',
    pendingPerm: data.pending_permission || null,
    streamingText: null,
    thinkingText: null,
    tasks: data.tasks || [],
    planMode: data.plan_mode || 'off',
    planFile: data.plan_file || null,
  });
}

function normalizeHistory(raw) {
  const result = [];
  const resultMap = {};
  for (const msg of raw) {
    if (msg.role === 'tool_result') {
      resultMap[msg.tool_call_id] = msg;
    }
  }
  for (const msg of raw) {
    if (msg.role === 'assistant') {
      const textParts = [];
      for (const c of (msg.content || [])) {
        if (c.type === 'text' && c.text) {
          textParts.push(c.text);
        } else if (c.type === 'tool_call') {
          if (textParts.length > 0) {
            result.push({ role: 'assistant', content: [{ type: 'text', text: textParts.join('') }] });
            textParts.length = 0;
          }
          const tr = resultMap[c.tool_call_id];
          let resultText = null;
          let status = 'running';
          if (tr) {
            resultText = (tr.content || []).filter(x => x.type === 'text').map(x => x.text).join('');
            if (tr.custom?.rejected === true) {
              status = 'rejected';
            } else if (tr.is_error) {
              status = 'error';
            } else {
              status = 'done';
            }
          }
          result.push({
            _type: 'tool_start',
            tool_call_id: c.tool_call_id,
            tool_name: c.tool_name,
            args: c.arguments || {},
            status,
            result: resultText,
          });
        }
      }
      if (textParts.length > 0) {
        result.push({ role: 'assistant', content: [{ type: 'text', text: textParts.join('') }] });
      }
    } else if (msg.role === 'user') {
      result.push(msg);
    }
  }
  return result;
}

// --- Streaming delta batching ---
// Text/thinking deltas arrive per-token (30-60+/s). Accumulate in buffers
// and flush once per animation frame to avoid redundant renders + markdown parses.
const pendingTextDeltas = {};    // sessionId → accumulated text delta
const pendingThinkingDeltas = {}; // sessionId → accumulated thinking delta
const pendingToolDeltas = {};    // sessionId → { toolCallId → accumulated delta }
let flushScheduled = false;

function scheduleFlush() {
  if (flushScheduled) return;
  flushScheduled = true;
  requestAnimationFrame(flushDeltas);
}

function flushDeltas() {
  flushScheduled = false;

  // Collect all sessions that need updating
  const sessionIds = new Set([
    ...Object.keys(pendingTextDeltas),
    ...Object.keys(pendingThinkingDeltas),
    ...Object.keys(pendingToolDeltas),
  ]);

  for (const id of sessionIds) {
    const sess = state.sessions[id];
    if (!sess) {
      delete pendingTextDeltas[id];
      delete pendingThinkingDeltas[id];
      delete pendingToolDeltas[id];
      continue;
    }
    const patch = {};

    if (pendingTextDeltas[id]) {
      patch.streamingText = (sess.streamingText || '') + pendingTextDeltas[id];
      delete pendingTextDeltas[id];
    }

    if (pendingThinkingDeltas[id]) {
      patch.thinkingText = (sess.thinkingText || '') + pendingThinkingDeltas[id];
      delete pendingThinkingDeltas[id];
    }

    if (pendingToolDeltas[id]) {
      let messages = patch.messages || sess.messages;
      let changed = false;
      for (const [toolCallId, delta] of Object.entries(pendingToolDeltas[id])) {
        messages = messages.map(m => {
          if (m._type === 'tool_start' && m.tool_call_id === toolCallId) {
            changed = true;
            return { ...m, streamingResult: (m.streamingResult || '') + delta };
          }
          return m;
        });
      }
      if (changed) patch.messages = messages;
      delete pendingToolDeltas[id];
    }

    if (Object.keys(patch).length > 0) {
      updateSession(id, patch);
    }
  }
}

export function handleWsTextDelta(id, delta) {
  if (!state.sessions[id]) return;
  pendingTextDeltas[id] = (pendingTextDeltas[id] || '') + delta;
  scheduleFlush();
}

export function handleWsThinkingDelta(id, delta) {
  if (!state.sessions[id]) return;
  pendingThinkingDeltas[id] = (pendingThinkingDeltas[id] || '') + delta;
  scheduleFlush();
}

export function handleWsMessageEnd(id, fullText) {
  // Discard any pending text/thinking deltas — fullText is authoritative.
  delete pendingTextDeltas[id];
  delete pendingThinkingDeltas[id];
  const sess = state.sessions[id];
  if (!sess) return;
  const msg = { role: 'assistant', content: [{ type: 'text', text: fullText }] };
  updateSession(id, {
    messages: [...sess.messages, msg],
    streamingText: null,
    thinkingText: null,
  });
}

export function handleWsToolStart(id, data) {
  const sess = state.sessions[id];
  if (!sess) return;

  const existingIdx = (sess.messages || []).findIndex(
    m => m._type === 'tool_start' && m.tool_call_id === data.tool_call_id,
  );

  if (existingIdx >= 0) {
    const messages = sess.messages.map((m, idx) => {
      if (idx !== existingIdx) return m;
      return {
        ...m,
        tool_name: data.tool_name,
        args: data.args,
        status: 'running',
      };
    });
    updateSession(id, { messages });
    return;
  }

  const toolMsg = {
    _type: 'tool_start',
    tool_call_id: data.tool_call_id,
    tool_name: data.tool_name,
    args: data.args,
    status: 'running',
    result: null,
  };
  updateSession(id, { messages: [...sess.messages, toolMsg] });
}

export function handleWsToolUpdate(id, data) {
  if (!state.sessions[id]) return;
  if (!pendingToolDeltas[id]) pendingToolDeltas[id] = {};
  pendingToolDeltas[id][data.tool_call_id] =
    (pendingToolDeltas[id][data.tool_call_id] || '') + data.delta;
  scheduleFlush();
}

export function handleWsToolEnd(id, data) {
  // Discard any pending tool deltas for this tool — final result is authoritative.
  if (pendingToolDeltas[id]) {
    delete pendingToolDeltas[id][data.tool_call_id];
    if (Object.keys(pendingToolDeltas[id]).length === 0) delete pendingToolDeltas[id];
  }
  const sess = state.sessions[id];
  if (!sess) return;

  const nextStatus = data.rejected === true
    ? 'rejected'
    : (data.is_error ? 'error' : 'done');

  const messages = sess.messages.map(m => {
    if (m._type === 'tool_start' && m.tool_call_id === data.tool_call_id) {
      return { ...m, status: nextStatus, result: data.result, streamingResult: null };
    }
    return m;
  });
  updateSession(id, { messages });
}

export function handleWsStateChange(id, data) {
  const prev = state.sessions[id];
  const wasRunning = prev && (prev.state === 'running' || prev.state === 'permission');
  updateSession(id, { state: data.state, error: data.error || null });
  if (data.state === 'idle' || data.state === 'error') {
    const sess = state.sessions[id];
    if (sess) updateSession(id, { streamingText: null, thinkingText: null });
    if (wasRunning) {
      flashSession(id, data.state === 'error' ? 'error' : 'done');
      // Notify non-visible sessions that finished
      const visible = visibleSessionIds(state);
      if (!visible.includes(id) && data.state === 'idle' && sess) {
        triggerDone(sess, state.soundEnabled);
      }
    }
  }
}

export function handleWsAskUser(id, data) {
  updateSession(id, {
    pendingAsk: { id: data.id, questions: data.questions },
  });
  flashSession(id, 'attention');
  const sess = state.sessions[id];
  if (sess) triggerAttention(sess, 'ask_user', state.soundEnabled);
}

export function handleWsPermissionRequest(id, data) {
  updateSession(id, {
    state: 'permission',
    pendingPerm: { id: data.id, tool_name: data.tool_name, args: data.args },
  });
  flashSession(id, 'attention');
  const sess = state.sessions[id];
  if (sess) triggerAttention(sess, data.tool_name, state.soundEnabled);
}

function flashSession(id, type) {
  updateSession(id, { flash: type });
  setTimeout(() => {
    if (state.sessions[id]?.flash === type) updateSession(id, { flash: null });
  }, 1300);
}

export function handleWsConfigChange(id, data) {
  updateSession(id, {
    model: data.model || state.sessions[id]?.model,
    thinking: data.thinking || state.sessions[id]?.thinking,
  });
}

export function handleWsSubagentCount(id, count) {
  updateSession(id, { subagentCount: count });
}

export function handleWsSubagentComplete(id, data) {
  const sess = state.sessions[id];
  if (!sess) return;
  updateSession(id, { messages: [...sess.messages, { _type: 'system', text: data.text }] });
}

export function handleWsRunEnd(id) {
  delete pendingTextDeltas[id];
  delete pendingThinkingDeltas[id];
  delete pendingToolDeltas[id];
  updateSession(id, { streamingText: null, thinkingText: null });
}

// --- API actions ---

export async function createSession(opts) {
  const sess = await api('POST', '/api/sessions', opts);
  await loadSessions();
  const id = sess.id;
  if (state.isMobile) {
    setActiveSession(id);
  } else {
    assignToTile(state.focusedTile, id);
  }
  return sess;
}

export async function deleteSession(id) {
  await api('DELETE', `/api/sessions/${id}`);
  const sessions = { ...state.sessions };
  delete sessions[id];
  const tileTree = clearSession(state.tileTree, id);
  const activeSession = state.activeSession === id ? null : state.activeSession;
  setState({ sessions, tileTree, activeSession });
  afterVisibilityChange();
}

export async function sendMessage(id, text) {
  const sess = state.sessions[id];
  if (sess) {
    const userMsg = { role: 'user', content: [{ type: 'text', text }] };
    updateSession(id, {
      messages: [...sess.messages, userMsg],
      streamingText: null,
      thinkingText: null,
    });
  }
  const res = await api('POST', `/api/sessions/${id}/send`, { text });
  return res?.action || 'send';
}

export async function cancelRun(id) {
  await api('POST', `/api/sessions/${id}/cancel`);
}

export async function resolvePermission(sessionId, permId, approved) {
  await api('POST', `/api/sessions/${sessionId}/permission`, {
    id: permId, approved, feedback: '',
  });
  updateSession(sessionId, { pendingPerm: null });
}

export async function resolveAskUser(sessionId, askId, answers) {
  await api('POST', `/api/sessions/${sessionId}/ask`, {
    id: askId, answers,
  });
  updateSession(sessionId, { pendingAsk: null });
}

export async function resumeSession(id) {
  const sess = await api('POST', `/api/sessions/${id}/resume`);
  await loadSessions();
  if (state.isMobile) {
    setActiveSession(sess.id);
  } else {
    assignToTile(state.focusedTile, sess.id);
  }
  return sess;
}

export async function configureSession(id, { model, thinking }) {
  const res = await api('PATCH', `/api/sessions/${id}/config`, { model, thinking });
  if (res) {
    updateSession(id, {
      model: res.model || state.sessions[id]?.model,
      thinking: res.thinking || state.sessions[id]?.thinking,
    });
  }
  return res;
}

export async function trustMcp(id) {
  await api('POST', `/api/sessions/${id}/trust-mcp`);
  updateSession(id, { untrustedMcp: false });
}

export async function execCommand(id, command) {
  return api('POST', `/api/sessions/${id}/command`, { command });
}

export function handleWsTasksUpdate(id, data) {
  updateSession(id, { tasks: data.tasks || [] });
}

export function handleWsPlanMode(id, data) {
  updateSession(id, {
    planMode: data.mode || 'off',
    planFile: data.plan_file || null,
  });
}

export function handleWsCommand(id, data) {
  if (data.command === 'clear') {
    updateSession(id, { messages: [], streamingText: null, thinkingText: null });
  } else if (data.command === 'compact') {
    // Server sends updated messages with the compact event.
    if (data.messages) {
      updateSession(id, { messages: normalizeHistory(data.messages) });
    }
  }
}

// --- Tile tree actions ---

export function applyPreset(presetId) {
  // Collect current sessions to re-assign
  const currentSessions = allSessionIds(state.tileTree);
  const tree = presetTree(presetId);
  const newTileIds = allTileIds(tree);
  // Re-assign existing sessions to new tiles
  let result = tree;
  for (let i = 0; i < Math.min(currentSessions.length, newTileIds.length); i++) {
    if (currentSessions[i]) {
      result = setTileSession(result, newTileIds[i], currentSessions[i]);
    }
  }
  const focused = newTileIds[0] || 1;
  setState({ tileTree: result, focusedTile: focused });
  autoFillTiles();
  afterVisibilityChange();
}

export function splitTile(tileId, direction) {
  const tree = splitTileNode(state.tileTree, tileId, direction);
  setState({ tileTree: tree });
  // Focus the new empty tile (it's the second child of the new split)
  const ids = allTileIds(tree);
  const oldIds = allTileIds(state.tileTree);
  const newId = ids.find(id => !oldIds.includes(id));
  if (newId) setState({ focusedTile: newId });
  autoFillTiles();
  afterVisibilityChange();
}

export function closeTile(tileId) {
  if (tileCount(state.tileTree) <= 1) return;
  const tree = removeTileNode(state.tileTree, tileId);
  // If focused tile was removed, focus first remaining tile
  const ids = allTileIds(tree);
  const focused = ids.includes(state.focusedTile) ? state.focusedTile : ids[0];
  setState({ tileTree: tree, focusedTile: focused });
  afterVisibilityChange();
}

export function assignToTile(tileId, sessionId) {
  // Remove from any other tile first (unique assignment)
  let tree = clearSession(state.tileTree, sessionId);
  tree = setTileSession(tree, tileId, sessionId);
  setState({ tileTree: tree, focusedTile: tileId });
  afterVisibilityChange();
}

export function focusTile(tileId) {
  const ids = allTileIds(state.tileTree);
  if (!ids.includes(tileId)) return;
  setState({ focusedTile: tileId });
  requestAnimationFrame(() => {
    const tile = document.querySelector(`[data-tile-id="${tileId}"]`);
    if (tile) {
      const ta = tile.querySelector('textarea');
      if (ta) ta.focus();
    }
  });
}

export function focusTileByIndex(idx) {
  const ids = allTileIds(state.tileTree);
  if (idx >= 0 && idx < ids.length) focusTile(ids[idx]);
}

export function swapTiles(id1, id2) {
  if (id1 === id2) return;
  const tree = swapSessions(state.tileTree, id1, id2);
  setState({ tileTree: tree, focusedTile: id2 });
  afterVisibilityChange();
}

export function resizeSplit(path, ratio) {
  const tree = setRatioAtPath(state.tileTree, path, ratio);
  setState({ tileTree: tree });
}

export function setActiveSession(id) {
  setState({ activeSession: id });
  afterVisibilityChange();
}


export function toggleSound() { setState({ soundEnabled: !state.soundEnabled }); }
export function setMobile(isMobile) { setState({ isMobile }); }

// --- Auto-fill tiles with active sessions ---

export function autoFillTiles() {
  const assigned = new Set(allSessionIds(state.tileTree));
  const available = Object.values(state.sessions)
    .filter(s => s.state !== 'saved' && !assigned.has(s.id))
    .sort((a, b) => (b.updated || 0) - (a.updated || 0));

  if (available.length === 0) return;

  let tree = state.tileTree;
  let changed = false;
  for (const tileId of allTileIds(tree)) {
    if (available.length === 0) break;
    const tile = findTile(tree, tileId);
    if (tile && !tile.sessionId) {
      tree = setTileSession(tree, tileId, available.shift().id);
      changed = true;
    }
  }
  if (changed) {
    setState({ tileTree: tree });
    afterVisibilityChange();
  }
}

export function autoSelectMobile() {
  if (state.activeSession && state.sessions[state.activeSession]) return;
  const active = Object.values(state.sessions)
    .filter(s => s.state !== 'saved')
    .sort((a, b) => (b.updated || 0) - (a.updated || 0));
  if (active.length > 0) {
    setState({ activeSession: active[0].id });
    afterVisibilityChange();
  }
}

const resumingIds = new Set();

function afterVisibilityChange() {
  const visible = visibleSessionIds(state);

  // Auto-resume saved sessions that are visible in tiles.
  // Without this, syncConnections would try to open a WebSocket to a session
  // that only exists on disk, getting 404 → infinite reconnect loop.
  for (const id of visible) {
    const sess = state.sessions[id];
    if (sess?.state === 'saved' && !resumingIds.has(id)) {
      resumingIds.add(id);
      resumeSession(id)
        .catch(e => console.error('Auto-resume failed for', id, e))
        .finally(() => resumingIds.delete(id));
    }
  }

  // Only open WebSockets for non-saved sessions (resumed ones will
  // trigger another afterVisibilityChange via loadSessions).
  const connectable = visible.filter(id => state.sessions[id]?.state !== 'saved');
  syncConnections(connectable);
}
