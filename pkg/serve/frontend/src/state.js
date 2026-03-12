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
  drawerOpen: false,
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
  updateSession(id, {
    messages: normalizeHistory(data.messages || []),
    state: data.state || 'idle',
    pendingPerm: data.pending_permission || null,
    streamingText: null,
    thinkingText: null,
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
          let status = 'done';
          if (tr) {
            resultText = (tr.content || []).filter(x => x.type === 'text').map(x => x.text).join('');
            if (tr.is_error) status = 'error';
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

export function handleWsTextDelta(id, delta) {
  const sess = state.sessions[id];
  if (!sess) return;
  updateSession(id, { streamingText: (sess.streamingText || '') + delta });
}

export function handleWsThinkingDelta(id, delta) {
  const sess = state.sessions[id];
  if (!sess) return;
  updateSession(id, { thinkingText: (sess.thinkingText || '') + delta });
}

export function handleWsMessageEnd(id, fullText) {
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
  const sess = state.sessions[id];
  if (!sess) return;
  const messages = sess.messages.map(m => {
    if (m._type === 'tool_start' && m.tool_call_id === data.tool_call_id) {
      return { ...m, streamingResult: (m.streamingResult || '') + data.delta };
    }
    return m;
  });
  updateSession(id, { messages });
}

export function handleWsToolEnd(id, data) {
  const sess = state.sessions[id];
  if (!sess) return;
  const messages = sess.messages.map(m => {
    if (m._type === 'tool_start' && m.tool_call_id === data.tool_call_id) {
      return { ...m, status: data.is_error ? 'error' : 'done', result: data.result, streamingResult: null };
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

export function toggleDrawer() { setState({ drawerOpen: !state.drawerOpen }); }
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

function afterVisibilityChange() {
  const visible = visibleSessionIds(state);
  syncConnections(visible);
}
