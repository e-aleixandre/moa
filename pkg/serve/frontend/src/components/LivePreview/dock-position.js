// dock-position — where the phone's preview dock rests, and where a drag
// lands it. The dock is never left in the middle of nowhere: it rests at one
// of four anchors — the bottom and top edges (horizontal) or the middle of the
// left and right edges (vertical) — and a released drag snaps to the nearest.
//
// Four, not two: an app's own controls live mostly in its top bar and its
// bottom bar, which are exactly what a horizontal dock covers. The side
// anchors cover neither.

export const ANCHORS = ["bottom", "top", "left", "right"];
export const DEFAULT_ANCHOR = "bottom";

const KEY = "moa-preview-dock";

export function isVertical(anchor) {
  return anchor === "left" || anchor === "right";
}

// nearestAnchor — the anchor whose resting point is closest to where the dock's
// centre was released. Measured in fractions of the stage, not pixels: a phone
// is twice as tall as it is wide, and in pixels every point would be "near" a
// side edge. A side only wins when the thumb clearly carried the dock there.
export function nearestAnchor(point, stage) {
  const x = point.x / Math.max(stage.w, 1);
  const y = point.y / Math.max(stage.h, 1);
  const spots = {
    bottom: { x: 0.5, y: 1 },
    top: { x: 0.5, y: 0 },
    left: { x: 0, y: 0.5 },
    right: { x: 1, y: 0.5 },
  };
  let best = DEFAULT_ANCHOR;
  let bestDistance = Infinity;
  for (const anchor of ANCHORS) {
    const spot = spots[anchor];
    const distance = Math.hypot(x - spot.x, y - spot.y);
    if (distance < bestDistance) {
      best = anchor;
      bestDistance = distance;
    }
  }
  return best;
}

// oppositeAnchor — where one tap on the grip sends the dock: the other side
// of the same axis, so "get out of the way" never takes a drag.
export function oppositeAnchor(anchor) {
  return { bottom: "top", top: "bottom", left: "right", right: "left" }[anchor] || DEFAULT_ANCHOR;
}

// Arrow keys on the grip move the dock the way the arrow points.
export function anchorForKey(key) {
  return { ArrowDown: "bottom", ArrowUp: "top", ArrowLeft: "left", ArrowRight: "right" }[key] || null;
}

// Per device, not per session: it is about the hand holding the phone.
export function loadDockAnchor(storage = globalThis.localStorage) {
  try {
    const value = storage?.getItem(KEY);
    return ANCHORS.includes(value) ? value : DEFAULT_ANCHOR;
  } catch {
    return DEFAULT_ANCHOR;
  }
}

export function saveDockAnchor(anchor, storage = globalThis.localStorage) {
  if (!ANCHORS.includes(anchor)) return;
  try {
    storage?.setItem(KEY, anchor);
  } catch {
    /* private mode / quota — the dock just starts at the bottom next time */
  }
}
