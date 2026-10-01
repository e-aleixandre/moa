// steer-restore.js — the one rule for putting discarded steers back in the
// composer.
//
// The server owns the queue, so it alone says which steers were discarded and
// in which order. A Stop or queue recall of this client carries an operation ID
// the server echoes in its reply and in the `steers_canceled` broadcast; only a
// discard carrying one of those IDs is this client's to restore. Reply and
// broadcast may arrive in either order, or one of them not at all, so each
// steer ID is restored at most once and appended to whatever drop the composer
// has not consumed yet — it never replaces it. A broadcast without one of our
// IDs only removes chips.

import { setState } from './store.js';
import { addToast } from './notifications.js';
import { combineQueueText, droppedImageCount } from './composer-queue.js';

const restoredIDs = new Map(); // sessionId → Set of steer IDs already restored
const ownOperations = new Set(); // operation IDs this client sent and may still hear about

// beginOperation mints the correlation ID for one Stop or recall.
export function beginOperation(kind) {
  let id;
  try {
    if (typeof crypto !== 'undefined' && crypto.randomUUID) id = `${kind}-${crypto.randomUUID()}`;
  } catch { /* fall through */ }
  id ||= `${kind}-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`;
  ownOperations.add(id);
  return id;
}

// endOperation forgets an operation once moa has answered it: its reply named
// every discard. A request that never got an answer stays known, because the
// server may have applied it and its broadcast is then the only word left.
export function endOperation(id) {
  ownOperations.delete(id);
}

export function isOwnOperation(id) {
  return !!id && ownOperations.has(id);
}

// restoreDiscarded restores, in the server's order, the steers it discarded.
// `sources` resolve an ID to its text, the first that knows it winning.
export function restoreDiscarded(sessionId, ids, ...sources) {
  const byID = new Map();
  for (const source of sources.reverse()) {
    for (const steer of source || []) byID.set(steer.id, steer);
  }
  restoreSteers(sessionId, [...new Set(ids || [])].map((id) => byID.get(id)).filter(Boolean));
}

function restoreSteers(sessionId, steers) {
  let seen = restoredIDs.get(sessionId);
  if (!seen) {
    seen = new Set();
    restoredIDs.set(sessionId, seen);
  }
  const fresh = steers.filter((s) => !s.non_recallable && !seen.has(s.id));
  if (fresh.length === 0) return;
  fresh.forEach((s) => seen.add(s.id));
  setState((state) => {
    const prev = state.composerDrops[sessionId];
    return {
      composerDrops: {
        ...state.composerDrops,
        [sessionId]: {
          id: `restore-${Date.now()}`,
          text: combineQueueText(prev?.text, fresh),
          files: prev?.files || [],
          focus: true,
        },
      },
    };
  });
  const dropped = droppedImageCount(fresh);
  if (dropped > 0) {
    addToast({ sessionId, title: 'Queued images dropped', detail: `${dropped} attached image${dropped > 1 ? 's were' : ' was'} not restored — re-attach if still needed.`, type: 'info' });
  }
}

export function __resetSteerRestoreForTests() {
  restoredIDs.clear();
  ownOperations.clear();
}
