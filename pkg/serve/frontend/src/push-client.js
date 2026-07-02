// push-client.js — Web Push subscription lifecycle: permission → pushManager →
// server sync. Distinct from notifications.js (in-page toasts/sound/vibration)
// and pwa.js (service-worker registration). The UI observes pushState to render
// the "notifications" control.

import { api } from './api.js';
import { addToast } from './notifications.js';

// Push state surfaced to the UI:
//   'unsupported' — no SW / PushManager / Notification (e.g. iOS not installed)
//   'default'     — supported, not subscribed yet
//   'denied'      — permission denied (must re-enable in OS/browser settings)
//   'subscribed'  — permission granted and a subscription is registered
//   'busy'        — a subscribe/unsubscribe call is in flight
let pushState = 'default';
const listeners = new Set();

export function getPushState() { return pushState; }

export function subscribePushState(fn) {
  listeners.add(fn);
  fn(pushState);
  return () => listeners.delete(fn);
}

function setPushState(s) {
  pushState = s;
  listeners.forEach((fn) => fn(s));
}

function supported() {
  return 'serviceWorker' in navigator && 'PushManager' in window && 'Notification' in window;
}

// VAPID public key arrives base64url-encoded; pushManager.subscribe needs bytes.
function urlBase64ToUint8Array(base64) {
  const padding = '='.repeat((4 - (base64.length % 4)) % 4);
  const b64 = (base64 + padding).replace(/-/g, '+').replace(/_/g, '/');
  const raw = atob(b64);
  const out = new Uint8Array(raw.length);
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
  return out;
}

// withTimeout rejects if a step doesn't settle in time, so the enable flow can
// never hang silently on 'busy' (on iOS the service-worker or subscribe step can
// stay pending forever). The label names the failing step in the surfaced error.
function withTimeout(promise, ms, label) {
  return Promise.race([
    promise,
    new Promise((_, reject) => setTimeout(() => reject(new Error(`timeout en «${label}»`)), ms)),
  ]);
}

// readyRegistration returns an active service-worker registration, registering
// it on demand. We avoid navigator.serviceWorker.ready because on iOS it can
// stay pending forever when the page isn't yet controlled by a worker. Instead
// we register (or reuse) and wait for the worker to reach 'activated'.
async function readyRegistration() {
  let reg = await navigator.serviceWorker.getRegistration('/');
  if (!reg) reg = await navigator.serviceWorker.register('/sw.js');
  if (reg.active) return reg;
  const worker = reg.installing || reg.waiting;
  if (!worker) throw new Error('el service worker no arrancó');
  await new Promise((resolve, reject) => {
    worker.addEventListener('statechange', () => {
      if (worker.state === 'activated') resolve();
      else if (worker.state === 'redundant') reject(new Error('el service worker falló al instalar'));
    });
  });
  return reg;
}

function bufToBase64Url(buf) {
  const bytes = new Uint8Array(buf);
  let bin = '';
  for (let i = 0; i < bytes.length; i++) bin += String.fromCharCode(bytes[i]);
  return btoa(bin).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

// Build the server payload explicitly from the subscription keys. Safer than
// sub.toJSON(), which on iOS Safari has been observed to omit `keys` — the server
// would then 400 and the browser would still hold a dangling subscription.
function subscriptionPayload(sub) {
  const p256dh = sub.getKey && sub.getKey('p256dh');
  const auth = sub.getKey && sub.getKey('auth');
  if (p256dh && auth) {
    return {
      endpoint: sub.endpoint,
      keys: { p256dh: bufToBase64Url(p256dh), auth: bufToBase64Url(auth) },
    };
  }
  return sub.toJSON();
}

// refreshPushState reconciles the UI with the browser's actual state on load.
export async function refreshPushState() {
  if (!supported()) { setPushState('unsupported'); return; }
  if (Notification.permission === 'denied') { setPushState('denied'); return; }
  if (Notification.permission === 'default') { setPushState('default'); return; }
  try {
    const reg = await readyRegistration();
    const sub = await reg.pushManager.getSubscription();
    setPushState(sub ? 'subscribed' : 'default');
  } catch (_) {
    setPushState('default');
  }
}

// enablePush must run from a user gesture (iOS requires it for both the
// permission prompt and pushManager.subscribe).
export async function enablePush() {
  if (!supported()) return;
  setPushState('busy');
  try {
    const perm = await Notification.requestPermission();
    if (perm !== 'granted') {
      setPushState(perm === 'denied' ? 'denied' : 'default');
      return;
    }
    const { key } = await withTimeout(api('GET', '/api/push/vapid-public-key'), 10000, 'clave VAPID');
    const reg = await withTimeout(readyRegistration(), 10000, 'service worker');
    let sub = await withTimeout(reg.pushManager.getSubscription(), 10000, 'suscripción actual');
    if (!sub) {
      sub = await withTimeout(reg.pushManager.subscribe({
        userVisibleOnly: true,
        applicationServerKey: urlBase64ToUint8Array(key),
      }), 20000, 'suscribir');
    }
    await withTimeout(api('POST', '/api/push/subscribe', subscriptionPayload(sub)), 10000, 'guardar en servidor');
    setPushState('subscribed');
  } catch (e) {
    // Surface the failure instead of silently reporting success: a browser-side
    // subscription can exist even when the server never stored it.
    console.error('[push] enable failed', e);
    addToast({ title: 'No se pudieron activar las notificaciones', detail: String((e && e.message) || e), type: 'attention' });
    setPushState('default');
  }
}

export async function disablePush() {
  setPushState('busy');
  try {
    const reg = await readyRegistration();
    const sub = await reg.pushManager.getSubscription();
    if (sub) {
      // Drop it server-side first, then locally. Ignore server errors so a stale
      // endpoint can still be unsubscribed in the browser.
      await api('POST', '/api/push/unsubscribe', { endpoint: sub.endpoint }).catch(() => {});
      await sub.unsubscribe();
    }
  } catch (e) {
    console.error('[push] disable failed', e);
  }
  await refreshPushState();
}
