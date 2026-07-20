// router.js — minimal in-app view router. Replaces the full-page navigations
// (window.location.href = "?view=…") that used to switch between the single
// conversation screen and the pane grid. Those hops are frequent — maximizing a
// session to focus and dropping it back to the grid is a core gesture — and a
// full reload there tears down the bundle, re-runs the bootstrap (reloading
// sessions, reopening every WebSocket) and flashes the whole page. That felt
// broken. Here `view` lives in the store; navigate() flips it and updates the
// URL via pushState so nothing reloads, and a single popstate listener keeps the
// store in sync with the browser Back/Forward buttons.
//
// SCOPE: only the in-product hop (conversation ⇄ grid) goes through here. The
// design galleries (?view=catalog|live|subagent|mobile) stay plain <a> links —
// they are dev-only and a reload there is fine (they need the bootstrap anyway).
//
// COEXISTENCE WITH overlay-history.js: that module pushes a same-URL "guard"
// entry for open overlays and pops it on Back. Our popstate handler re-derives
// the view from the URL and only applies a change, so an overlay guard pop
// (URL unchanged) is a no-op here — the two never fight.

import { store, setState } from './store.js';
import { openSession } from './tile-actions.js';
import { collapseForNavigation } from './overlay-history.js';

// viewFromLocation reads the current ?view= (null for the default conversation
// screen). Exported so the store can seed its initial `view` from the URL.
export function viewFromLocation() {
  if (typeof location === 'undefined') return null;
  return new URLSearchParams(location.search).get('view') || null;
}

// applyLocation reconciles the store's `view` with the current URL. Called on
// popstate (Back/Forward). Idempotent: no store write when the view is already
// in sync (so an overlay-history guard pop, which leaves the URL untouched,
// changes nothing here).
function applyLocation() {
  const view = viewFromLocation();
  if (store.get().view !== view) setState({ view });
}

// navigate switches the view in place — no reload. `target` is the view key
// (null = conversation/mobile, 'grid' = pane grid). `opts.session`, when set, is
// brought into focus first (openSession assigns it to the focused tile on
// desktop / the active slot on mobile) so the conversation screen shows it.
// `opts.replace` swaps the history entry instead of pushing a new one.
//
// Any open Sheet-based overlay is CLOSED first (collapseForNavigation): its
// single history guard sits on top of the current URL, so we must consume it
// before changing the URL or a later Back would land on the stranded guard
// instead of the previous view. When a guard was active we overwrite it with
// replaceState (history stays [previous view, new view]); otherwise we push.
export function navigate(target = null, opts = {}) {
  const { session } = opts;
  if (session) openSession(session);
  const hadOverlay = collapseForNavigation();
  const method = (opts.replace || hadOverlay) ? 'replaceState' : 'pushState';
  try {
    const url = target ? `?view=${encodeURIComponent(target)}` : location.pathname;
    window.history[method]({ moaView: target || null }, '', url);
  } catch (_) { /* history/location unavailable (SSR/tests) — store update still applies */ }
  if (store.get().view !== (target || null)) setState({ view: target || null });
}

let bound = false;

// bindRouter installs the single global popstate listener. Called once from the
// app bootstrap. Returns an unbind for teardown/tests.
export function bindRouter() {
  if (bound || typeof window === 'undefined') return () => {};
  bound = true;
  window.addEventListener('popstate', applyLocation);
  return () => {
    window.removeEventListener('popstate', applyLocation);
    bound = false;
  };
}

// Test-only: fully unbind (remove the listener) and reset the guard so a test
// can install/remove the listener deterministically regardless of order.
export function __resetRouterForTests() {
  if (bound && typeof window !== 'undefined') {
    window.removeEventListener('popstate', applyLocation);
  }
  bound = false;
}
