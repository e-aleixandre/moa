// zoom.js — PURE pinch/pan math for the LivePreview stage.
//
// The pinch is measured by an overlay the SHELL puts over the iframe (Zoom
// mode): a gesture that crossed the iframe boundary would be split between two
// touch-active documents and never become one two-finger sequence. So the
// touches arrive in STAGE px, `stageGesture` converts them to the iframe's own
// coordinates, and `applyGesture` turns that into the shell's transform.
//
// Coordinates, once, so the rest of the code can stop thinking about it:
//   base   — the scale the chosen viewport width is already drawn at ("390" on
//            a narrower stage is scaled down to fit). Not ours to change.
//   zoom   — what the user pinched, 1..4, on TOP of base.
//   pan    — the stage-pixel offset of the frame's top-left corner.
//   S      — base * zoom, the total scale: 1 iframe px = S stage px.
// A finger travelling D stage px therefore reads as D / S inside the app, which
// is why the reported dx/dy are multiplied back by the scale at gesture start.

export const MIN_ZOOM = 1;
export const MAX_ZOOM = 4;

export const IDENTITY = { zoom: 1, x: 0, y: 0 };

export function clampZoom(zoom) {
  if (!Number.isFinite(zoom)) return 1;
  return Math.min(MAX_ZOOM, Math.max(MIN_ZOOM, zoom));
}

// clampPan keeps the frame covering the stage: at zoom 1 there is nothing to
// pan (the fit already fills it), and zoomed in the user cannot drag the app
// off-screen and be left looking at the void.
export function clampPan(x, y, contentW, contentH, stageW, stageH) {
  const limit = (value, content, viewport) => {
    if (content <= viewport) return 0;
    return Math.min(0, Math.max(viewport - content, value));
  };
  return { x: limit(x, contentW, stageW), y: limit(y, contentH, stageH) };
}

// applyGesture — the new view after a pinch/pan update.
//   start : the {zoom,x,y} captured when the gesture began.
//   g     : { scale, dx, dy, cx, cy } as reported by the inspector.
//   frame : { base, w, h } — the frame's unscaled size and its fitting scale.
//   stage : { w, h } — the visible box.
//
// The anchor (cx,cy) is the midpoint between the two fingers, in iframe px: the
// pixel of the app the user is holding. It must stay under the fingers, so the
// pan is solved for it rather than accumulated.
export function applyGesture(start, g, frame, stage) {
  const base = frame.base || 1;
  const zoom = clampZoom((start.zoom || 1) * (g.scale || 1));
  const s0 = base * (start.zoom || 1);
  const s1 = base * zoom;
  const cx = g.cx || 0;
  const cy = g.cy || 0;
  const x = start.x + cx * s0 + (g.dx || 0) * s0 - cx * s1;
  const y = start.y + cy * s0 + (g.dy || 0) * s0 - cy * s1;
  const pan = clampPan(x, y, frame.w * s1, frame.h * s1, stage.w, stage.h);
  return { zoom, x: pan.x, y: pan.y };
}

// pinchState — what the overlay measures from two touches, in STAGE px: the
// distance between the fingers and their midpoint.
export function pinchState(a, b) {
  return {
    d: Math.hypot(a.clientX - b.clientX, a.clientY - b.clientY) || 1,
    cx: (a.clientX + b.clientX) / 2,
    cy: (a.clientY + b.clientY) / 2,
  };
}

// stageGesture — the overlay speaks stage px; `applyGesture` speaks iframe px.
// This is the single conversion between them: the anchor is the app pixel that
// was under the fingers when the gesture began, and the travel is the finger
// movement read at the scale the app was drawn at THEN.
export function stageGesture(start, frame, initial, current) {
  const s = (frame.base || 1) * (start.zoom || 1);
  return {
    scale: current.d / initial.d,
    dx: (current.cx - initial.cx) / s,
    dy: (current.cy - initial.cy) / s,
    cx: (initial.cx - start.x) / s,
    cy: (initial.cy - start.y) / s,
  };
}

// zoomAt — a scale step anchored on a stage point. What the −/+ buttons and a
// ctrl-wheel do: same math as a pinch, with the anchor given instead of
// measured. Without a point the centre of the stage is the honest default.
export function zoomAt(view, factor, point, frame, stage) {
  const s = (frame.base || 1) * (view.zoom || 1);
  const px = point ? point.x : stage.w / 2;
  const py = point ? point.y : stage.h / 2;
  return applyGesture(
    view,
    { scale: factor, dx: 0, dy: 0, cx: (px - view.x) / s, cy: (py - view.y) / s },
    frame,
    stage,
  );
}
