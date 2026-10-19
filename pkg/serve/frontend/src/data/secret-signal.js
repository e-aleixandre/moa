import { store } from './store.js';
import { isPresent } from './presence.js';
import { addToast, alertAway, removeToast } from './notifications.js';
import { openSession } from './tile-actions.js';
import { closeDrawer } from './drawer.js';

// secret-signal — the global "this session is stopped until you enter a
// password" toast. It is a pure function of the store:
//
//   a session has an open password request, and you are not looking at that
//   session right now, and you have not closed the toast for that request.
//
// There is no "already told" memory. A session blocked on a password stays
// blocked until the value goes in, so the toast is on whenever that is true and
// you are elsewhere, including after you saw the card and walked away. Because
// nothing is consumed, a navigation frame that briefly looks like "you are in
// it" can at worst make the toast blink; it can never lose it. The one memory
// is what you closed with X, per request id: a request replaced by another is a
// new thing to tell about.

const dismissed = new Set(); // request ids closed with X
const alerted = new Set(); // request ids that already made a sound or a system notification
const raised = new Map(); // session id -> { key, toast } of the toast on screen

// cardShown is the presence rule the server already trusts for "this client is
// looking at that conversation" (rendered, history landed, page visible), less
// the phone drawer, which presence does not know covers the conversation.
function cardShown(state, id) {
  // For you lists every password request with its Enter: the toast would
  // only cover the row that already says it.
  if (state.view === 'foryou') return true;
  return isPresent(state, id) && !(state.isMobile && state.drawerOpen);
}

export function reconcileSecretSignal(state = store.get()) {
  const pending = new Set();
  // A request id is known from the first time this client sees it, in any
  // circumstance. Only an id first seen with the list loaded and its session
  // out of view is news worth a sound or a buzz; a reload, a reconnect or a
  // card you were looking at never turns a known id into news again.
  const loading = state.sessionsLoaded === false;
  for (const sess of Object.values(state.sessions)) {
    const ids = (sess.secretIds || []).filter((id) => !dismissed.has(id));
    for (const id of sess.secretIds || []) pending.add(id);
    const key = ids.join(',');
    const on = raised.get(sess.id);
    const shown = cardShown(state, sess.id);
    const news = (sess.secretIds || []).filter((id) => !alerted.has(id));
    for (const id of news) alerted.add(id);
    if (!ids.length || shown) {
      if (on) { removeToast(on.toast); raised.delete(sess.id); }
      continue;
    }
    if (on?.key === key) continue;
    if (on) removeToast(on.toast);
    const title = sess.title || 'Untitled';
    // The id names the session and its requests: raising it again (a frame that
    // looked like presence, then did not) replaces the toast, never doubles it.
    raised.set(sess.id, {
      key,
      toast: addToast({
        id: `secret:${sess.id}:${key}`,
        sessionId: sess.id, title, type: 'attention', sticky: true,
        onOpen: () => enterSecretSession(sess.id),
        detail: 'Stopped until you enter the password',
        action: { label: 'Enter', onClick: () => enterSecretSession(sess.id) },
        onDismiss: () => { for (const id of ids) dismissed.add(id); },
      }),
    });
    if (!loading && news.some((id) => ids.includes(id))) {
      alertAway(title, 'Needs a password', state.soundEnabled);
    }
  }
  // A session that is gone, or whose requests are, owes nothing.
  for (const [sid, on] of raised) {
    if (!state.sessions[sid]?.secretIds?.length) { removeToast(on.toast); raised.delete(sid); }
  }
  for (const set of [dismissed, alerted]) for (const id of set) if (!pending.has(id)) set.delete(id);
}

// Enter shows the card: on a phone the drawer that listed the session would
// otherwise stay over it.
function enterSecretSession(id) {
  closeDrawer();
  openSession(id);
}

export function watchSecretSignal() {
  reconcileSecretSignal();
  const off = store.subscribe(reconcileSecretSignal);
  // A page going to or coming from the background changes what is shown
  // without touching the store.
  const doc = globalThis.document;
  const onVisibility = () => reconcileSecretSignal();
  doc?.addEventListener?.('visibilitychange', onVisibility);
  return () => {
    off();
    doc?.removeEventListener?.('visibilitychange', onVisibility);
    for (const on of raised.values()) removeToast(on.toast);
    raised.clear();
    dismissed.clear();
    alerted.clear();
  };
}
