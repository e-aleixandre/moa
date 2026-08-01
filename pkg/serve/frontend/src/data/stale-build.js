// stale-build.js — detects that the page is running code the server no longer
// serves, and reloads past the browser cache.
//
// The assets ship under fixed names, so a client that cached the shell keeps
// running it. Cache-Control on the server (see pkg/serve/static_assets.go) is
// what normally fixes this, but an installed iOS PWA holds its own cache
// container with no reload affordance: a user who never deletes and re-adds the
// icon can stay on an old interface indefinitely. So the client also checks.
//
// The check compares the build id compiled into this bundle against the one
// /api/version reports. They come from the same frontend build, so a mismatch
// means, unambiguously, that this page is stale.

// RELOAD_MARK carries the id we reloaded for across the reload, so a cache that
// serves the old bundle again is detected as a failed attempt instead of
// looping. sessionStorage, not localStorage: the guard is scoped to this tab.
const RELOAD_MARK = 'moa-stale-build-reload';

export function currentBuildID() {
  return typeof globalThis.__MOA_BUILD_ID__ === 'string' ? globalThis.__MOA_BUILD_ID__ : '';
}

// isStale is true only when both ids are known and differ. An older server
// (no build_id) or an unstamped bundle reads as "cannot tell", and cannot tell
// must never trigger a reload.
export function isStale(current, served) {
  return Boolean(current) && Boolean(served) && current !== served;
}

// reloadPlan decides what to do about a served id, given what a previous
// attempt already tried. Pure, so the loop guard is testable without a DOM.
//
// Returns 'reload' to fetch the new build, 'clear' when a reload already
// succeeded and the mark is spent, 'give-up' when the reload came back on the
// same stale bundle (the cache won — reloading again would spin), and 'none'
// otherwise.
export function reloadPlan({ current, served, attempted }) {
  if (!isStale(current, served)) {
    return attempted ? 'clear' : 'none';
  }
  return attempted === served ? 'give-up' : 'reload';
}

// dropCaches empties the Cache API. The service worker stores nothing today
// (sw.js is push-only), but a stale-build reload is exactly the moment where a
// leftover cache from any past build must not survive.
async function dropCaches() {
  if (!('caches' in globalThis)) return;
  try {
    const names = await caches.keys();
    await Promise.all(names.map((n) => caches.delete(n)));
  } catch (_) { /* best effort: a failed purge must not block the reload */ }
}

// reloadForBuild reloads onto a URL the cache has no entry for. A plain
// location.reload() is allowed to be served from cache, which is precisely the
// failure being worked around; a one-shot query parameter is not, and it is
// dropped from the URL as soon as the new bundle boots (see adoptBuild).
async function reloadForBuild(served) {
  try {
    sessionStorage.setItem(RELOAD_MARK, served);
  } catch (_) { /* private mode: proceed without the loop guard */ }
  await dropCaches();
  const url = new URL(location.href);
  url.searchParams.set('__build', served);
  location.replace(url.toString());
}

function readMark() {
  try {
    return sessionStorage.getItem(RELOAD_MARK) || '';
  } catch (_) {
    return '';
  }
}

function clearMark() {
  try {
    sessionStorage.removeItem(RELOAD_MARK);
  } catch (_) { /* nothing to clean up */ }
  const url = new URL(location.href);
  if (url.searchParams.has('__build')) {
    url.searchParams.delete('__build');
    history.replaceState({}, '', url.pathname + (url.search || '') + url.hash);
  }
}

// adoptBuild acts on one /api/version response. Call it with every poll: the
// version poll already runs on load and on a timer, and returning to a
// backgrounded PWA is the moment a user expects to see the new interface.
//
// onStale, when the reload could not win against the cache, is the last resort:
// on iOS only closing the app from the app switcher clears its memory cache, so
// the user has to be told.
export function adoptBuild(result, { onStale } = {}) {
  const served = result && result.build_id;
  const plan = reloadPlan({ current: currentBuildID(), served, attempted: readMark() });
  switch (plan) {
    case 'reload':
      reloadForBuild(served);
      return 'reload';
    case 'clear':
      clearMark();
      return 'clear';
    case 'give-up':
      clearMark();
      if (onStale) onStale();
      return 'give-up';
    default:
      return 'none';
  }
}
