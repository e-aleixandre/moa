// Screen Wake Lock helper — keeps the device screen awake while an activity is
// in progress (currently: voice recording), so a phone doesn't auto-lock
// mid-sentence and cut the microphone off.
//
// Best-effort by design:
//   - It's a no-op where the Wake Lock API is unsupported (older browsers).
//   - The OS still releases the lock when the page is backgrounded or the user
//     locks the screen manually; we re-acquire on return-to-foreground as long
//     as a caller still wants the screen awake (`wanted`).
//   - iOS Safari 16.4+ supports this; on older iOS it simply does nothing.
//
// The API is a small imperative pair (request/release) rather than a hook so it
// can be driven straight from the recorder lifecycle callbacks in useVoice.

let sentinel = null; // current WakeLockSentinel, or null
let wanted = false; // whether a caller currently wants the screen awake
let listening = false; // visibilitychange listener installed?
let acquiring = false; // a navigator.wakeLock.request() is in flight
// Bumped by every releaseWakeLock(); an in-flight acquire() adopts its sentinel
// only if its epoch is still current, so a release during the request can't
// leave an orphaned lock or clobber a newer one.
let epoch = 0;

const supported = typeof navigator !== 'undefined'
  && 'wakeLock' in navigator
  && typeof navigator.wakeLock?.request === 'function';

function visible() {
  return typeof document === 'undefined' || document.visibilityState === 'visible';
}

async function acquire() {
  if (!supported || sentinel || acquiring || !wanted || !visible()) return;
  const token = epoch;
  acquiring = true;
  let s;
  try {
    s = await navigator.wakeLock.request('screen');
  } catch {
    // Rejections (not visible, not allowed) are non-fatal — the screen may just
    // lock as it would without us.
    acquiring = false;
    return;
  }
  acquiring = false;
  // A release (or a superseding request) happened while this was in flight, or
  // the caller no longer wants the lock — don't adopt the stale sentinel.
  if (token !== epoch || !wanted || sentinel) {
    try { s.release(); } catch { /* already gone */ }
    // If the page is still wanted-but-lockless (e.g. a spurious double request
    // beat us), let a fresh attempt run.
    if (wanted && !sentinel) acquire();
    return;
  }
  sentinel = s;
  // The OS can drop the lock on its own (e.g. low battery / manual lock); clear
  // our ref so a later return-to-foreground can re-acquire it.
  sentinel.addEventListener('release', () => {
    if (sentinel === s) sentinel = null;
  });
}

function onVisibility() {
  if (wanted && visible()) acquire();
}

// requestWakeLock asks to keep the screen awake and re-acquires the lock
// whenever the page returns to the foreground, until releaseWakeLock is called.
export function requestWakeLock() {
  if (!supported) return;
  wanted = true;
  if (!listening && typeof document !== 'undefined') {
    document.addEventListener('visibilitychange', onVisibility);
    listening = true;
  }
  acquire();
}

// releaseWakeLock lets the screen sleep again and stops re-acquiring.
export function releaseWakeLock() {
  wanted = false;
  epoch++; // invalidate any in-flight acquire()
  if (listening && typeof document !== 'undefined') {
    document.removeEventListener('visibilitychange', onVisibility);
    listening = false;
  }
  const s = sentinel;
  sentinel = null;
  if (s) { try { s.release(); } catch { /* already gone */ } }
}

// Exposed for tests only.
export function __wakeLockStateForTests() {
  return { held: !!sentinel, wanted, listening, supported, acquiring };
}
