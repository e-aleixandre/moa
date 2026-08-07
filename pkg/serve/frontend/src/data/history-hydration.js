// history-hydration.js — per-session authority boundary for cached transcripts

import { store, updateSession } from './store.js';

// A roster entry retains its last rendered messages while its socket is away.
// Mark the brief interval from opening that socket through its init snapshot so
// the stream can distinguish a useful cached view from authoritative history.
export function beginHistoryHydration(id) {
  const session = store.get().sessions[id];
  if (!session || session.historyPending) return false;
  updateSession(id, {
    historyPending: true,
    historyStale: false,
    historyHydrated: false,
    historyShownGen: 0,
    historyShownInstance: '',
  });
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
  shownGeneration = 0,
  shownInstance = '',
} = {}) {
  const session = store.get().sessions[id];
  if (!session) return false;
  updateSession(id, {
    historyPending: false,
    historyStale: stale,
    // Only init's authoritative snapshot may make an occurrence eligible for
    // acknowledgement. Closing a pending socket is not evidence the cached
    // transcript was shown authoritatively.
    historyHydrated: shown,
    // These must come from this init snapshot, never a newer roster response.
    historyShownGen: shown ? shownGeneration : 0,
    historyShownInstance: shown ? shownInstance : '',
  });
  return true;
}
