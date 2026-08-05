// Screen Wake Lock claims. A caller receives a private release function, so
// ending one recording cannot drop another recording's claim.

let sentinel = null;
let claims = new Set();
let listening = false;
let acquiring = false;
let epoch = 0;

const supported = typeof navigator !== 'undefined'
  && 'wakeLock' in navigator
  && typeof navigator.wakeLock?.request === 'function';

function visible() {
  return typeof document === 'undefined' || document.visibilityState === 'visible';
}

function wanted() {
  return claims.size > 0;
}

async function acquire() {
  if (!supported || sentinel || acquiring || !wanted() || !visible()) return;
  const token = epoch;
  acquiring = true;
  let next;
  try {
    next = await navigator.wakeLock.request('screen');
  } catch {
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
  sentinel.addEventListener('release', () => {
    if (sentinel === next) sentinel = null;
  });
}

function onVisibility() {
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
  if (!supported) return () => {};
  const claim = {};
  claims.add(claim);
  ensureVisibilityListener();
  acquire();
  let released = false;
  return () => {
    if (released) return;
    released = true;
    claims.delete(claim);
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
