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
    const list = await api('GET', '/api/sessions');
    // Read the store AFTER the round-trip: WS handlers may have updated
    // sessions while the request was in flight, and rebuilding from a
    // pre-await snapshot would silently revert those (lost messages, perms).
    const state = store.get();
    const prev = state.sessions;
    const visible = new Set(visibleSessionIds(state));
    const sessions = {};
    for (const info of list) {
      const existing = prev[info.id];
      // A visible session has a live WS connection that owns its live-tracked
      // fields (state, config, context, plan). This poll response may already
      // be stale relative to WS events that arrived while the request was in
      // flight, so keep the WS-tracked values rather than reverting them.
      // Hidden sessions have no WS connection, so the poll is their only source
      // of truth and must refresh those fields.
      const wsOwns = existing && visible.has(info.id);
      sessions[info.id] = {
        id: info.id,
        title: info.title,
        state: wsOwns ? existing.state : info.state,
        model: wsOwns ? existing.model : info.model,
        thinking: wsOwns ? existing.thinking : (info.thinking || ''),
        cwd: info.cwd,
        error: wsOwns ? existing.error : (info.error || null),
        untrustedMcp: info.untrusted_mcp || false,
        messages: existing ? existing.messages : [],
        contextPercent: wsOwns ? existing.contextPercent : (info.context_percent ?? (existing ? existing.contextPercent : -1)),
        permissionMode: wsOwns ? existing.permissionMode : (info.permission_mode || (existing ? existing.permissionMode : 'yolo')),
        pendingPerm: existing ? existing.pendingPerm : null,
        pendingAsk: existing ? existing.pendingAsk : null,
        pendingSteers: existing ? existing.pendingSteers : null,
        streamingText: existing ? existing.streamingText : null,
        thinkingText: existing ? existing.thinkingText : null,
        subagentCount: existing ? existing.subagentCount : 0,
        autoVerifying: existing ? existing.autoVerifying : false,
        onOverage: existing ? existing.onOverage : false,
        tasks: existing ? existing.tasks : [],
        planMode: wsOwns ? existing.planMode : (info.plan_mode || (existing ? existing.planMode : 'off')),
        planFile: wsOwns ? existing.planFile : (info.plan_file || (existing ? existing.planFile : null)),
      };
    }
    // Detect attention transitions (hidden sessions only)
    for (const [id, sess] of Object.entries(sessions)) {
      const prevSess = prev[id];
      if (prevSess && prevSess.state !== sess.state) {
        if (sess.state === 'permission' || sess.state === 'error') {
          if (!visible.has(id)) {
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
  // On mobile only one session is visible (and WS-backed); the poll just keeps
  // the list and hidden sessions fresh, and push covers anything urgent — so a
  // slower cadence saves battery/data. The foreground handler refreshes on
  // return, so a stale gap while backgrounded doesn't matter.
  const interval = store.get().isMobile ? 15000 : 3000;
  pollTimer = setInterval(loadSessions, interval);
}

export function stopPolling() {
  if (pollTimer) { clearInterval(pollTimer); pollTimer = null; }
}

let usageTimer = null;

// loadUsage refreshes the global plan usage snapshot. Failures keep the
// previous snapshot rather than clearing the widget.
export async function loadUsage() {
  try {
    const usage = await api('GET', '/api/usage');
    setState({ usage });
  } catch (e) {
    console.error('loadUsage failed:', e);
  }
}

export function startUsagePolling() {
  stopUsagePolling();
  loadUsage();
  usageTimer = setInterval(loadUsage, 60000);
}

export function stopUsagePolling() {
  if (usageTimer) { clearInterval(usageTimer); usageTimer = null; }
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
  await api('DELETE', `/api/sessions/${id}`);
  // Read the store after the await so concurrent WS updates to other
  // sessions aren't clobbered by a stale pre-request snapshot.
  const state = store.get();
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

export async function resolvePermission(sessionId, permId, approved, opts = {}) {
  await api('POST', `/api/sessions/${sessionId}/permission`, {
    id: permId,
    approved,
    feedback: opts.feedback || '',
    allow: opts.allow || '',
  });
  updateSession(sessionId, { pendingPerm: null });
}

export async function addPermissionRule(sessionId, permId, rule) {
  await api('POST', `/api/sessions/${sessionId}/permission`, {
    id: permId,
    action: 'add_rule',
    rule,
  });
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
  const res = await api('POST', `/api/sessions/${id}/command`, { command });
  if (res && res.newSessionId) {
    await loadSessions();
    const state = store.get();
    if (state.isMobile) {
      setActiveSession(res.newSessionId);
    } else {
      assignToTile(state.focusedTile, res.newSessionId);
    }
  }
  return res;
}

// fetchBranchPoints returns the conversation's branch targets (user/assistant
// turns) for the rewind picker.
export async function fetchBranchPoints(id) {
  return api('GET', `/api/sessions/${id}/branches`);
}

// branchTo rewinds the conversation to entryId, starting a new branch from that
// point. The server publishes a CommandExecuted event that reloads the message
// list over the WebSocket, so callers don't need to apply the result manually.
export async function branchTo(id, entryId) {
  return api('POST', `/api/sessions/${id}/branch`, { entry_id: entryId });
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
