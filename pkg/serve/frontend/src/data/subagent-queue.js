// subagent-queue.js — a subagent's queue, shown in its view exactly as the
// session's own queue is shown at the end of the thread (QueuedTail).
//
// The child's queue is the server's: it holds what the owner sent from the
// subagent view and what the parent sent with subagent_steer, and it reaches
// the client as the same frames the session's queue uses, wrapped in
// subagent_event (steer_queued, steers_redirected, steers_canceled, and the
// steer that delivers a message). The state lives on the subagent entry as
// `pendingSteers`, in the shape of the session's own.

import { store, updateSession } from './store.js';

// subagentComposerKey names a subagent's steer box in the composerDrops
// handoff, so a recall of the child's queue lands in that box and never in
// the parent's.
export function subagentComposerKey(sessionId, jobId) {
  return `${sessionId}:subagent:${jobId}`;
}

// reduceSubagentQueue applies one wrapped frame to a child's queue and returns
// the next queue (null when empty), or `pending` itself when the frame is not
// about the queue. `messages` is the child's transcript, already reduced up to
// this frame: a steer_queued that lost the race to its own delivery must not
// queue the message again.
export function reduceSubagentQueue(pending, evt, messages) {
  const data = evt?.data || {};
  const queue = pending || [];
  switch (evt?.type) {
    case 'steer_queued': {
      if (!data.id || queue.some((s) => s.id === data.id)) return pending;
      if ((messages || []).some((m) => m?._steer_id === data.id)) return pending;
      return [...queue, {
        id: data.id,
        text: data.text || '',
        confirmed: true,
        ...(data.non_recallable ? { non_recallable: true } : {}),
        ...(data.redirect ? { redirect: true } : {}),
      }];
    }
    case 'steer': {
      if (!data.id || !queue.some((s) => s.id === data.id)) return pending;
      return orNull(queue.filter((s) => s.id !== data.id));
    }
    case 'steers_redirected': {
      const marked = new Set(data.steer_ids || []);
      if (marked.size === 0) return pending;
      let changed = false;
      const next = queue.map((s) => {
        if (!marked.has(s.id) || s.redirect) return s;
        changed = true;
        return { ...s, redirect: true };
      });
      return changed ? next : pending;
    }
    case 'steers_canceled': {
      if (!Array.isArray(data.discarded_steer_ids)) return null;
      const gone = new Set(data.discarded_steer_ids);
      return orNull(queue.filter((s) => !gone.has(s.id)));
    }
    default:
      return pending;
  }
}

function orNull(queue) {
  return queue.length > 0 ? queue : null;
}

export function subagentQueue(sessionId, jobId) {
  return store.get().sessions[sessionId]?.subagents?.[jobId]?.pendingSteers || null;
}

// setSubagentQueue replaces a child's queue; a no-op when the job is gone.
export function setSubagentQueue(sessionId, jobId, next) {
  const sess = store.get().sessions[sessionId];
  const sub = sess?.subagents?.[jobId];
  if (!sub || sub.pendingSteers === next) return;
  updateSession(sessionId, { subagents: { ...sess.subagents, [jobId]: { ...sub, pendingSteers: next } } });
}
