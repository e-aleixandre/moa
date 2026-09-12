// router.js — minimal in-app view router. Replaces the full-page navigations
// (window.location.href = "?view=…") that used to switch between the single
// conversation screen and the pane grid. Those hops are frequent — maximizing a
// session to focus and dropping it back to the grid is a core gesture — and a
// full reload there tears down the bundle, re-runs the bootstrap (reloading
// sessions, reopening every WebSocket) and flashes the whole page. That felt
// broken. Here `view` lives in the store and navigate() flips it in place.
//
// SCOPE: only the in-product hop (conversation ⇄ grid) goes through here.
//
// NO HISTORY ENTRIES. moa is used as an installed PWA that behaves like a
// native app, where a browser back/forward gesture means nothing: the grid and
// the conversation each carry their own visible way to reach the other. This
// used to pushState, which gave the PWA back/forward gestures that navigated
// inside the app (and, combined with the overlay history guard that no longer
// exists, a forward gesture onto an inert entry). So the URL is kept in sync
// with replaceState only: it still names the current view, so RELOADING and
// SHARING a link keep working, but no entry is ever added.

import { store, setState } from './store.js';
import { openSession } from './tile-actions.js';

// viewFromLocation reads the current ?view= (null for the default conversation
// screen). Exported so the store can seed its initial `view` from the URL.
export function viewFromLocation() {
  if (typeof location === 'undefined') return null;
  return new URLSearchParams(location.search).get('view') || null;
}

// navigate switches the view in place — no reload, no history entry. `target`
// is the view key (null = conversation/mobile, 'grid' = pane grid).
// `opts.session`, when set, is brought into focus first (openSession assigns it
// to the focused tile on desktop / the active slot on mobile) so the
// conversation screen shows it.
export function navigate(target = null, opts = {}) {
  const { session } = opts;
  if (session) openSession(session);
  try {
    // Preserve any other query parameters; only ?view= is ours.
    const params = new URLSearchParams(location.search);
    if (target) params.set('view', target); else params.delete('view');
    const qs = params.toString();
    window.history.replaceState(
      { moaView: target || null },
      '',
      qs ? `${location.pathname}?${qs}` : location.pathname,
    );
  } catch (_) { /* history/location unavailable (SSR/tests) — store update still applies */ }
  if (store.get().view !== (target || null)) setState({ view: target || null });
}
