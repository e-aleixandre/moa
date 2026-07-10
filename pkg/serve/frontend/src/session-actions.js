// session-actions.js — API-backed session operations

import { api } from './api.js';
import { normalizeHistory } from './ws-handlers.js';
import { triggerAttention, addToast } from './notifications.js';
import { store, setState, updateSession, visibleSessionIds } from './store.js';
import {
  assignToTile, setActiveSession, afterVisibilityChange, autoFillTiles,
} from './tile-actions.js';
import { allSessionIds, clearSession } from './tileTree.js';

let pollTimer = null;

// cacheExpiresAtMs parses the server's cache_expires_at into an epoch-ms number,
// returning 0 for absent/unparseable/non-positive values. The backend omits the
// field when not applicable (omitzero), but a defensive guard keeps a stray Go
// zero-time ("0001-01-01…", which parses to a negative number) from being
// treated as a real deadline and spinning up a pointless UI timer.
function cacheExpiresAtMs(iso) {
  if (!iso) return 0;
  const ms = Date.parse(iso);
  return Number.isFinite(ms) && ms > 0 ? ms : 0;
}

function samePolledSession(existing, next) {
  return !!existing && Object.keys(next).every(key => Object.is(existing[key], next[key]));
}

export async function loadSessions() {
  try {
    const [list, attention] = await Promise.all([
      api('GET', '/api/sessions'),
      api('GET', '/api/attention'),
    ]);
    // Read the store AFTER the round-trip: WS handlers may have updated
    // sessions while the request was in flight, and rebuilding from a
    // pre-await snapshot would silently revert those (lost messages, perms).
    const state = store.get();
    const prev = state.sessions;
    const visible = new Set(visibleSessionIds(state));
    const sessions = {};
	let sessionsChanged = Object.keys(prev).length !== list.length;
    for (const info of list) {
      const existing = prev[info.id];
      // A visible session has a live WS connection that owns its live-tracked
      // fields (state, config, context, plan). This poll response may already
      // be stale relative to WS events that arrived while the request was in
      // flight, so keep the WS-tracked values rather than reverting them.
      // Hidden sessions have no WS connection, so the poll is their only source
      // of truth and must refresh those fields.
      const wsOwns = existing && visible.has(info.id);
      const next = {
        id: info.id,
        title: info.title,
        state: wsOwns ? existing.state : info.state,
        model: wsOwns ? existing.model : info.model,
        provider: wsOwns ? existing.provider : info.provider,
        thinking: wsOwns ? existing.thinking : (info.thinking || ''),
        cwd: info.cwd,
        updated: info.updated ? Date.parse(info.updated) : (existing ? existing.updated : 0),
        cacheExpiresAt: cacheExpiresAtMs(info.cache_expires_at),
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
        // Live subagent transcripts are WS-only state (fed by subagent_start/
        // event/end); the poll response knows nothing about them, so always
        // carry them over or the agent tray vanishes on every poll tick.
        subagents: existing ? existing.subagents : {},
        viewingSubagent: existing ? existing.viewingSubagent : null,
        autoVerifying: existing ? existing.autoVerifying : false,
        compacting: existing ? existing.compacting : false,
        onOverage: existing ? existing.onOverage : false,
        tasks: existing ? existing.tasks : [],
        planMode: wsOwns ? existing.planMode : (info.plan_mode || (existing ? existing.planMode : 'off')),
        planFile: wsOwns ? existing.planFile : (info.plan_file || (existing ? existing.planFile : null)),
        costUSD: wsOwns ? existing.costUSD : (info.cost_usd ?? (existing ? existing.costUSD : 0)),
        unseen: existing ? existing.unseen : false,
        // archived is server-owned (no WS event tracks it), so the poll is
        // always the source of truth here — unlike wsOwns fields above, we
        // don't prefer the optimistic `existing` value. An in-flight optimistic
        // archiveSession/unarchiveSession update gets briefly overwritten by a
        // stale poll only if the poll started before the archive call
        // resolved; the next poll (≤3s desktop / ≤15s mobile) self-corrects.
        archived: info.archived || false,
      };
		if (samePolledSession(existing, next)) {
			sessions[info.id] = existing;
		} else {
			sessions[info.id] = next;
			sessionsChanged = true;
		}
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
		if (sessionsChanged || attention) {
			setState({ sessions, attentionItems: attention?.items || [] });
		}
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

// archiveSession "closes" a session: unlike deleteSession it doesn't remove
// the session from the store, it just flips `archived`, which hides it from
// the TabBar/overview. It still needs to drop the session from wherever it's
// currently visible (tile/activeSession), mirroring deleteSession, so the UI
// doesn't keep showing a closed session as if it were open.
export async function archiveSession(id, archived = true) {
  await api('POST', `/api/sessions/${id}/archive`, { archived });
  // Reflect immediately so the UI updates without waiting for the next poll
  // (which can lag up to ~15s on mobile). The server already committed above.
  updateSession(id, { archived });
  if (!archived) return;
  const state = store.get();
  const tileTree = clearSession(state.tileTree, id);
  const activeSession = state.activeSession === id ? null : state.activeSession;
  setState({ tileTree, activeSession });
  afterVisibilityChange();
}

// unarchiveSession reopens a closed session. The server also auto-unarchives
// on send/resume, but reopening from the palette can assign an already-loaded
// session straight into a tile without going through send/resume, so we flip
// the flag explicitly here too.
export async function unarchiveSession(id) {
  await api('POST', `/api/sessions/${id}/archive`, { archived: false });
  updateSession(id, { archived: false });
}

export async function cancelBashJob(sessionId, jobId) {
  return api('POST', `/api/sessions/${sessionId}/bash-jobs/${jobId}/cancel`);
}

// attachmentToContent converts a client-side attachment into the same content
// block shape the server builds (see pkg/serve/attachments.go), so the
// optimistic echo below renders identically to what comes back after a reload.
function attachmentToContent(a) {
  if (a.isImage) {
    return { type: 'image', data: a.data, mime_type: a.mime };
  }
  const name = a.name.replaceAll('"', '\\"');
  const text = base64ToUtf8(a.data);
  return { type: 'text', text: `<attachment name="${name}">\n${text}\n</attachment>` };
}

function base64ToUtf8(b64) {
  try {
    const binary = atob(b64);
    const bytes = Uint8Array.from(binary, (c) => c.charCodeAt(0));
    return new TextDecoder('utf-8').decode(bytes);
  } catch (_) {
    return '';
  }
}

export async function sendMessage(id, text, attachments = []) {
  const state = store.get();
  const sess = state.sessions[id];
  if (!sess) return;

  const isIdle = sess.state === 'idle' || sess.state === 'error';
  let optimisticMsg = null;
  if (isIdle) {
    // Attachment blocks first, text last — matches the order the server sends
    // to the agent (see Manager.Send).
    const content = attachments.map(attachmentToContent);
    if (text) content.push({ type: 'text', text });
    optimisticMsg = { role: 'user', content };
    updateSession(id, {
      messages: [...sess.messages, optimisticMsg],
      state: 'running',
      streamingText: null,
      thinkingText: null,
    });
  } else {
    const current = store.get().sessions[id];
    const steers = current?.pendingSteers || [];
    updateSession(id, { pendingSteers: [...steers, text] });
  }

  try {
    const res = await api('POST', `/api/sessions/${id}/send`, {
      text,
      attachments: attachments.map((a) => ({ name: a.name, mime: a.mime, data: a.data })),
    });
    return res?.action || 'send';
  } catch (e) {
    // Roll back the optimistic echo so a rejected send (e.g. 400 on a bad
    // attachment) doesn't leave a phantom message stuck in "running". Remove
    // exactly the message we appended (by reference), leaving any events that
    // arrived meanwhile untouched.
    if (optimisticMsg) {
      const cur = store.get().sessions[id];
      if (cur) {
        updateSession(id, {
          messages: cur.messages.filter((m) => m !== optimisticMsg),
          state: 'idle',
          streamingText: null,
          thinkingText: null,
        });
      }
    }
    throw e;
  }
}

export async function cancelRun(id) {
  await api('POST', `/api/sessions/${id}/cancel`);
}

export async function cancelSubagent(id, jobId) {
  await api('POST', `/api/sessions/${id}/subagents/${jobId}/cancel`);
}

// promoteSubagent detaches a synchronous (blocking) subagent so it keeps
// running in the background after the turn that spawned it ends. The server
// echoes the flip over the WebSocket (subagent_start with async:true), which
// flips sa.async in the store and makes the promote button disappear.
export async function promoteSubagent(id, jobId) {
  await api('POST', `/api/sessions/${id}/subagents/${jobId}/promote`);
}

// steerSubagent sends a message to a live subagent's child agent. Returns the
// server response ({ queued: bool }); there's no WS echo for this (parity with
// cancelSubagent), so the caller shows optimistic visual feedback.
export async function steerSubagent(id, jobId, text) {
  return api('POST', `/api/sessions/${id}/subagents/${jobId}/steer`, { text });
}

// openPersistedSubagent loads a finished subagent's transcript from disk and
// opens it in the SubagentView. Used when clicking a subagent card in the chat
// after the live tray entry is gone.
export async function openPersistedSubagent(id, jobId) {
  const sess = store.get().sessions[id];
  if (!sess) return;
  // If we still have it live in memory, just open it.
  if (sess.subagents && sess.subagents[jobId]) {
    updateSession(id, { viewingSubagent: jobId });
    return;
  }
  const t = await api('GET', `/api/sessions/${id}/subagents/${jobId}`);
  if (!t) return;
  const usage = (t.cost_usd || (t.usage && (t.usage.input || t.usage.output)))
    ? {
        inputTokens: (t.usage && t.usage.input) || 0,
        outputTokens: (t.usage && t.usage.output) || 0,
        costUSD: t.cost_usd || 0,
      }
    : null;
  const subs = { ...(store.get().sessions[id].subagents || {}) };
  subs[jobId] = {
    jobId,
    task: t.task || '',
    model: t.model || '',
    status: t.status || 'completed',
    async: !!t.async,
    messages: normalizeHistory(t.messages || []),
    streamingText: null,
    thinkingText: null,
    usage,
  };
  updateSession(id, { subagents: subs, viewingSubagent: jobId });
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
  const isError = result.exit_code !== 0 || result.timed_out;

  const state = store.get();
  const sess = state.sessions[id];
  if (sess) {
    const toolMsg = {
      _type: 'tool_start',
      tool_call_id: 'shell_' + Date.now(),
      tool_name: 'bash',
      args: { command },
      status: isError ? 'error' : 'done',
      result: result.timed_out ? `${output}\n(timed out)` : output,
    };
    updateSession(id, { messages: [...sess.messages, toolMsg] });
  }

  if (result.delivery_error) {
    addToast({ title: 'Shell output not delivered', detail: result.delivery_error, type: 'error' });
  }

  return result;
}
