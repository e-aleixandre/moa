// state.js — immutable snapshot store with pub/sub

import { api, syncConnections } from './api.js';
import { triggerAttention } from './notifications.js';

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
      layout: s.layout,
      tileAssignments: s.tileAssignments,
      focusedTile: s.focusedTile,
      sidebarOpen: s.sidebarOpen,
      soundEnabled: s.soundEnabled,
    }));
  } catch (_) { /* ignore */ }
}

const persisted = loadPersistedState();

let state = {
  sessions: {},

  layout: persisted.layout || 1,
  tileAssignments: persisted.tileAssignments || [null],
  focusedTile: persisted.focusedTile || 0,
  sidebarOpen: persisted.sidebarOpen !== undefined ? persisted.sidebarOpen : true,
  soundEnabled: persisted.soundEnabled || false,

  isMobile: false,
  drawerOpen: false,
  dialogOpen: false,
  activeSession: null,
};

// Ensure tileAssignments length matches layout
while (state.tileAssignments.length < state.layout) state.tileAssignments.push(null);
state.tileAssignments = state.tileAssignments.slice(0, state.layout);

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
  return s.tileAssignments.filter(id => id !== null);
}

export function sessionsByGroup(s) {
  const groups = {};
  for (const sess of Object.values(s.sessions)) {
    const key = sess.cwd || 'Unknown';
    if (!groups[key]) groups[key] = [];
    groups[key].push(sess);
  }
  // Sort each group by updated descending
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
        cwd: info.cwd,
        error: info.error || null,
        untrustedMcp: info.untrusted_mcp || false,
        // Preserve existing WS-populated data if available
        messages: existing ? existing.messages : [],
        pendingPerm: existing ? existing.pendingPerm : null,
        streamingText: existing ? existing.streamingText : null,
        thinkingText: existing ? existing.thinkingText : null,
        subagentCount: existing ? existing.subagentCount : 0,
      };
    }
    // Detect attention transitions (poll path — hidden sessions)
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
    // Clean tile assignments — remove deleted sessions
    const validIds = new Set(Object.keys(sessions));
    const cleaned = state.tileAssignments.map(id => id && validIds.has(id) ? id : null);
    if (JSON.stringify(cleaned) !== JSON.stringify(state.tileAssignments)) {
      setState({ tileAssignments: cleaned });
    }
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

/**
 * Normalize raw LLM message history into our flat render list.
 *
 * The backend sends the raw conversation: assistant messages may contain
 * tool_call content blocks, and tool_result messages carry the output.
 * We flatten these into the same shape used by real-time WS events so
 * that MessageList renders them uniformly.
 */
function normalizeHistory(raw) {
  const result = [];
  // Index tool_result messages by tool_call_id for quick lookup
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
          // Flush accumulated text as a message first
          if (textParts.length > 0) {
            result.push({ role: 'assistant', content: [{ type: 'text', text: textParts.join('') }] });
            textParts.length = 0;
          }
          // Emit synthetic tool entry
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
        // thinking blocks are skipped (shown in real-time only)
      }
      // Flush any trailing text
      if (textParts.length > 0) {
        result.push({ role: 'assistant', content: [{ type: 'text', text: textParts.join('') }] });
      }
    } else if (msg.role === 'user') {
      result.push(msg);
    }
    // tool_result messages are consumed above via resultMap, skip here
  }
  return result;
}

export function handleWsTextDelta(id, delta) {
  const sess = state.sessions[id];
  if (!sess) return;
  updateSession(id, {
    streamingText: (sess.streamingText || '') + delta,
  });
}

export function handleWsThinkingDelta(id, delta) {
  const sess = state.sessions[id];
  if (!sess) return;
  updateSession(id, {
    thinkingText: (sess.thinkingText || '') + delta,
  });
}

export function handleWsMessageEnd(id, fullText) {
  const sess = state.sessions[id];
  if (!sess) return;
  // Append the finished assistant message to history and clear streaming
  const msg = {
    role: 'assistant',
    content: [{ type: 'text', text: fullText }],
  };
  updateSession(id, {
    messages: [...sess.messages, msg],
    streamingText: null,
    thinkingText: null,
  });
}

export function handleWsToolStart(id, data) {
  const sess = state.sessions[id];
  if (!sess) return;
  // Track active tool calls in messages as a synthetic entry
  const toolMsg = {
    _type: 'tool_start',
    tool_call_id: data.tool_call_id,
    tool_name: data.tool_name,
    args: data.args,
    status: 'running',
    result: null,
  };
  updateSession(id, {
    messages: [...sess.messages, toolMsg],
  });
}

export function handleWsToolEnd(id, data) {
  const sess = state.sessions[id];
  if (!sess) return;
  const messages = sess.messages.map(m => {
    if (m._type === 'tool_start' && m.tool_call_id === data.tool_call_id) {
      return { ...m, status: data.is_error ? 'error' : 'done', result: data.result };
    }
    return m;
  });
  updateSession(id, { messages });
}

export function handleWsStateChange(id, data) {
  updateSession(id, {
    state: data.state,
    error: data.error || null,
  });
  if (data.state === 'idle' || data.state === 'error') {
    const sess = state.sessions[id];
    if (sess) {
      updateSession(id, { streamingText: null, thinkingText: null });
    }
  }
}

export function handleWsPermissionRequest(id, data) {
  updateSession(id, {
    state: 'permission',
    pendingPerm: { id: data.id, tool_name: data.tool_name, args: data.args },
  });
  const sess = state.sessions[id];
  if (sess) {
    triggerAttention(sess, data.tool_name, state.soundEnabled);
  }
}

export function handleWsSubagentCount(id, count) {
  updateSession(id, { subagentCount: count });
}

export function handleWsSubagentComplete(id, data) {
  const sess = state.sessions[id];
  if (!sess) return;
  const msg = {
    _type: 'system',
    text: data.text,
  };
  updateSession(id, {
    messages: [...sess.messages, msg],
  });
}

export function handleWsRunEnd(id) {
  updateSession(id, {
    streamingText: null,
    thinkingText: null,
  });
}

// --- API actions ---

export async function createSession(opts) {
  const sess = await api('POST', '/api/sessions', opts);
  await loadSessions();
  const id = sess.id;
  if (state.isMobile) {
    setActiveSession(id);
  } else {
    // Assign to focused tile
    assignTile(state.focusedTile, id);
  }
  return sess;
}

export async function deleteSession(id) {
  await api('DELETE', `/api/sessions/${id}`);
  // Clean from state
  const sessions = { ...state.sessions };
  delete sessions[id];
  const tileAssignments = state.tileAssignments.map(tid => tid === id ? null : tid);
  const activeSession = state.activeSession === id ? null : state.activeSession;
  setState({ sessions, tileAssignments, activeSession });
  afterVisibilityChange();
}

export async function sendMessage(id, text) {
  // Optimistically add user message to local state
  const sess = state.sessions[id];
  if (sess) {
    const userMsg = { role: 'user', content: [{ type: 'text', text }] };
    updateSession(id, {
      messages: [...sess.messages, userMsg],
      streamingText: null,
      thinkingText: null,
    });
  }
  await api('POST', `/api/sessions/${id}/send`, { text });
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
    assignTile(state.focusedTile, sess.id);
  }
  return sess;
}

export async function trustMcp(id) {
  await api('POST', `/api/sessions/${id}/trust-mcp`);
  updateSession(id, { untrustedMcp: false });
}

// --- UI actions ---

export function setLayout(n) {
  const assignments = [...state.tileAssignments];
  while (assignments.length < n) assignments.push(null);
  const tileAssignments = assignments.slice(0, n);
  const focusedTile = Math.min(state.focusedTile, n - 1);
  setState({ layout: n, tileAssignments, focusedTile });
  autoFillTiles();
  afterVisibilityChange();
}

export function assignTile(tileIdx, sessionId) {
  if (tileIdx < 0 || tileIdx >= state.layout) return;
  // Remove from any other tile first (unique assignment)
  const assignments = state.tileAssignments.map(id => id === sessionId ? null : id);
  assignments[tileIdx] = sessionId;
  setState({ tileAssignments: assignments, focusedTile: tileIdx });
  afterVisibilityChange();
}

export function focusTile(idx) {
  if (idx >= 0 && idx < state.layout) {
    setState({ focusedTile: idx });
  }
}

export function setActiveSession(id) {
  setState({ activeSession: id });
  afterVisibilityChange();
}

export function toggleDrawer() {
  setState({ drawerOpen: !state.drawerOpen });
}

export function toggleDialog() {
  setState({ dialogOpen: !state.dialogOpen });
}

export function toggleSound() {
  setState({ soundEnabled: !state.soundEnabled });
}

export function toggleSidebar() {
  setState({ sidebarOpen: !state.sidebarOpen });
}

export function setMobile(isMobile) {
  setState({ isMobile });
}

// --- Auto-fill tiles with active sessions ---

export function autoFillTiles() {
  const assigned = new Set(state.tileAssignments.filter(id => id !== null));
  const available = Object.values(state.sessions)
    .filter(s => s.state !== 'saved' && !assigned.has(s.id))
    .sort((a, b) => (b.updated || 0) - (a.updated || 0));

  let changed = false;
  const assignments = [...state.tileAssignments];
  for (let i = 0; i < assignments.length; i++) {
    if (assignments[i] === null && available.length > 0) {
      assignments[i] = available.shift().id;
      changed = true;
    }
  }
  if (changed) {
    setState({ tileAssignments: assignments });
    afterVisibilityChange();
  }
}

// Auto-select mobile active session
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

// Called after any change that affects which sessions are visible.
// Syncs WS connections.
function afterVisibilityChange() {
  const visible = visibleSessionIds(state);
  syncConnections(visible);
}
