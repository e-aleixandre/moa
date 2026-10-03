// push-client.js — Web Push subscription lifecycle: permission → pushManager →
// server sync. Distinct from notifications.js (in-page toasts/sound/vibration).
// The UI observes pushState to render the "notifications" control.
//
// /next does NOT register its own service worker. Push is handled
// by the ROOT service worker at /sw.js (scope /), which already exists in
// pkg/serve/static/. A notification opens the old frontend until the cutover —
// accepted.

import { api } from './api.js';
import { addToast } from './notifications.js';
import { isPresent, shownSessionIds } from './presence.js';
import { hasBlockingOverlay, subscribeOverlays } from './overlays.js';
import { sessionPanelView } from './session-panel.js';
import { store } from './store.js';

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

// The paired iOS app injects window.MoaNativePush. When present it replaces
// Web Push entirely: APNs through the relay, decrypted by the app's extension.
function nativePush() {
  const n = globalThis.window?.MoaNativePush;
  return n && typeof n.enable === 'function' ? n : null;
}

const NATIVE_ERRORS = {
  not_paired: 'This app is not paired with a server.',
  denied: 'Notifications are blocked in iOS settings.',
  relay_mismatch: 'The server uses a different push relay than this app.',
  unavailable: 'The push relay is not reachable. Try again later.',
  timeout: 'Activation timed out. Open the app and try again.',
  rejected: 'The push relay rejected the registration.',
  keychain: 'The notification key could not be updated. Try again.',
  unsupported: 'Native notifications are not available on this device.',
};

function nativeStateFrom(status) {
  if (status?.enabled) return 'subscribed';
  return status?.permission === 'denied' ? 'denied' : 'default';
}

async function refreshNativeState(native) {
  try {
    setPushState(nativeStateFrom(await native.status()));
  } catch (_) {
    setPushState('default');
  }
}

async function enableNative(native) {
  setPushState('busy');
  try {
    setPushState(nativeStateFrom(await native.enable()));
  } catch (e) {
    if (e?.code !== 'cancelled') {
      addToast({
        title: 'Could not enable notifications',
        detail: NATIVE_ERRORS[e?.code] || 'Try again.',
        type: 'error',
      });
    }
    await refreshNativeState(native);
  }
}

async function disableNative(native) {
  setPushState('busy');
  try {
    setPushState(nativeStateFrom(await native.disable()));
  } catch (e) {
    addToast({ title: 'Could not disable notifications', detail: NATIVE_ERRORS[e?.code] || 'Try again.', type: 'error' });
    await refreshNativeState(native);
  }
}

// What is drawn over the conversation without replacing it: the mobile session
// panel and drawer, the full-screen preview and any registered overlay
// (settings, popovers, timelines). Under any of them the conversation is not
// being read.
function conversationCovered(state, id) {
  if (hasBlockingOverlay()) return true;
  if (state.drawerOpen || state.sessions?.[id]?.previewOpen) return true;
  return !!state.isMobile && sessionPanelView(state, id).open;
}

// Tells the app which conversation is on screen so it can skip the banner for
// a notification about that very session. Only a conversation whose history is
// loaded and uncovered counts; in any other case it reports null and the app
// shows everything.
export function watchNativeVisibleSession({ getState = store.get, subscribe = store.subscribe, doc = globalThis.document } = {}) {
  let last;
  const sync = () => {
    const native = nativePush();
    if (!native || !doc) return;
    const state = getState();
    const shown = shownSessionIds(state);
    const id = shown.length === 1 && isPresent(state, shown[0], doc) && !conversationCovered(state, shown[0])
      ? shown[0]
      : null;
    if (id === last) return;
    last = id;
    native.setVisibleSession(id).catch(() => { last = undefined; });
  };
  const unsubscribe = subscribe(sync);
  const unsubscribeOverlays = subscribeOverlays(sync);
  doc?.addEventListener('visibilitychange', sync);
  sync();
  return () => {
    unsubscribe();
    unsubscribeOverlays();
    doc?.removeEventListener('visibilitychange', sync);
  };
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
    new Promise((_, reject) => setTimeout(() => reject(new Error(`timeout on «${label}»`)), ms)),
  ]);
}

// readyRegistration returns an active service-worker registration, registering
// it on demand. We avoid navigator.serviceWorker.ready because on iOS it can
// stay pending forever when the page isn't yet controlled by a worker. Instead
// we register (or reuse) the ROOT worker (/sw.js) and wait for it to reach
// 'activated'.
export async function readyRegistration() {
  let reg = await navigator.serviceWorker.getRegistration('/');
  if (!reg) reg = await navigator.serviceWorker.register('/sw.js');
  if (reg.active) return reg;
  const worker = reg.installing || reg.waiting;
  if (!worker) throw new Error('the service worker did not start');
  await new Promise((resolve, reject) => {
    worker.addEventListener('statechange', () => {
      if (worker.state === 'activated') resolve();
      else if (worker.state === 'redundant') reject(new Error('the service worker failed to install'));
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
  const native = nativePush();
  if (native) { await refreshNativeState(native); return; }
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
  const native = nativePush();
  if (native) { await enableNative(native); return; }
  if (!supported()) return;
  setPushState('busy');
  try {
    const perm = await Notification.requestPermission();
    if (perm !== 'granted') {
      setPushState(perm === 'denied' ? 'denied' : 'default');
      return;
    }
    const { key } = await withTimeout(api('GET', '/api/push/vapid-public-key'), 10000, 'VAPID key');
    const reg = await withTimeout(readyRegistration(), 10000, 'service worker');
    let sub = await withTimeout(reg.pushManager.getSubscription(), 10000, 'current subscription');
    if (!sub) {
      sub = await withTimeout(reg.pushManager.subscribe({
        userVisibleOnly: true,
        applicationServerKey: urlBase64ToUint8Array(key),
      }), 20000, 'subscribe');
    }
    await withTimeout(api('POST', '/api/push/subscribe', subscriptionPayload(sub)), 10000, 'save on server');
    setPushState('subscribed');
  } catch (e) {
    // Surface the failure instead of silently reporting success: a browser-side
    // subscription can exist even when the server never stored it.
    console.error('[push] enable failed', e);
    addToast({ title: 'Could not enable notifications', detail: String((e && e.message) || e), type: 'error' });
    setPushState('default');
  }
}

export async function disablePush() {
  const native = nativePush();
  if (native) { await disableNative(native); return; }
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
