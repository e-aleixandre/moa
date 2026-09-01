// WebSocket attention and permission handling.

import { triggerAttention } from '../notifications.js';
import { acknowledgeVisibleAttentionThrough } from '../api.js';
import { store, setState, updateSession, visibleSessionIds } from '../store.js';
import { finishHistoryHydration } from '../history-hydration.js';

export function parseAttentionNamespace(namespace) {
  const separator = typeof namespace === 'string' ? namespace.lastIndexOf(':') : -1;
  if (separator <= 0) return null;
  const incarnationText = namespace.slice(separator + 1);
  if (!/^\d+$/.test(incarnationText)) return null;
  const incarnation = Number(incarnationText);
  if (!Number.isSafeInteger(incarnation) || incarnation < 1) return null;
  return { serverInstance: namespace.slice(0, separator), incarnation };
}

// An init is usable for the cursor only when its namespace is well-formed and
// belongs to the runtime that sent it. This keeps malformed cursor data from
// becoming either a read boundary or a future frame's namespace.
export function attentionNamespaceFromInit(data) {
  const namespace = data?.attention_namespace;
  const parsed = parseAttentionNamespace(namespace);
  if (!parsed || parsed.serverInstance !== data?.server_instance) return '';
  return namespace;
}

// The namespace is an ordered runtime incarnation, not an opaque token: a
// delayed roster response for A must not reset state after the client accepted
// B. Different server processes are unordered, so only the roster may move
// between them; a socket may advance incarnations within its own process.
export function attentionNamespaceTransition(session, namespace, { allowCrossProcess = true } = {}) {
  const current = session?.attentionNamespace || '';
  const next = parseAttentionNamespace(namespace);
  if (!next) return { accepted: false, reset: false, namespace: current };
  if (!current) return { accepted: true, reset: false, namespace };
  const previous = parseAttentionNamespace(current);
  if (!previous) return { accepted: false, reset: false, namespace: current };
  if (current === namespace) return { accepted: true, reset: false, namespace };
  if (previous.serverInstance !== next.serverInstance) {
    return allowCrossProcess
      ? { accepted: true, reset: true, namespace }
      : { accepted: false, reset: false, namespace: current };
  }
  if (next.incarnation > previous.incarnation) return { accepted: true, reset: true, namespace };
  return { accepted: false, reset: false, namespace: current };
}

// Apply an accepted socket namespace transition before its init or frames use
// the cursor. An init is fresher than the roster, but follows the same ordered
// incarnation rule.
export function adoptAttentionNamespace(id, namespace) {
  const session = store.get().sessions[id];
  const transition = attentionNamespaceTransition(session, namespace);
  if (!transition.accepted) return transition;
  if (transition.reset) {
    updateSession(id, {
      attentionNamespace: transition.namespace,
      unseen: false,
      unseenSeq: 0,
      ackedThroughSeq: 0,
      readCandidateSeq: 0,
    });
  } else if (session?.attentionNamespace !== transition.namespace) {
    updateSession(id, { attentionNamespace: transition.namespace });
  }
  return transition;
}

// mergeSteers reconciles the authoritative server queue from an init snapshot
// with any local optimistic chips. The snapshot (each item carrying its
// client-minted ID) is authoritative. A local chip is kept only if it is still
// in flight (its POST hasn't returned, so confirmed !== true) and not already
// in the snapshot: that covers a steer sent moments before the cut. A confirmed
// chip absent from the snapshot was delivered or cancelled server-side, so it is

export function handleWsAskUser(id, data, seq = 0) {
  updateSession(id, {
    pendingAsk: { id: data.id, questions: data.questions },
  });
  markUnseen(id, seq, true);
  acknowledgeVisibleLiveAttention(id, seq);
  const state = store.get();
  if (!visibleSessionIds(state).includes(id)) {
    flashSession(id, 'attention');
    const sess = state.sessions[id];
    if (sess) triggerAttention(sess, 'ask_user', state.soundEnabled);
  }
}

export function handleWsPermissionRequest(id, data, seq = 0) {
  updateSession(id, {
    state: 'permission',
    pendingPerm: {
      id: data.id,
      tool_name: data.tool_name,
      args: data.args,
      allow_pattern: data.allow_pattern || '',
    },
  });
  markUnseen(id, seq, true);
  acknowledgeVisibleLiveAttention(id, seq);
  flashSession(id, 'attention');
  const state = store.get();
  if (!visibleSessionIds(state).includes(id)) {
    const sess = state.sessions[id];
    if (sess) triggerAttention(sess, data.tool_name, state.soundEnabled);
  }
}

// A terminal event rendered live in the selected session is itself the read:
// the user was watching that run finish. It must POST /read rather than only
// clearing local state, or the next roster poll surfaces a dot for a result
// this client just showed.
export function acknowledgeVisibleLiveAttention(id, seq) {
  const state = store.get();
  if (!visibleSessionIds(state).includes(id)) return;
  const session = state.sessions[id];
  if (seq > 0 && session?.attentionNamespace) {
    acknowledgeVisibleAttentionThrough(id, seq, session.attentionNamespace).catch(() => {});
  }
}

export function handleWsPermissionResolved(id, data) {
  const perm = store.get().sessions[id]?.pendingPerm;
  if (!perm) return;
  if (data?.id && data.id !== perm.id) return;
  updateSession(id, {
    pendingPerm: null,
  });
}

export function handleWsAskResolved(id, data) {
  const ask = store.get().sessions[id]?.pendingAsk;
  if (!ask) return;
  if (data?.id && data.id !== ask.id) return;
  updateSession(id, {
    pendingAsk: null,
  });
}

export function flashSession(id, type) {
  updateSession(id, { flash: type });
  setTimeout(() => {
    if (store.get().sessions[id]?.flash === type) updateSession(id, { flash: null });
  }, 1300);
}

// markUnseen flags a session as having unread activity when the user isn't
// currently looking at it (not visible, or the tab is backgrounded), so a badge
// can nudge them back. Cleared by afterVisibilityChange when it comes into view.
export function markUnseen(id, seq = 0, isNewOccurrence = false) {
  const state = store.get();
  const hidden = typeof document !== 'undefined' && document.hidden;
  const sess = state.sessions[id];
  if (!sess || seq === 0) return;
  const visible = visibleSessionIds(state).includes(id) && !hidden;
  const unseenSeq = Math.max(sess.unseenSeq || 0, seq);
  if (visible) {
    if (unseenSeq !== (sess.unseenSeq || 0)) updateSession(id, { unseenSeq });
    return;
  }
  const arrival = (isNewOccurrence || !sess.unseen) ? (sess.attentionArrival || 0) + 1 : sess.attentionArrival || 0;
  if (!sess.unseen || arrival !== sess.attentionArrival || unseenSeq !== (sess.unseenSeq || 0)) {
    updateSession(id, { unseen: true, unseenSeq, attentionArrival: arrival });
  }
}

// isSessionAway is true when the user isn't looking at a session (tab hidden or
// the session not on screen) — the same condition markUnseen uses. Toasts for
// in-chat activity (subagent/bash completion) only fire when away, since a
// visible delegation/background block already reports the outcome.
export function isSessionAway(id) {
  const state = store.get();
  const hidden = typeof document !== 'undefined' && document.hidden;
  return hidden || !visibleSessionIds(state).includes(id);
}
