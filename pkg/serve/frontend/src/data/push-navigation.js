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
