// session-actions.js — API-backed session operations

import { api } from './api.js';
import { triggerAttention } from './notifications.js';
import { store, setState, updateSession, visibleSessionIds } from './store.js';
import {
  assignToTile, setActiveSession, afterVisibilityChange, autoFillTiles,
} from './tile-actions.js';
import { allSessionIds, clearSession } from './tileTree.js';

let pollTimer = null;

export async function loadSessions() {
  try {
    const state = store.get();
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
        contextPercent: info.context_percent ?? (existing ? existing.contextPercent : -1),
        permissionMode: info.permission_mode || (existing ? existing.permissionMode : 'yolo'),
        pendingPerm: existing ? existing.pendingPerm : null,
        pendingAsk: existing ? existing.pendingAsk : null,
        pendingSteers: existing ? existing.pendingSteers : null,
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
    const currentState = store.get();
    let tree = currentState.tileTree;
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

export async function createSession(opts) {
  const sess = await api('POST', '/api/sessions', opts);
  await loadSessions();
  const state = store.get();
  const id = sess.id;
  if (state.isMobile) {
    setActiveSession(id);
  } else {
    assignToTile(state.focusedTile, id);
  }
  return sess;
}

export async function deleteSession(id) {
  const state = store.get();
  await api('DELETE', `/api/sessions/${id}`);
  const sessions = { ...state.sessions };
  delete sessions[id];
  const tileTree = clearSession(state.tileTree, id);
  const activeSession = state.activeSession === id ? null : state.activeSession;
  setState({ sessions, tileTree, activeSession });
  afterVisibilityChange();
}

export async function sendMessage(id, text) {
  const state = store.get();
  const sess = state.sessions[id];
  if (!sess) return;

  const isIdle = sess.state === 'idle' || sess.state === 'error';
  if (isIdle) {
    const userMsg = { role: 'user', content: [{ type: 'text', text }] };
    updateSession(id, {
      messages: [...sess.messages, userMsg],
      state: 'running',
      streamingText: null,
      thinkingText: null,
    });
  } else {
    const current = store.get().sessions[id];
    const steers = current?.pendingSteers || [];
    updateSession(id, { pendingSteers: [...steers, text] });
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
  const state = store.get();
  if (state.isMobile) {
    setActiveSession(sess.id);
  } else {
    assignToTile(state.focusedTile, sess.id);
  }
  return sess;
}

export async function configureSession(id, { model, thinking, permissionMode }) {
  const body = {};
  if (model) body.model = model;
  if (thinking) body.thinking = thinking;
  if (permissionMode) body.permission_mode = permissionMode;
  const res = await api('PATCH', `/api/sessions/${id}/config`, body);
  if (res) {
    const patch = {};
    if (res.model) patch.model = res.model;
    if (res.thinking) patch.thinking = res.thinking;
    if (res.permission_mode) patch.permissionMode = res.permission_mode;
    updateSession(id, patch);
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

export async function execShell(id, command, silent) {
  const result = await api('POST', `/api/sessions/${id}/shell`, { command, silent });
  const output = (result.output || '').replace(/\n$/, '');
  const isError = result.exit_code !== 0;

  const state = store.get();
  const sess = state.sessions[id];
  if (sess) {
    const toolMsg = {
      _type: 'tool_start',
      tool_call_id: 'shell_' + Date.now(),
      tool_name: 'bash',
      args: { command },
      status: isError ? 'error' : 'done',
      result: output,
    };
    updateSession(id, { messages: [...sess.messages, toolMsg] });
  }

  return result;
}
