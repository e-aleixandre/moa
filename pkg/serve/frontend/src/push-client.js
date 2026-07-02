// push-client.js — Web Push subscription lifecycle: permission → pushManager →
// server sync. Distinct from notifications.js (in-page toasts/sound/vibration)
// and pwa.js (service-worker registration). The UI observes pushState to render
// the "notifications" control.

import { api } from './api.js';

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

// refreshPushState reconciles the UI with the browser's actual state on load.
export async function refreshPushState() {
  if (!supported()) { setPushState('unsupported'); return; }
  if (Notification.permission === 'denied') { setPushState('denied'); return; }
  if (Notification.permission === 'default') { setPushState('default'); return; }
  try {
    const reg = await navigator.serviceWorker.ready;
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
    const { key } = await api('GET', '/api/push/vapid-public-key');
    const reg = await navigator.serviceWorker.ready;
    let sub = await reg.pushManager.getSubscription();
    if (!sub) {
      sub = await reg.pushManager.subscribe({
        userVisibleOnly: true,
        applicationServerKey: urlBase64ToUint8Array(key),
      });
    }
    // sub.toJSON() → { endpoint, expirationTime, keys:{p256dh,auth} }; the extra
    // field is ignored server-side (pkg/push expects endpoint + keys).
    await api('POST', '/api/push/subscribe', sub.toJSON());
    setPushState('subscribed');
  } catch (e) {
    console.error('[push] enable failed', e);
    await refreshPushState();
  }
}

export async function disablePush() {
  setPushState('busy');
  try {
    const reg = await navigator.serviceWorker.ready;
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
