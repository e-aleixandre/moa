// steer-restore.js — the one rule for putting discarded steers back in the
// composer.
//
// The server owns the queue, so it alone says which steers were discarded: the
// Stop or recall reply (`discarded_steers`), or the `steers_canceled` broadcast.
// Either can arrive first, twice, or not at all, so the text is restored per
// steer ID, at most once, and appended to whatever drop the composer has not
// consumed yet — it never replaces it.

import { setState } from './store.js';
import { addToast } from './notifications.js';
import { combineQueueText, droppedImageCount } from './composer-queue.js';

const restoredIDs = new Map(); // sessionId → Set of steer IDs already restored
const stops = new Map(); // sessionId → { inFlight, unresolved }

// restoreSteers appends the owner's text of `steers` to the session's composer
// drop, skipping task notices and any ID restored before.
export function restoreSteers(sessionId, steers) {
  let seen = restoredIDs.get(sessionId);
  if (!seen) {
    seen = new Set();
    restoredIDs.set(sessionId, seen);
  }
  const fresh = (steers || []).filter((s) => s && !s.non_recallable && !seen.has(s.id));
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

// A Stop of this client is armed from its request until its reply tells what
// was discarded. A failed request stays armed: the server may have stopped
// anyway, and then its broadcast is the only word on the discarded text.
export function beginStop(sessionId) {
  const stop = stops.get(sessionId) || { inFlight: 0, unresolved: false };
  stop.inFlight += 1;
  stops.set(sessionId, stop);
}

export function endStop(sessionId, { answered }) {
  const stop = stops.get(sessionId);
  if (!stop) return;
  stop.inFlight -= 1;
  if (!answered) stop.unresolved = true;
  if (stop.inFlight === 0 && !stop.unresolved) stops.delete(sessionId);
}

// restoreBroadcastDiscards is called with the chips a `steers_canceled`
// broadcast is about to remove. Only a Stop of this client restores them: a
// recall reports through its own reply, and another client's Stop is theirs.
export function restoreBroadcastDiscards(sessionId, steers) {
  const stop = stops.get(sessionId);
  if (!stop) return;
  restoreSteers(sessionId, steers);
  stop.unresolved = false;
  if (stop.inFlight === 0) stops.delete(sessionId);
}

export function __resetSteerRestoreForTests() {
  restoredIDs.clear();
  stops.clear();
}
