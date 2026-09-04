// moa service worker.
//
// Lifecycle: minimal install/activate so moa serve is installable as a
// PWA. There is no offline caching — the app is only used over the tailnet
// against a live server.
//
// Web Push: a 'push' handler turns the encrypted payload from
// pkg/push.Dispatcher into a system notification, and 'notificationclick' routes
// the tap to the right session (focusing an open window or opening a new one).

self.addEventListener('install', () => {
  // Activate this worker immediately instead of waiting for old clients to close.
  self.skipWaiting();
});

self.addEventListener('activate', (event) => {
  // Take control of already-open clients so the SW is active without a reload.
  event.waitUntil(self.clients.claim());
});

// Payload shape mirrors pkg/push.Notification: { title, body, session_id, tag }.
self.addEventListener('push', (event) => {
  let data = {};
  try {
    data = event.data ? event.data.json() : {};
  } catch (_) {
    data = {};
  }
  const title = data.title || 'moa';
  event.waitUntil(
    self.registration.showNotification(title, {
      body: data.body || '',
      tag: data.tag || undefined, // coalesce same-session notifications
      icon: '/icon-192.png',
      data: { session_id: data.session_id || '', inbox: !!data.inbox },
    })
  );
});

self.addEventListener('notificationclick', (event) => {
  event.notification.close();
  const rawSessionId = event.notification.data && event.notification.data.session_id;
  const sessionId = typeof rawSessionId === 'string' && /^[A-Za-z0-9_-]{1,128}$/.test(rawSessionId) ? rawSessionId : '';
  const inbox = !sessionId && !!(event.notification.data && event.notification.data.inbox);
  const url = sessionId ? `/?session=${encodeURIComponent(sessionId)}` : inbox ? '/?inbox=1' : '/';

  event.waitUntil((async () => {
    const clients = await self.clients.matchAll({ type: 'window' });
    // Prefer focusing an already-open window and telling it which session to show
    // (no reload, keeps live WS connections).
    for (const client of clients) {
      if (!('focus' in client)) continue;
      let sameOrigin = false;
      try { sameOrigin = new URL(client.url).origin === self.location.origin; } catch (_) { /* ignore malformed client URL */ }
      if (!sameOrigin) continue;
      try {
        await client.focus();
        if (sessionId) {
          if (await requestOpenSession(client, sessionId)) return;
        } else if (inbox) {
          if (await requestOpenInbox(client)) return;
        } else {
          return;
        }
        // The focused client did not acknowledge promptly, usually because an
        // installed iOS PWA was suspended or is restarting. Navigate it to the
        // same deep link a cold start uses instead of losing the notification.
        if ('navigate' in client) {
          const navigated = await client.navigate(url);
          if (navigated) {
            if ('focus' in navigated) await navigated.focus();
            return;
          }
        }
      } catch (_) { /* another controlled client may still be focusable */ }
    }
    // No window open → cold start with the session (or inbox) pinned in the URL.
    if (self.clients.openWindow) await self.clients.openWindow(url);
  })());
});

function requestOpenInbox(client) {
  if (typeof MessageChannel === 'undefined') return Promise.resolve(false);
  const requestId = `${Date.now()}-${Math.random().toString(36).slice(2)}`;
  const channel = new MessageChannel();
  return new Promise((resolve) => {
    const timeout = setTimeout(() => finish(false), 1200);
    const finish = (acknowledged) => {
      clearTimeout(timeout);
      channel.port1.onmessage = null;
      channel.port1.close();
      resolve(acknowledged);
    };
    channel.port1.onmessage = (event) => {
      const data = event.data;
      finish(!!data && data.type === 'open-inbox-ack' && data.requestId === requestId);
    };
    client.postMessage({ type: 'open-inbox', requestId }, [channel.port2]);
  });
}

function requestOpenSession(client, sessionId) {
  if (typeof MessageChannel === 'undefined') return Promise.resolve(false);
  const requestId = `${Date.now()}-${Math.random().toString(36).slice(2)}`;
  const channel = new MessageChannel();
  return new Promise((resolve) => {
    const timeout = setTimeout(() => finish(false), 1200);
    const finish = (acknowledged) => {
      clearTimeout(timeout);
      channel.port1.onmessage = null;
      channel.port1.close();
      resolve(acknowledged);
    };
    channel.port1.onmessage = (event) => {
      const data = event.data;
      finish(!!data && data.type === 'open-session-ack' && data.requestId === requestId);
    };
    client.postMessage({ type: 'open-session', sessionId, requestId }, [channel.port2]);
  });
}
