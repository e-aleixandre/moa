// notifications.js — toasts, sound, browser notifications, vibration

let toasts = [];
let toastListeners = new Set();

export function getToasts() { return toasts; }
export function subscribeToasts(fn) {
  toastListeners.add(fn);
  return () => toastListeners.delete(fn);
}

function notifyToastListeners() {
  toastListeners.forEach(fn => fn(toasts));
}

// addToast returns the id it assigned so a caller that has to REPLACE its own
// toast (wake-on-event coalesces a burst of arrivals into a single count) can
// remove the previous one instead of stacking a second.
export function addToast(toast) {
  const id = Date.now() + Math.random();
  toasts = [...toasts, { ...toast, id }];
  notifyToastListeners();
  setTimeout(() => removeToast(id), 5000);
  return id;
}

// addSessionToast raises a toast for an event of a session, and is the one
// place that decides whether that session speaks to the user at all: a session
// an owner launched (origin "owner", not the owner's own conversation) reports
// to its owner instead. Returns null when nothing was raised.
export function addSessionToast(session, toast) {
  if (session?.origin === 'owner' && session?.kind !== 'owner') return null;
  return addToast(toast);
}

export function removeToast(id) {
  toasts = toasts.filter(t => t.id !== id);
  notifyToastListeners();
}

// Short beep sound (base64 encoded tiny wav)
const BEEP_DATA = 'data:audio/wav;base64,UklGRnoGAABXQVZFZm10IBAAAAABAAEAQB8AAEAfAAABAAgAZGF0YQoGAACBhYqFbF1fdH+Jk5ORf2xfW2x/ipSTkH5sXVxuf4qUk5B9bF1dbX+KlJOQfW1eXW1/ipSTkH1tXV1tf4qUk5B9bV5dbH+KlJOQfW1eXWx/ipSTkH1tXV5sf4qUkpB+bV5dbH+KlZKQfm1eXmx/ipWSkH5tXV5sf4qVkpB+bV5ebH+KlZKRfm1eXmx/ipaSkX5tXl5sf4qWkpF+bl5ebH+Kl5KRfm5eXmt/ipeSkX5uXl5rf4qYk5F+bl5ea3+KmJORf25eX2t/ipmTkX9uX19rf4qZk5F/bl9fa3+KmpSRf29fX2p/ipqUkX9vX19qf4qblJGAb19fanyLm5SRgG9fX2p8i5yVkYBvX2Bqe4udlZGAcF9gan2LnZWRgHBgYGp8i56VkYBwYGBqfIuelZGBcGBganyLnpWRgXBgYGp8i56WkYFwYGFqe4uel5GBcWBhanuLnpeRgXFhYWp7i5+XkYFxYWFqe4ufmJGBcWFhan2Ln5iRgnFhYWp8i5+YkoJyYWFqfIugmJKCcmFhanyLoJmSgnJhYmt8i6CZkoJyYmJrfIuhmZOCc2Jia3yLopmTg3NiYmt8i6KZk4NzYmJrfIujmpODc2Jia3yLo5qTg3NjY2t8i6Oak4NzY2NrfIujmpSDdGNja3uLpJuUg3RjY2t7i6SblIN0Y2Rre4uknJWDdGRka3uLpZyVg3RkZGt6i6WclYN1ZGRreouln';

let audioCtx = null;
let beepBuffer = null;

async function initAudio() {
  if (audioCtx) return;
  try {
    audioCtx = new (window.AudioContext || window.webkitAudioContext)();
    const resp = await fetch(BEEP_DATA);
    const buf = await resp.arrayBuffer();
    beepBuffer = await audioCtx.decodeAudioData(buf);
  } catch (_) {
    audioCtx = null;
  }
}

function playBeep() {
  if (!audioCtx || !beepBuffer) return;
  try {
    const source = audioCtx.createBufferSource();
    source.buffer = beepBuffer;
    source.connect(audioCtx.destination);
    source.start();
  } catch (_) { /* ignore */ }
}

function browserNotify(title, body) {
  if (!document.hidden) return;
  if ('Notification' in window && Notification.permission === 'granted') {
    new Notification(title, { body });
  }
}

// alertAway adds what a toast alone does not: a session the user is not
// looking at reached a point that matters. Toasts that answer the user's own
// action never come through here.
function alertAway(title, body, soundEnabled) {
  if (soundEnabled) initAudio().then(playBeep);
  browserNotify(title, body);
  if (navigator.vibrate) navigator.vibrate(200);
}

// Question/permission — called for non-visible sessions. `detail` says what
// the session waits on; without one the toast only says it needs attention.
export function triggerAttention(session, detail, soundEnabled) {
  const title = session.title || 'Untitled';
  detail ||= 'needs attention';
  if (addSessionToast(session, { sessionId: session.id, title, detail, type: 'attention' }) === null) return;
  alertAway(title, detail, soundEnabled);
}

// A run of a non-visible session failed: one Failed toast that names the
// session, with the same alert as a question.
export function triggerFailed(session, detail, soundEnabled) {
  const title = session.title || 'Untitled';
  detail ||= 'Run failed';
  if (addSessionToast(session, { sessionId: session.id, title, detail, type: 'error' }) === null) return;
  alertAway(title, detail, soundEnabled);
}

// Turn done — called from state.js when a non-visible session finishes.
// Always shows a toast (the session isn't on screen). Sound/browser
// notifications only fire when the tab is hidden (background use).
export function triggerDone(session, soundEnabled) {
  const title = session.title || 'Untitled';

  if (addSessionToast(session, { sessionId: session.id, title, type: 'done' }) === null) return;

  if (document.hidden) {
    if (soundEnabled) initAudio().then(playBeep);
    browserNotify(title, 'Turn finished');
    if (navigator.vibrate) navigator.vibrate(100);
  }
}
