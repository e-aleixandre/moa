// moa service worker.
//
// Lifecycle: minimal install/activate so moa serve is installable as a
// PWA. There is no offline caching — the app is only used over the tailnet
// against a live server.
//
// Web Push: a 'push' handler turns the encrypted payload from
// pkg/push.Dispatcher into a system notification, and 'notificationclick' routes
// the tap to the right session (focusing an open window or opening a new one).
//
// Share target: a 'fetch' handler answers the POST the OS share sheet makes to
// /share-target (declared in manifest.webmanifest), retains what was shared in
// the Cache Storage and hands the app a redirect it can pick the share up from.

// Where a share is retained between the POST and the moment the app consumes it.
// The same two constants live in data/share-target.js, which is the reader —
// the worker is copied verbatim by the build and shares no module with the app.
const SHARE_CACHE = 'moa-share-v1';
const SHARE_PREFIX = '/__moa-share__/';
// An unconsumed share is swept after this long. Nothing else ever deletes a
// share the owner neither routed nor dismissed, and a 32 MB video must not
// live in the cache forever because a share sheet was opened by accident.
const SHARE_TTL_MS = 24 * 60 * 60 * 1000;

self.addEventListener('install', () => {
  // Activate this worker immediately instead of waiting for old clients to close.
  self.skipWaiting();
});

self.addEventListener('activate', (event) => {
  // Take control of already-open clients so the SW is active without a reload.
  event.waitUntil(self.clients.claim());
  // A share the app never consumed (the PWA was killed before it could) is
  // swept here rather than kept: activate is the one moment we are guaranteed
  // to run without a user waiting on the answer.
  event.waitUntil(sweepShares(''));
});

// Share target. The OS posts multipart/form-data to the manifest's `action`;
// this worker is what makes that URL exist, since there is no server route for
// it (files never leave the device until the owner sends the message).
//
// The answer is a 303 redirect, as the Web Share Target spec asks: a POST
// response left in the history would be re-submitted by a refresh, and the
// shared files are already retained by then, so the reload would deliver them
// twice.
self.addEventListener('fetch', (event) => {
  const request = event.request;
  // Everything else is left to the network untouched: moa is used live against
  // its server over the tailnet and this worker does no offline caching.
  if (request.method !== 'POST') return;
  let url;
  try {
    url = new URL(request.url);
  } catch (_) {
    return;
  }
  if (url.origin !== self.location.origin || url.pathname !== '/share-target') return;
  event.respondWith(receiveShare(event, request));
});

async function receiveShare(event, request) {
  const shareId = newShareId();
  let stored = false;
  try {
    stored = await storeShare(shareId, await request.formData());
  } catch (_) {
    stored = false;
  }
  if (!stored) {
    // Nothing usable came through. Land the owner in the app rather than on a
    // dead POST, and do not advertise a share that does not exist.
    return Response.redirect(new URL('/', self.location.origin).href, 303);
  }
  // Tell a window that is already alive, so the picker is up before the
  // navigation below even lands. Same handshake as a warm notification tap
  // (requestOpenSession): postMessage + MessageChannel, acknowledged or timed
  // out. There is no openWindow fallback here because the redirect IS the
  // fallback — it opens the app with the share pinned in the URL.
  event.waitUntil(notifyShare(shareId));
  event.waitUntil(sweepShares(shareId));
  return Response.redirect(
    new URL(`/?share=${encodeURIComponent(shareId)}`, self.location.origin).href,
    303
  );
}

// The id starts with the time it was minted, in base 36, so a sweep can date a
// share even when the interrupted write left no metadata behind.
function newShareId() {
  return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`;
}

function shareMintedAt(shareId) {
  const stamp = Number.parseInt(String(shareId).split('-')[0], 36);
  return Number.isFinite(stamp) ? stamp : 0;
}

// storeShare retains the shared form. The metadata entry is written LAST and is
// what marks a share as complete: a reader that finds it can trust that every
// file it lists is already in the cache.
async function storeShare(shareId, form) {
  const cache = await caches.open(SHARE_CACHE);
  const base = `${SHARE_PREFIX}${shareId}`;
  const files = [];
  let index = 0;
  for (const value of form.getAll('files')) {
    // No format filter, deliberately: the composer accepts anything the phone
    // hands over (Composer.jsx ATTACH_ACCEPT) and the server validates size and
    // count, never type.
    if (!value || typeof value === 'string') continue;
    const key = `${base}/file/${index}`;
    await cache.put(new Request(key), new Response(value, {
      headers: { 'content-type': value.type || 'application/octet-stream' },
    }));
    files.push({
      key,
      name: value.name || `shared-${index + 1}`,
      mime: value.type || '',
      size: value.size || 0,
    });
    index++;
  }
  const title = formText(form, 'title');
  const text = formText(form, 'text');
  const url = formText(form, 'url');
  if (files.length === 0 && !title && !text && !url) return false;
  await cache.put(new Request(`${base}/meta`), new Response(
    JSON.stringify({ id: shareId, at: Date.now(), title, text, url, files }),
    { headers: { 'content-type': 'application/json' } }
  ));
  return true;
}

function formText(form, field) {
  const value = form.get(field);
  return typeof value === 'string' ? value : '';
}

// sweepShares removes every retained share older than the TTL, keeping the one
// that has just arrived whatever its age says.
async function sweepShares(keepId) {
  let cache;
  try {
    cache = await caches.open(SHARE_CACHE);
  } catch (_) {
    return;
  }
  const cutoff = Date.now() - SHARE_TTL_MS;
  const requests = await cache.keys();
  for (const request of requests) {
    let pathname = '';
    try { pathname = new URL(request.url).pathname; } catch (_) { continue; }
    if (!pathname.startsWith(SHARE_PREFIX)) continue;
    const shareId = pathname.slice(SHARE_PREFIX.length).split('/')[0];
    if (!shareId || shareId === keepId) continue;
    if (shareMintedAt(shareId) > cutoff) continue;
    await cache.delete(request);
  }
}

function notifyShare(shareId) {
  return (async () => {
    const clients = await self.clients.matchAll({ type: 'window' });
    for (const client of clients) {
      let sameOrigin = false;
      try { sameOrigin = new URL(client.url).origin === self.location.origin; } catch (_) { continue; }
      if (!sameOrigin) continue;
      if (await requestOpenShare(client, shareId)) return;
    }
  })().catch(() => { /* the redirect still carries the share */ });
}

function requestOpenShare(client, shareId) {
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
      finish(!!data && data.type === 'open-share-ack' && data.requestId === requestId);
    };
    client.postMessage({ type: 'open-share', shareId, requestId }, [channel.port2]);
  });
}

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
