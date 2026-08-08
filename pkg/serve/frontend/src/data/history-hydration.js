// history-hydration.js — per-session authority boundary for cached transcripts

import { store, updateSession } from './store.js';

// An init normally arrives in a single network turn. Waiting 300ms avoids a
// distracting mount/unmount flash while still making a genuinely slow snapshot
// apparent before it feels stalled.
export const HISTORY_HYDRATION_GRACE_MS = 300;

const tailTimers = new Map();

function clearTailTimer(id) {
  const timer = tailTimers.get(id);
  if (timer !== undefined) clearTimeout(timer);
  tailTimers.delete(id);
}

function cachedTranscriptMayBeBehind(session) {
  const hasCachedTranscript = (session.messages || []).length > 0;
  if (!hasCachedTranscript) return true;

  const currentInstance = session.serverInstance || '';
  const cachedInstance = session.historyCacheInstance || '';
  if (currentInstance && cachedInstance && currentInstance !== cachedInstance) return true;

  if (!session.unseen) return false;
  const unseenInstance = session.serverUnseenInstance || '';
  // A generation is meaningful only within a server instance. The current
  // roster instance cannot prove an occurrence with missing provenance belongs
  // to this cache, even when their generation numbers happen to be equal.
  // Visibility therefore fails open here; acknowledgement remains separately
  // fenced on the authoritative init below.
  if (!cachedInstance || !(session.unseenGen || 0)) return true;
  if (!unseenInstance || unseenInstance !== cachedInstance) return true;
  return (session.historyCacheGen || 0) < session.unseenGen;
}

// A roster entry retains its last rendered messages while its socket is away.
// Mark the brief interval from opening that socket through its init snapshot so
// the stream can distinguish a useful cached view from authoritative history.
export function beginHistoryHydration(id, { deltaResume: _deltaResume = false } = {}) {
  const session = store.get().sessions[id];
  if (!session || session.historyPending) return false;
  clearTailTimer(id);
  // since_msg is only a request. A server may reject its base and send a full
  // init, so hiding a possibly missing tail before init confirms delta_base
  // would present stale cached history as current. A successful delta init
  // settles before this grace period in the usual case and clears the tail.
  const historyTailNeeded = cachedTranscriptMayBeBehind(session);
  updateSession(id, {
    historyPending: true,
    historyStale: false,
    // These are display-only. Keep the acknowledgement fence below separate:
    // historyHydrated/historyShown* still mean exactly "this init was shown".
    historyTailNeeded,
    historyTailReady: false,
    historyHydrated: false,
    historyAckProven: false,
    historyShownGen: 0,
    historyShownInstance: '',
  });
  if (historyTailNeeded) {
    tailTimers.set(id, setTimeout(() => {
      tailTimers.delete(id);
      const current = store.get().sessions[id];
      if (current?.historyPending && current.historyTailNeeded) {
        updateSession(id, { historyTailReady: true });
      }
    }, HISTORY_HYDRATION_GRACE_MS));
  }
  return true;
}

// An init snapshot clears the boundary. A failed attempt leaves an explicit
// stale boundary in its place, because cached history is not authority to
// dismiss attention. The caller decides whether it is safe to acknowledge
// attention; this module only owns presentation state and has no network
// dependency.
export function finishHistoryHydration(id, {
  stale = false,
  shown = false,
  ackProven = false,
  shownGeneration = 0,
  shownInstance = '',
} = {}) {
  // A session can be deleted while its grace timer is pending. The store entry
  // is already gone by the time syncConnections settles that socket, but its
  // timer must still be released before the absent-session early return.
  clearTailTimer(id);
  const session = store.get().sessions[id];
  if (!session) return false;
  updateSession(id, {
    historyPending: false,
    historyStale: stale,
    historyTailNeeded: false,
    historyTailReady: false,
    // Cache provenance is deliberately distinct from historyShown*. The latter
    // is reset for every init to protect acknowledgement; this only tells the
    // visual tail whether retained rows could belong to another occurrence.
    historyCacheGen: shown ? shownGeneration : (session.historyCacheGen || 0),
    historyCacheInstance: shown
      ? (shownInstance || session.serverInstance || session.historyCacheInstance || '')
      : (session.historyCacheInstance || ''),
    // Only init's authoritative snapshot may make an occurrence eligible for
    // acknowledgement. Closing a pending socket is not evidence the cached
    // transcript was shown authoritatively.
    historyHydrated: shown,
    // This is deliberately stricter than historyHydrated: an unproven or
    // unbounded init is still rendered, but can never clear attention.
    historyAckProven: shown && ackProven,
    // These must come from this init snapshot, never a newer roster response.
    historyShownGen: shown ? shownGeneration : 0,
    historyShownInstance: shown ? shownInstance : '',
  });
  return true;
}
