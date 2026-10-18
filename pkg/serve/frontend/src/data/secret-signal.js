import { store } from './store.js';
import { isPresent } from './presence.js';
import { addToast, alertAway, removeToast } from './notifications.js';
import { openSession } from './tile-actions.js';
import { closeDrawer } from './drawer.js';

// secret-signal — the global "this session is stopped until you enter a
// password" toast, derived from the store and from nothing else.
//
// A toast is a function of two things: which requests each session has open
// (`secretIds`, kept authoritative by the roster poll and by the socket) and
// which session you are looking at. Deriving it, rather than raising and
// clearing it from each event, is what gives it one lifecycle: a request that
// is answered, cancelled or deleted with its session, or a session you walk
// into by any route, changes the store, and the toast follows.
//
// Announcing is by request id, not by count: a request replaced by another
// between two polls is still "one open", but it is a new thing to tell about.

const announced = new Map(); // session id -> { ids: Set of announced request ids, toast: toast id }

export function reconcileSecretSignal(state = store.get()) {
  for (const [sid, entry] of announced) {
    if (!state.sessions[sid]?.secretIds?.length) {
      removeToast(entry.toast);
      announced.delete(sid);
    }
  }
  for (const sess of Object.values(state.sessions)) {
    const ids = sess.secretIds || [];
    if (!ids.length) continue;
    const entry = announced.get(sess.id) || { ids: new Set(), toast: null };
    const fresh = ids.some((id) => !entry.ids.has(id));
    entry.ids = new Set(ids);
    announced.set(sess.id, entry);
    if (cardShown(state, sess.id)) {
      // The card has really been in front of you, and what was shown is not
      // owed again once you leave. Selected is not shown: behind Tasks, under
      // the phone drawer or in a background page the request still needs telling.
      removeToast(entry.toast);
      entry.toast = null;
    } else if (fresh) {
      removeToast(entry.toast);
      const title = sess.title || 'Untitled';
      entry.toast = addToast({
        sessionId: sess.id, title, type: 'attention', sticky: true,
        detail: 'Stopped until you enter the password',
        action: { label: 'Enter', onClick: () => enterSecretSession(sess.id) },
      });
      alertAway(title, 'Needs a password', state.soundEnabled);
    }
  }
}

// cardShown is the presence rule the server already trusts for "this client is
// looking at that conversation" (rendered, history landed, page visible), less
// the phone drawer, which presence does not know covers the conversation.
function cardShown(state, id) {
  return isPresent(state, id) && !(state.isMobile && state.drawerOpen);
}

// Enter shows the card: on a phone the drawer that listed the session would
// otherwise stay over it.
function enterSecretSession(id) {
  closeDrawer();
  openSession(id);
}

export function watchSecretSignal() {
  let lastSessions = null;
  let lastView = null;
  const run = (state) => {
    const view = [state.activeSession, state.tileTree, state.isMobile, state.view, state.inboxOpen, state.drawerOpen, state.sessionsLoaded, state.focusedTile];
    if (state.sessions === lastSessions && view.every((v, i) => Object.is(v, lastView[i]))) return;
    lastSessions = state.sessions;
    lastView = view;
    reconcileSecretSignal(state);
  };
  run(store.get());
  const off = store.subscribe(run);
  // A page going to or coming from the background changes what is shown
  // without touching the store.
  const doc = globalThis.document;
  const onVisibility = () => reconcileSecretSignal();
  doc?.addEventListener?.('visibilitychange', onVisibility);
  return () => {
    off();
    doc?.removeEventListener?.('visibilitychange', onVisibility);
    for (const entry of announced.values()) removeToast(entry.toast);
    announced.clear();
  };
}
