// Presence: tells the server which conversation this client is showing right
// now, so a question you are already looking at does not also buzz your phone.
// A session counts only when the screen is actually rendering it, its history
// has been shown (the init landed) and the page is visible — a connected
// socket proves none of that. The server lets a report expire (~45 s), so a
// shown session keeps renewing it and a client that vanishes without a word
// stops counting by itself.

import { allSessionIds } from './tileTree.js';
import { focusedSessionId } from './selectors.js';

const WS_OPEN = 1;
export const PRESENCE_RENEW_MS = 15000;

// The sessions whose conversation is on screen. Mobile shows the active one;
// the desktop grid shows every tile, the tasks screen none, and otherwise the
// focused conversation only — the other tiles of the tree stay connected but
// are not being read.
export function shownSessionIds(state) {
  if (state.isMobile) return state.activeSession ? [state.activeSession] : [];
  if (state.view === 'grid') return allSessionIds(state.tileTree);
  if (state.view === 'tasks') return [];
  const id = focusedSessionId(state);
  return id ? [id] : [];
}

export function isPresent(state, sessionId, doc = globalThis.document) {
  if (doc?.visibilityState !== 'visible') return false;
  if (!state.sessions[sessionId]?.historyHydrated) return false;
  return shownSessionIds(state).includes(sessionId);
}

export function presenceFrame(visible) {
  return JSON.stringify({ type: 'presence', visible });
}

// Reports each connection's presence whenever it changes, and renews the ones
// that are present. `connections()` yields [sessionId, ws] pairs; `subscribe`
// is the store's. Returns a function that stops it.
export function watchPresence({ connections, getState, subscribe, doc = globalThis.document }) {
  if (!doc) return () => {};
  const last = new WeakMap(); // ws → what the server was last told
  const sync = (renew) => {
    const state = getState();
    for (const [id, ws] of connections()) {
      if (!ws || ws.readyState !== WS_OPEN) continue;
      const visible = isPresent(state, id, doc);
      if (last.get(ws) === visible && !(renew && visible)) continue;
      ws.send(presenceFrame(visible));
      last.set(ws, visible);
    }
  };
  const timer = setInterval(() => sync(true), PRESENCE_RENEW_MS);
  const unsubscribe = subscribe(() => sync(false));
  const onVisibility = () => sync(false);
  doc.addEventListener('visibilitychange', onVisibility);
  return () => {
    clearInterval(timer);
    unsubscribe();
    doc.removeEventListener('visibilitychange', onVisibility);
  };
}
