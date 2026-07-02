// moa service worker.
//
// Lifecycle (Fase 1): minimal install/activate so moa serve is installable as a
// PWA. There is no offline caching — the app is only used over the tailnet
// against a live server.
//
// Web Push (Fase 3): a 'push' handler turns the encrypted payload from
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
      data: { session_id: data.session_id || '' },
    })
  );
});

self.addEventListener('notificationclick', (event) => {
  event.notification.close();
  const sessionId = (event.notification.data && event.notification.data.session_id) || '';
  const url = sessionId ? `/?session=${sessionId}` : '/';

  event.waitUntil((async () => {
    const clients = await self.clients.matchAll({ type: 'window', includeUncontrolled: true });
    // Prefer focusing an already-open window and telling it which session to show
    // (no reload, keeps live WS connections).
    for (const client of clients) {
      if ('focus' in client) {
        await client.focus();
        if (sessionId) client.postMessage({ type: 'open-session', sessionId });
        return;
      }
    }
    // No window open → cold start with the session pinned in the URL.
    if (self.clients.openWindow) await self.clients.openWindow(url);
  })());
});
