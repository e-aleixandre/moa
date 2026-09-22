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
// underneath that Sheet. The sheets (components/Sheet, MobileSheet) and the
// drawer's own two layers register; the sheets also get the one-sheet rule
// below.

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

// isTopLayer — is `id` the overlay currently on top? Read by key handlers that
// must defer to anything above them. A sheet on its way out is no longer in
// the order: it keeps painting its exit, but the keys belong to what is left.
export function isTopLayer(id) {
  for (let i = stack.length - 1; i >= 0; i--) {
    if (!stack[i].closing) return stack[i].id === id;
  }
  return false;
}

// takeKey — is this key event `id`'s to act on? Only the top layer's, and
// only once. Closing the top sheet shows the one below in the same synchronous
// step, so without the once the SAME Escape would reach the sheet below — on
// top by then — and close it too.
const takenKeys = new WeakSet();
export function takeKey(id, event) {
  if (takenKeys.has(event) || !isTopLayer(id)) return false;
  takenKeys.add(event);
  return true;
}

// sheetEscape — the ONE thing an Escape does to the sheet that took it: back
// one page when it is showing a pushed page (`onBack`), otherwise close. Its
// pages are walked back from here rather than from a listener of their own, so
// one key can never both leave a page and close the sheet around it.
export function sheetEscape({ onBack, onClose }) {
  if (onBack) onBack();
  else onClose?.();
}

// ── One sheet at a time ─────────────────────────────────────────────────────
// On a phone a sheet asked for while another is showing used to paint OVER it:
// two grabbers, two ✕, a doubled scrim, and Escape / ✕ / swipe answering for
// different sheets. The rule lives here so no door has to remember it: only
// the most recently opened sheet is visible; every other sheet on the stack is
// COVERED — not closed. Its owner keeps its state and its children (a sheet is
// often the child of the one below it, so closing that one would take the new
// one down with it), and when the covering sheet goes the covered one is
// simply shown again, as it was. A sheet already leaving is covered by any
// open sheet, so a hand-off (close one, open the next) never shows both.
//
// Only sheets take part. A full-screen surface (the artifacts drawer, the live
// preview) registers with pushLayer: it neither hides nor gets hidden, and a
// sheet over it is still the only sheet.

// sheetLayer — the stack entry of one sheet component, for its whole life.
// `onCover(covered)` is called synchronously whenever that flag changes, so a
// covered sheet is hidden (or shown again) before anyone moves focus into it.
export function sheetLayer(id, onCover) {
  const entry = { id, sheet: true, closing: false, covered: false, onCover };
  const remove = () => {
    const idx = stack.indexOf(entry);
    if (idx !== -1) stack.splice(idx, 1);
  };
  return {
    // open — this sheet is the one asked for now: it goes on top.
    open() {
      remove();
      entry.closing = false;
      stack.push(entry);
      syncSheets();
    },
    // close — dismissed, but still painting its exit until release().
    close() {
      if (!stack.includes(entry) || entry.closing) return;
      entry.closing = true;
      syncSheets();
    },
    // release — gone from the screen. Silent: there is nothing left to show.
    release() {
      remove();
      entry.closing = false;
      entry.covered = false;
      syncSheets();
    },
  };
}

function setCovered(entry, covered) {
  if (entry.covered === covered) return;
  entry.covered = covered;
  entry.onCover?.(covered);
}

function syncSheets() {
  let visible = null;
  for (let i = stack.length - 1; i >= 0; i--) {
    if (stack[i].sheet && !stack[i].closing) { visible = stack[i]; break; }
  }
  for (const entry of stack.slice()) {
    if (entry.sheet) setCovered(entry, visible !== null && entry !== visible);
  }
}

// Test-only escape hatch: bun test doesn't reload modules between files, so a
// stray entry from one test could leak into the next.
export function __resetOverlayLayersForTests() {
  stack.length = 0;
}
