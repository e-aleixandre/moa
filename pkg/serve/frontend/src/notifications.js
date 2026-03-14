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

export function addToast(toast) {
  const id = Date.now() + Math.random();
  toasts = [...toasts, { ...toast, id }];
  notifyToastListeners();
  setTimeout(() => removeToast(id), 5000);
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

// Browser notifications
export function requestNotificationPermission() {
  if ('Notification' in window && Notification.permission === 'default') {
    Notification.requestPermission();
  }
}

function browserNotify(title, body) {
  if (!document.hidden) return;
  if ('Notification' in window && Notification.permission === 'granted') {
    new Notification(title, { body });
  }
}

// Permission/error — called from state.js for non-visible sessions
export function triggerAttention(session, toolName, soundEnabled) {
  const title = session.title || 'Untitled';
  const detail = toolName
    ? `${toolName} — needs permission`
    : 'needs attention';

  addToast({ sessionId: session.id, title, detail, type: 'attention' });

  if (soundEnabled) {
    initAudio().then(playBeep);
  }

  browserNotify(title, detail);

  if (navigator.vibrate) {
    navigator.vibrate(200);
  }
}

// Turn done — called from state.js when a non-visible session finishes.
// Always shows a toast (the session isn't on screen). Sound/browser
// notifications only fire when the tab is hidden (background use).
export function triggerDone(session, soundEnabled) {
  const title = session.title || 'Untitled';

  addToast({ sessionId: session.id, title, detail: 'finished', type: 'done' });

  if (document.hidden) {
    if (soundEnabled) initAudio().then(playBeep);
    browserNotify(title, 'Turn finished');
    if (navigator.vibrate) navigator.vibrate(100);
  }
}
