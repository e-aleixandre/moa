import { store } from './store.js';
import { openSession } from './tile-actions.js';
import { loadSessions } from './session-actions.js';
import { openInbox } from './events.js';

export function isPushSessionID(value) {
  return typeof value === 'string' && /^[A-Za-z0-9_-]{1,128}$/.test(value);
}

// installOpenSessionNavigation receives warm notification taps from the service
// worker. A tap can arrive before the initial session fetch finishes, so the
// latest ID is retained until the store says that list is authoritative. A
// loaded-but-stale roster gets one refresh/retry before we withhold the ACK and
// let the service worker's deep-link fallback take over.
export function installOpenSessionNavigation({
  serviceWorker = typeof navigator === 'undefined' ? null : navigator.serviceWorker,
  selectSession = openSession,
  refreshSessions = loadSessions,
} = {}) {
  if (!serviceWorker) return () => {};

  let pending = null;
  const acknowledge = (request) => {
    if (request.reply && typeof request.reply.postMessage === 'function') {
      request.reply.postMessage({ type: 'open-session-ack', requestId: request.requestId });
    }
  };
  const selectPending = () => {
    if (!pending || !store.get().sessionsLoaded) return;
    const request = pending;
    if (request.refreshing || request.exhausted) return;

    try {
      if (selectSession(request.sessionId)) {
        pending = null;
        acknowledge(request);
        return;
      }
    } catch (_) {
      // Treat a selection failure like a stale list. The worker must not be
      // told the tap succeeded until the target is actually open.
    }

    if (request.refreshed) {
      request.exhausted = true;
      return; // no ACK: the service worker will navigate to its deep link
    }
    request.refreshing = true;
    Promise.resolve(refreshSessions())
      .catch(() => {})
      .then(() => {
        if (pending !== request) return;
        request.refreshing = false;
        request.refreshed = true;
        selectPending();
      });
  };
  const onMessage = (event) => {
    const data = event?.data;
    if (data?.type === 'open-inbox') {
      openInbox();
      if (event.ports?.[0] && typeof event.ports[0].postMessage === 'function') {
        event.ports[0].postMessage({ type: 'open-inbox-ack', requestId: data.requestId });
      }
      return;
    }
    if (!data || data.type !== 'open-session' || !isPushSessionID(data.sessionId)) return;
    pending = {
      sessionId: data.sessionId,
      requestId: data.requestId,
      reply: event.ports?.[0],
      refreshed: false,
      refreshing: false,
      exhausted: false,
    };
    selectPending();
  };

  serviceWorker.addEventListener('message', onMessage);
  const unsubscribe = store.subscribe(selectPending);
  return () => {
    serviceWorker.removeEventListener('message', onMessage);
    unsubscribe();
  };
}

// openSessionInPlace opens a session in the page that is already loaded, so a
// notification tap in the native app does not reload it (and lose a draft).
// It waits for the authoritative first session list, refreshes a stale one
// once, and resolves false when the session cannot be opened: the caller then
// falls back to loading the ?session= deep link.
export function openSessionInPlace(sessionId, {
  selectSession = openSession,
  refreshSessions = loadSessions,
  timeoutMs = 10000,
} = {}) {
  if (!isPushSessionID(sessionId)) return Promise.resolve(false);
  const select = () => {
    try {
      return !!selectSession(sessionId);
    } catch (_) {
      return false;
    }
  };
  const loaded = new Promise((resolve) => {
    if (store.get().sessionsLoaded) return resolve(true);
    let unsubscribe = () => {};
    const timer = setTimeout(() => { unsubscribe(); resolve(false); }, timeoutMs);
    unsubscribe = store.subscribe(() => {
      if (!store.get().sessionsLoaded) return;
      clearTimeout(timer);
      unsubscribe();
      resolve(true);
    });
  });
  return loaded.then(async (ready) => {
    if (!ready) return false;
    if (select()) return true;
    try {
      await refreshSessions();
    } catch (_) {
      return false;
    }
    return select();
  });
}

// installNativeNavigation exposes the in-place navigation to the iOS container
// as window.MoaNavigation. Additive: an older container never calls it, and a
// container that finds it missing loads the deep link as before.
export function installNativeNavigation(target = globalThis) {
  if (!target) return () => {};
  const api = Object.freeze({
    version: 1,
    openSession: (sessionId) => openSessionInPlace(sessionId),
    openInbox: () => {
      openInbox();
      return true;
    },
  });
  target.MoaNavigation = api;
  return () => {
    if (target.MoaNavigation === api) delete target.MoaNavigation;
  };
}
