// overlay-history.js — single, shared history binding for top-layer overlays
// (Sheet-based dialogs: RewindTimeline, the palette, drawers, file/HTML
// viewers…). The problem this replaces: several components each pushed their
// own history entry and installed their own `popstate` listener (FileViewer,
// HtmlResourceInfo). That doesn't compose — with more than one overlay open,
// or two independent listeners racing on the same pop, entries and callbacks
// get out of sync. This module keeps ONE stack and ONE global `popstate`
// listener for the whole app.
//
// KEY INVARIANT: exactly ONE history entry (the "guard") exists whenever the
// overlay stack is non-empty — NOT one per overlay. Browser history is linear:
// you can only pop the top, never an entry from the middle. If we pushed one
// entry per overlay and an overlay closed out of order (not the top), its
// entry would be stranded in the middle of history and a later back() would
// land on it instead of the pre-overlay state. Keeping a single guard entry
// for the whole stack removes that failure mode: opening/closing overlays
// while others remain open never touches history.
//
// Mental model:
//   • open when stack was empty  → pushState(guard), bind popstate.
//   • open when stack non-empty  → just stack it, no history change.
//   • back gesture (popstate)    → the browser consumed the guard; close the
//       TOP overlay; if others remain, re-push the guard (one back == one
//       overlay closed); if none remain, unbind.
//   • close via UI (Escape/X/backdrop, or programmatic) → remove from the
//       stack wherever it is; only when the stack becomes EMPTY do we consume
//       the guard with a single history.back(). Out-of-order close is then
//       just a stack removal — no history corruption.

const stack = [];
let popstateBound = false;

function hasHistory() {
  return typeof window !== "undefined" && typeof window.history?.pushState === "function";
}

function pushGuard() {
  window.history.pushState({ moaOverlay: true }, "");
}

function bindPopstate() {
  if (popstateBound || !hasHistory()) return;
  window.addEventListener("popstate", onPopState);
  popstateBound = true;
}

function unbindPopstate() {
  if (!popstateBound) return;
  window.removeEventListener("popstate", onPopState);
  popstateBound = false;
}

function onPopState() {
  // The browser already consumed the guard entry. Close the top overlay only.
  const entry = stack.pop();
  if (stack.length === 0) {
    // No overlays left — nothing to guard, let history rest where it is.
    unbindPopstate();
  } else {
    // Overlays remain open below: re-establish the single guard so the next
    // back gesture closes the next one (one back == one overlay).
    pushGuard();
  }
  if (!entry) return;
  entry.closed = true;
  entry.onRequestClose(true);
}

// openOverlay — call when an overlay becomes visible. `onRequestClose(fromPop)`
// is invoked once: either when the user goes back (fromPop === true, via the
// popstate listener above) or when the returned `close()` is called some
// other way. The returned `close(fromPop)` is idempotent — calling it twice
// (e.g. once from the component's own close handler and once from an
// already-fired popstate) is a no-op the second time.
//
// If history isn't available (SSR/tests without jsdom), the stack is skipped
// entirely and `close` degrades to a plain, idempotent callback wrapper.
export function openOverlay(id, onRequestClose) {
  if (!hasHistory()) {
    let closed = false;
    return (fromPop = false) => {
      if (closed) return;
      closed = true;
      if (!fromPop) onRequestClose(false);
    };
  }

  // Push the single guard entry only when the stack transitions empty→non-empty.
  if (stack.length === 0) {
    pushGuard();
    bindPopstate();
  }
  const entry = { id, onRequestClose, closed: false };
  stack.push(entry);

  return (fromPop = false) => {
    if (entry.closed) return;
    entry.closed = true;
    closeOverlay(id, fromPop, entry);
  };
}

// closeOverlay — remove an overlay from the stack when it closes through its
// own UI (Escape/X/backdrop click) or programmatically, rather than via the
// back gesture. Because the whole stack shares ONE guard entry, history is
// only touched when the LAST overlay closes: then a single history.back()
// consumes the guard. Closing any overlay while others remain open (including
// out of order, from the middle of the stack) is a pure stack removal with no
// history side effect — the guard still represents the overlays left open.
// Idempotent: closing an id/entry that isn't on the stack is a no-op.
export function closeOverlay(id, fromPop = false, knownEntry = null) {
  const idx = knownEntry ? stack.indexOf(knownEntry) : stack.findIndex((e) => e.id === id);
  if (idx === -1) return;
  stack.splice(idx, 1);
  if (fromPop) return;
  // Only consume the guard when nothing is left open.
  if (stack.length === 0) {
    unbindPopstate();
    if (hasHistory()) window.history.back();
  }
}

// Test-only escape hatch: bun test doesn't reload modules between files, so a
// stray listener/stack entry from one test could leak into the next.
export function __resetOverlayHistoryForTests() {
  stack.length = 0;
  unbindPopstate();
}

