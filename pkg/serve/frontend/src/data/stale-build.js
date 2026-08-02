// stale-build.js — detects that the page is running code the server no longer
// serves, and reloads onto the versioned shell for the new build.
//
// The shell gives app.js and app.css build-specific paths. Changing only the
// document URL would not bypass stale HTTP-cache entries for fixed asset names;
// changing the subresource URLs does. The build id compiled into this bundle
// and the one /api/version reports come from the same runtime frontend tree, so
// a mismatch means this page is stale.

const RELOAD_MARK = 'moa-stale-build-reload';
let navigationPending = '';
let warnedAttempt = '';

export function currentBuildID() {
  return typeof globalThis.__MOA_BUILD_ID__ === 'string' ? globalThis.__MOA_BUILD_ID__ : '';
}

export function isStale(current, served) {
  return Boolean(current) && Boolean(served) && current !== served;
}

function attemptKey(current, served) {
  return `${current}->${served}`;
}

// reloadPlan is pure so the loop guard can be tested without a DOM. The stored
// marker identifies the exact transition already attempted; the URL marker is
// the fallback when sessionStorage is unavailable.
export function reloadPlan({ current, served, attempted = '', urlAttempt = '' }) {
  if (!current || !served) return 'none';
  if (current === served) return attempted || urlAttempt ? 'clear' : 'none';
  const transition = attemptKey(current, served);
  if (attempted === transition || urlAttempt === transition) return 'give-up';
  return 'reload';
}

function readMark() {
  try {
    return sessionStorage.getItem(RELOAD_MARK) || '';
  } catch (_) {
    return '';
  }
}

function writeMark(current, served) {
  try {
    sessionStorage.setItem(RELOAD_MARK, attemptKey(current, served));
  } catch (_) { /* the versioned URL remains as the loop guard */ }
}

function urlAttempt() {
  try {
    return new URL(location.href).searchParams.get('__build') || '';
  } catch (_) {
    return '';
  }
}

function clearAttempt() {
  navigationPending = '';
  warnedAttempt = '';
  try {
    sessionStorage.removeItem(RELOAD_MARK);
  } catch (_) { /* nothing to clean up */ }
  const url = new URL(location.href);
  if (url.searchParams.has('__build')) {
    url.searchParams.delete('__build');
    history.replaceState(history.state || null, '', url.pathname + (url.search || '') + url.hash);
  }
}

// The destination shell references /build/<id>/app.js and app.css, so all
// three requests use cache keys that did not exist in an older build.
// Navigation is synchronous: no asynchronous cache purge can let a concurrent
// version response consume the attempt marker before location.replace runs.
function reloadForBuild(current, served) {
  navigationPending = served;
  writeMark(current, served);
  const url = new URL(location.href);
  url.searchParams.set('__build', attemptKey(current, served));
  try {
    location.replace(url.toString());
    return true;
  } catch (_) {
    navigationPending = '';
    return false;
  }
}

// adoptBuild acts on every /api/version response. A failed transition remains
// marked until either the running build catches up or the server announces a
// different build, preventing repeated reloads on every foreground event.
export function adoptBuild(result, { onStale } = {}) {
  const current = currentBuildID();
  const served = result && result.build_id;

  if (navigationPending === served) return 'pending';
  if (navigationPending && navigationPending !== served) navigationPending = '';

  const plan = reloadPlan({
    current,
    served,
    attempted: readMark(),
    urlAttempt: urlAttempt(),
  });
  switch (plan) {
    case 'reload':
      if (reloadForBuild(current, served)) return 'reload';
      if (onStale) onStale();
      return 'give-up';
    case 'clear':
      clearAttempt();
      return 'clear';
    case 'give-up': {
      const key = attemptKey(current, served);
      if (warnedAttempt !== key) {
        warnedAttempt = key;
        if (onStale) onStale();
      }
      return 'give-up';
    }
    default:
      return 'none';
  }
}
