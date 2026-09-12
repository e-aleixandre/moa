// Screen Wake Lock claims. A caller receives a private release function, so
// ending one recording cannot drop another recording's claim.

let sentinel = null;
let claims = new Set();
let listening = false;
let acquiring = false;
let epoch = 0;

// A ring of what actually happened, for diagnosing a lock that works sometimes
// and not others. It records; it does not change behaviour. Read it from the
// phone's console with __moaWakeLog(), which is why it hangs off globalThis.
const LOG_MAX = 60;
const log = [];
function note(event, detail) {
  log.push({
    at: new Date().toISOString(),
    event,
    ...detail,
    claims: claims.size,
    held: !!sentinel,
    visibility: typeof document === 'undefined' ? 'n/a' : document.visibilityState,
  });
  if (log.length > LOG_MAX) log.shift();
}
if (typeof globalThis !== 'undefined') {
  globalThis.__moaWakeLog = () => log.slice();
}

const supported = typeof navigator !== 'undefined'
  && 'wakeLock' in navigator
  && typeof navigator.wakeLock?.request === 'function';

// Injectable so the backoff's time-based rules can be tested without waiting
// out real seconds.
let now = () => Date.now();
export function __setWakeClockForTests(fn) {
  now = fn || (() => Date.now());
}

function visible() {
  return typeof document === 'undefined' || document.visibilityState === 'visible';
}

function wanted() {
  return claims.size > 0;
}

async function acquire() {
  if (!supported || sentinel || acquiring || !wanted() || !visible()) {
    note('acquire:skipped', {
      why: !supported ? 'unsupported'
        : sentinel ? 'already-held'
          : acquiring ? 'in-flight'
            : !wanted() ? 'no-claims'
              : 'hidden',
    });
    return;
  }
  const token = epoch;
  acquiring = true;
  let next;
  try {
    next = await navigator.wakeLock.request('screen');
  } catch (error) {
    // Swallowed on purpose -- a refused lock must not break the recording --
    // but recorded, because this is the branch that would explain a recording
    // whose screen sleeps. NotAllowedError arrives here on low battery.
    note('acquire:refused', { name: error?.name, message: error?.message });
    acquiring = false;
    return;
  }
  acquiring = false;
  if (token !== epoch || !wanted() || sentinel) {
    try { next.release(); } catch { /* already released */ }
    if (wanted() && !sentinel) acquire();
    return;
  }
  sentinel = next;
  const grantedAt = now();
  note('acquire:granted', {});
  sentinel.addEventListener('release', () => {
    // The platform can revoke a granted lock on its own, and until now nothing
    // reclaimed it: the recording kept going with no lock and the screen slept.
    // Reproduced against this module -- a revoke arrived with claims=1 and the
    // log went quiet. Reclaiming only while a claim is open and the page is
    // visible; WebKit refuses a hidden request, and onVisibility covers the
    // return to the foreground.
    const mine = sentinel === next;
    const stillWanted = wanted();
    note('sentinel:released', { byPlatform: mine && stillWanted });
    if (mine) sentinel = null;
    // A lock that held for a while was working, so the next revoke starts its
    // ladder fresh. Resetting on every grant instead would defeat the brake: a
    // platform that revokes instantly kept the retry going for as long as the
    // recording lasted (measured: 30 requests in 15s, two a second forever).
    if (mine && now() - grantedAt >= STABLE_MS) resetRetries();
    if (mine && stillWanted && visible()) reclaim();
  });
}

// Reclaiming after a platform revoke needs a brake. Measured against a
// platform that revokes immediately and always: a bare retry asked for the
// lock 647 times in 700ms. Backing off turns that into a handful of attempts
// and then silence, which is the honest outcome -- if the system will not give
// the lock, hammering it does not change that and only burns battery on a
// phone that is already recording.
const RETRY_MS = [500, 2000, 8000];
// How long a granted lock has to survive before it counts as having worked.
const STABLE_MS = 30000;
let retries = 0;
let retryTimer = null;

function reclaim() {
  if (retryTimer) return;
  const wait = RETRY_MS[Math.min(retries, RETRY_MS.length - 1)];
  if (retries >= RETRY_MS.length) {
    note('reclaim:gave-up', { attempts: retries });
    return;
  }
  retries += 1;
  note('reclaim:scheduled', { in: wait, attempt: retries });
  retryTimer = setTimeout(() => {
    retryTimer = null;
    if (wanted() && !sentinel && visible()) acquire();
  }, wait);
}

function resetRetries() {
  retries = 0;
  if (retryTimer) { clearTimeout(retryTimer); retryTimer = null; }
}

function onVisibility() {
  note('visibilitychange', {});
  if (wanted() && visible()) acquire();
}

function ensureVisibilityListener() {
  if (!listening && typeof document !== 'undefined') {
    document.addEventListener('visibilitychange', onVisibility);
    listening = true;
  }
}

function removeVisibilityListenerIfUnused() {
  if (listening && !wanted() && typeof document !== 'undefined') {
    document.removeEventListener('visibilitychange', onVisibility);
    listening = false;
  }
}

/**
 * Claim the wake lock and return an idempotent, scope-bound release function.
 * A page hide/manual lock releases the browser sentinel, but active claims stay
 * registered and reacquire it when WebKit returns to the foreground.
 */
export function claimWakeLock() {
  if (!supported) {
    note('claim:unsupported', {});
    return () => {};
  }
  const claim = {};
  claims.add(claim);
  note('claim:opened', {});
  // A new recording deserves its own attempts, whatever happened to the last.
  resetRetries();
  ensureVisibilityListener();
  acquire();
  let released = false;
  return () => {
    if (released) return;
    released = true;
    claims.delete(claim);
    note('claim:closed', {});
    if (!wanted()) resetRetries();
    epoch++;
    if (!wanted()) {
      const current = sentinel;
      sentinel = null;
      if (current) { try { current.release(); } catch { /* already released */ } }
    }
    removeVisibilityListenerIfUnused();
  };
}

// Kept as a small compatibility alias for callers that previously requested a
// lock imperatively. Releasing must use the returned function; there is no
// page-global release operation because it would break other active claims.
export const requestWakeLock = claimWakeLock;

export function __wakeLockStateForTests() {
  return {
    held: !!sentinel,
    wanted: wanted(),
    claims: claims.size,
    listening,
    supported,
    acquiring,
  };
}
