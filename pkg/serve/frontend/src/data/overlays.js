// Minimal overlay registry: tracks transient top-layer overlays (popovers,
// sheets) that sit ABOVE the base screens, so global shortcuts can defer to
// them. The command palette gates ⌘K on this (spec §6): a chord shouldn't open
// the palette underneath an open model/settings popover.
//
// This is intentionally tiny — not a z-index/stack manager, just "is some
// higher-layer overlay currently open?". The palette itself does NOT register
// here (⌘K must still toggle the palette when it's the only thing open).

const active = new Set();
const listeners = new Set();

function changed() {
  for (const fn of listeners) fn();
}

export function registerOverlay(id) {
  active.add(id);
  changed();
  return () => unregisterOverlay(id);
}

export function unregisterOverlay(id) {
  if (active.delete(id)) changed();
}

// Lets a consumer that mirrors "is something on top?" react to changes.
export function subscribeOverlays(fn) {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

export function hasBlockingOverlay() {
  return active.size > 0;
}
