// history-hydration.js — per-session authority boundary for cached transcripts

import { store, updateSession } from './store.js';

// An init normally arrives in a single network turn. Waiting 300ms avoids a
// distracting mount/unmount flash while still making a genuinely slow snapshot
// apparent before it feels stalled.
export const HISTORY_HYDRATION_GRACE_MS = 300;
// A resumed delta normally arrives in one fast round trip. Give it a longer
// chance to confirm its cached base before exposing the stale-history tail;
// a full-init fallback explicitly releases this wait below.
export const HISTORY_DELTA_HYDRATION_GRACE_MS = 1500;

const tailTimers = new Map();

function clearTailTimer(id) {
  const timer = tailTimers.get(id);
  if (timer !== undefined) clearTimeout(timer);
  tailTimers.delete(id);
}

function cachedTranscriptMayBeBehind(session) {
  const hasCachedTranscript = (session.messages || []).length > 0;
  if (!hasCachedTranscript) return true;
  if (!session.unseen) return false;
  return (session.historyCacheSeq || 0) < (session.unseenSeq || 0);
}

// A roster entry retains its last rendered messages while its socket is away.
// Mark the brief interval from opening that socket through its init snapshot so
// the stream can distinguish a useful cached view from authoritative history.
export function beginHistoryHydration(id, { deltaResume = false } = {}) {
  const session = store.get().sessions[id];
  if (!session || session.historyPending) return false;
  clearTailTimer(id);
  // since_msg is only a request. A server may reject its base and send a full
  // init, so hiding a possibly missing tail before init confirms delta_base
  // would present stale cached history as current. A successful delta init
  // settles before the longer resume grace period in the usual case.
  const historyTailNeeded = cachedTranscriptMayBeBehind(session);
  updateSession(id, {
    historyPending: true,
    historyStale: false,
    // These are display-only. Keep the acknowledgement cursor below separate:
    // historyHydrated still means exactly "this init was shown".
    historyTailNeeded,
    historyTailReady: false,
    historyHydrated: false,
    readCandidateSeq: 0,
  });
  if (historyTailNeeded) {
    tailTimers.set(id, setTimeout(() => {
      tailTimers.delete(id);
      const current = store.get().sessions[id];
      if (current?.historyPending && current.historyTailNeeded) {
        updateSession(id, { historyTailReady: true });
      }
    }, deltaResume ? HISTORY_DELTA_HYDRATION_GRACE_MS : HISTORY_HYDRATION_GRACE_MS));
  }
  return true;
}

// The init envelope is authoritative about whether the server accepted a
// requested delta base. A confirmed delta makes the retained transcript
// current before its rows are appended; a full fallback releases the normal
// stale-tail presentation immediately rather than waiting out delta's grace.
export function confirmHistoryHydrationInit(id, { deltaBase = false } = {}) {
  const session = store.get().sessions[id];
  if (!session?.historyPending) return false;
  clearTailTimer(id);
  if (deltaBase) {
    updateSession(id, { historyTailNeeded: false, historyTailReady: false });
  } else if (session.historyTailNeeded) {
    updateSession(id, { historyTailReady: true });
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
    // The cache sequence is visual provenance only. readCandidateSeq is reset
    // when hydration begins, so it is nonzero here only for this init.
    historyCacheSeq: shown ? (session.readCandidateSeq || 0) : (session.historyCacheSeq || 0),
    historyHydrated: shown,
  });
  return true;
}
