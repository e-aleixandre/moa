// dismiss-gesture.js — the decisions behind dragging a sheet down or the
// drawer sideways, shared by hooks/useSheetDismiss.js and
// hooks/useEdgeSwipeDrawer.js and kept free of the DOM so they can be tested.
//
// The hooks used to reject a swipe as soon as its cross-axis drift passed a
// 10–12px slop, even when the main axis had moved much further — a fast flick
// is sampled coarsely, so its first move can be 30px down and 12px sideways.
// The direction is now decided once, by which axis dominates, when the finger
// has travelled far enough to tell. Release used to need 40% of the sheet's
// height (280px on a tall sheet) or a velocity read from the last two samples,
// which a finger slowing for its last frame reads as a stop.

export const DECIDE_DISTANCE = 10; // px of travel before the direction is judged
export const FLICK_VELOCITY = 0.4; // px/ms towards dismiss that commits regardless of distance
export const VELOCITY_WINDOW_MS = 100; // release velocity is measured over the last 100ms

// classifyDrag — "pending" until the finger has moved DECIDE_DISTANCE, then
// "drag" if the gesture goes mainly along `axis` in its positive/negative
// `sign`, otherwise "abandon" (the content keeps the gesture: a scroll, a
// horizontal strip, a move the other way).
export function classifyDrag(dx, dy, { axis, sign }) {
  if (Math.hypot(dx, dy) < DECIDE_DISTANCE) return "pending";
  const main = (axis === "y" ? dy : dx) * sign;
  const cross = Math.abs(axis === "y" ? dx : dy);
  return main > 0 && main >= cross ? "drag" : "abandon";
}

// releaseVelocity — px/ms along the samples' `v`, measured from the oldest
// sample inside the last VELOCITY_WINDOW_MS to the newest. Two adjacent
// samples alone are noise: a finger that slows for the last frame reads as a
// stop even after a fast flick.
export function releaseVelocity(samples) {
  if (!samples || samples.length < 2) return 0;
  const last = samples[samples.length - 1];
  let ref = samples[samples.length - 2];
  for (let i = samples.length - 2; i >= 0; i--) {
    if (last.t - samples[i].t > VELOCITY_WINDOW_MS) break;
    ref = samples[i];
  }
  const dt = last.t - ref.t;
  return dt > 0 ? (last.v - ref.v) / dt : 0;
}

// shouldDismiss — release commits when the surface has travelled far enough
// (a fraction of its size, capped so a tall sheet does not need a 300px drag)
// or when the finger was still moving towards dismiss fast enough.
export function shouldDismiss({ distance, size, velocity, fraction = 0.25, cap = 120 }) {
  return distance > Math.min(size * fraction, cap) || velocity > FLICK_VELOCITY;
}

// sheetDragBlocked — whether a touch starting on `target` must be left to the
// content instead of dragging the sheet: the text field being edited (the
// finger is placing a caret or selecting), or any scroller between the target
// and the sheet that is not at its top (dragging down scrolls it back first,
// as in iOS sheets). A field that is not focused does not block: the browser
// snaps a touch that lands near a field onto it, so blocking every field
// turned the band around each one into dead area. A tap still focuses it.
// `styleOf` is getComputedStyle, injected for tests; `focused` is
// document.activeElement.
export function sheetDragBlocked(target, root, styleOf, focused = null) {
  for (let el = target; el && el !== root; el = el.parentElement) {
    if (el === focused && isEditable(el)) return true;
    if (el.scrollTop > 0 && el.scrollHeight > el.clientHeight) {
      const oy = styleOf(el).overflowY;
      if (oy === "auto" || oy === "scroll") return true;
    }
  }
  return false;
}

function isEditable(el) {
  return el.tagName === "INPUT" || el.tagName === "TEXTAREA" || el.tagName === "SELECT" || !!el.isContentEditable;
}
