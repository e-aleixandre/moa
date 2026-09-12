// overlay-layers.js — ordering-only registry of stacked top-layer overlays.
//
// This is what is LEFT of the old data/overlay-history.js. That module bound
// overlays to the browser History API (a pushed "guard" entry per stack, a
// history.back() when the stack emptied, a global popstate listener) so the
// back gesture could close an overlay. moa is used as an installed PWA that
// behaves like a native app, and browser history navigation has no meaning
// anywhere in it: every overlay closes through its own controls (swipe-down,
// X, Escape, backdrop, internal Back) and the conversation ⇄ grid hop has its
// own affordance. Worse, the guard/back() dance left an entry AHEAD of the
// cursor, so the PWA offered a "forward" gesture that led to an inert entry.
// So the history machinery is gone and nothing here touches window.history.
//
// What survives is the one thing that was never about history: WHICH overlay is
// on top. The artifacts drawer listens for Escape in the CAPTURE phase, so it
// also sees keys aimed at a Sheet opened from one of its rows
// (HtmlResourceInfo). Without an order to consult it would close itself from
// underneath that Sheet. Only the surfaces involved in that question register:
// components/Sheet/Sheet.jsx and the drawer's own two layers.

const stack = [];

// pushLayer — register `id` as the new top layer. Returns an idempotent pop
// that removes it from wherever it sits (closing out of order is fine: this is
// a plain array, not a history cursor).
export function pushLayer(id) {
  const entry = { id };
  stack.push(entry);
  return () => {
    const idx = stack.indexOf(entry);
    if (idx !== -1) stack.splice(idx, 1);
  };
}

// isTopLayer — is `id` the overlay currently on top? Read by capture-phase key
// handlers that must defer to anything above them.
export function isTopLayer(id) {
  return stack.length > 0 && stack[stack.length - 1].id === id;
}

// Test-only escape hatch: bun test doesn't reload modules between files, so a
// stray entry from one test could leak into the next.
export function __resetOverlayLayersForTests() {
  stack.length = 0;
}
