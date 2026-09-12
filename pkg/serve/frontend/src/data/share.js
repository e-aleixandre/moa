// share.js — what happens after something is shared INTO moa.
//
// The service worker answers the OS share POST, retains the payload and hands
// the app the id (sw.js): warm through postMessage, cold through ?share= on the
// 303 it redirects to. Both land here, and here is where the product decision
// lives: moa ASKS which session the share belongs to. It is never sent
// automatically and never guessed from what happens to be on screen — the owner
// picks the conversation, writes what to do with the file, and sends it himself.
//
// The picker's state is an ephemeral global slice (store.share), the same shape
// as the artifacts drawer and the session dossier: it is a place you are
// looking, not a preference. `store.composerDrops` is the handoff to the
// composer of the chosen session, keyed by session id because the composer is
// mounted per session and may not exist at the instant the choice is made.

import { store, setState, SHARE_CLOSED } from './store.js';
import { addToast } from './notifications.js';
import { openSession } from './tile-actions.js';
import { resumeSession } from './session-actions.js';
import { readyRegistration } from './push-client.js';
import {
  discardShare, isShareID, readShare, shareComposerText,
} from './share-target.js';

// Share ids this client has already taken responsibility for. A share can
// reach us twice — the worker messages a live window AND the browser navigates
// that window to ?share=<id> — and the second arrival must not re-open a
// picker, nor report "nothing shared" for a payload the first one consumed.
const handled = new Set();

export function __resetSharesForTests() {
  handled.clear();
  setState({ share: SHARE_CLOSED, composerDrops: {} });
}

// openShare takes ownership of a retained share and opens the picker on it.
// Returns true when this client accepted the share (which is what the service
// worker's ack means), false when the id is unusable or already handled.
export async function openShare(shareId, { read = readShare } = {}) {
  if (!isShareID(shareId) || handled.has(shareId)) return false;
  handled.add(shareId);
  setState({ share: { ...SHARE_CLOSED, id: shareId, status: 'loading' } });
  let share = null;
  try {
    share = await read(shareId);
  } catch (_) {
    share = null;
  }
  // A superseded share: the owner shared twice and the second one owns the
  // picker now. Leave it alone.
  if (store.get().share.id !== shareId) return true;
  if (!share || (share.files.length === 0 && !shareComposerText(share))) {
    // Nothing to place. The alternative — an empty picker asking which session
    // to send nothing to — would be the app inventing a payload it does not
    // have.
    setState({ share: SHARE_CLOSED });
    addToast({
      title: 'Nothing arrived from the share',
      detail: 'The shared item was no longer available. Share it again from the other app.',
      type: 'attention',
    });
    return true;
  }
  setState({
    share: {
      id: shareId,
      status: 'ready',
      title: share.title,
      text: share.text,
      url: share.url,
      files: share.files,
    },
  });
  return true;
}

// chooseShareSession routes the open share to one session: bring it into view
// (resuming it first when it is closed, like every other way of opening a saved
// session), leave the payload for its composer, and forget the cached copy —
// the files are in memory from here on.
export async function chooseShareSession(sessionId, { resume = resumeSession, select = openSession } = {}) {
  const share = store.get().share;
  if (share.status !== 'ready' || !sessionId) return false;
  const session = store.get().sessions[sessionId];
  if (!session) return false;
  try {
    if (session.state === 'saved') await resume(sessionId);
    else select(sessionId);
  } catch (e) {
    addToast({ title: 'Could not open that session', detail: String(e?.message || e), type: 'error' });
    return false;
  }
  setState((state) => ({
    composerDrops: {
      ...state.composerDrops,
      [sessionId]: {
        id: share.id,
        text: shareComposerText(share),
        files: share.files,
      },
    },
    share: SHARE_CLOSED,
  }));
  // Consumed: the payload now lives in the composer's own state, so the cache
  // copy is dead weight (and it can be a 32 MB video).
  discardShare(share.id);
  return true;
}

// dismissShare closes the picker WITHOUT placing the share, and drops the
// retained copy. Nothing in the app can reach a share once its picker is gone,
// so keeping the bytes around would only leave unreachable data on the device
// until the worker's sweep.
export function dismissShare() {
  const share = store.get().share;
  if (share.status === 'idle') return;
  setState({ share: SHARE_CLOSED });
  if (share.id) discardShare(share.id);
}

// consumeComposerDrop is called by the composer that has just taken a drop, so
// the same payload is never placed twice (a re-render, or the session being
// re-opened later).
export function consumeComposerDrop(sessionId) {
  const drops = store.get().composerDrops;
  if (!drops[sessionId]) return;
  const next = { ...drops };
  delete next[sessionId];
  setState({ composerDrops: next });
}

// installShareNavigation receives the warm handoff from the service worker.
// Same handshake as a warm notification tap (push-navigation.js): postMessage
// with a MessageChannel, acknowledged by type+requestId. The worker needs no
// openWindow fallback here — its 303 redirect carries ?share=<id>, which a cold
// start reads on its own.
//
// It also makes sure the worker EXISTS. Until now the only thing that ever
// registered /sw.js was enabling push, so on a profile that had never granted
// notifications there was no worker to intercept the share POST at all --
// measured: navigator.serviceWorker.ready never resolved and the POST went
// straight to the server as a 404. Sharing must not depend on having said yes
// to notifications.
export function installShareNavigation({
  serviceWorker = typeof navigator === 'undefined' ? null : navigator.serviceWorker,
  accept = openShare,
  register = readyRegistration,
} = {}) {
  if (!serviceWorker) return () => {};
  // Fire and forget: a failure here leaves the app exactly as it was, minus
  // the ability to receive a share.
  Promise.resolve().then(register).catch(() => {});
  const onMessage = (event) => {
    const data = event?.data;
    if (!data || data.type !== 'open-share' || !isShareID(data.shareId)) return;
    const reply = event.ports?.[0];
    Promise.resolve(accept(data.shareId))
      .then((accepted) => {
        // Acknowledge only what we really took: an unacknowledged share still
        // reaches the app through the redirect.
        if (accepted && reply && typeof reply.postMessage === 'function') {
          reply.postMessage({ type: 'open-share-ack', requestId: data.requestId });
        }
      })
      .catch(() => {});
  };
  serviceWorker.addEventListener('message', onMessage);
  return () => serviceWorker.removeEventListener('message', onMessage);
}
