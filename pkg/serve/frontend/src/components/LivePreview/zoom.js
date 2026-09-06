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

// chainResidual — on ONE axis, the part of a requested app scroll the child did
// not take. Scroll snap (and any scroller that lands somewhere other than where
// it was asked to) can move further than requested, or the other way: the frame
// must never invent movement the finger did not ask for, so an overshoot is
// worth nothing more than the request and an opposite sign is worth nothing.
export function chainResidual(requested, consumed) {
  if (!Number.isFinite(requested)) return 0;
  const taken = Number.isFinite(consumed) ? consumed : 0;
  const rest = requested - taken;
  if (!rest) return 0;
  if (Math.sign(rest) !== Math.sign(requested)) return 0;
  return Math.abs(rest) > Math.abs(requested) ? requested : rest;
}

// chainPan — the view after a zoomed one-finger packet the app could not fully
// consume. The residual is app px again, so it becomes finger travel by the
// scale the packet was SENT at, and it moves the frame the opposite way (the
// app scrolls down when the finger goes up). It passes through the same bounds
// as every other pan: what neither the app scroller nor the frame can take is
// dropped, because both owned layers are at that edge.
export function chainPan(view, request, consumed, frame, stage) {
  const scale = request.scale || 1;
  const x = view.x - chainResidual(request.dx, consumed.dx) * scale;
  const y = view.y - chainResidual(request.dy, consumed.dy) * scale;
  const s = (frame.base || 1) * (view.zoom || 1);
  return { zoom: view.zoom, ...clampPan(x, y, frame.w * s, frame.h * s, stage.w, stage.h) };
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
// distance between the fingers and their midpoint. TouchEvent coordinates are
// client-relative, so callers can supply the overlay rect to normalize them.
export function pinchState(a, b, offset = null) {
  const left = offset?.left ?? offset?.x ?? 0;
  const top = offset?.top ?? offset?.y ?? 0;
  return {
    d: Math.hypot(a.clientX - b.clientX, a.clientY - b.clientY) || 1,
    cx: (a.clientX + b.clientX) / 2 - left,
    cy: (a.clientY + b.clientY) / 2 - top,
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

// zoomAt — a scale step anchored on a stage point. What a ctrl-wheel or a
// trackpad pinch does: same math as a pinch, with the anchor given instead of
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

// wheelFactor — the scale step a wheel notch is worth. A mouse's ctrl-wheel and
// a trackpad pinch arrive as the same event with the same units, so they get the
// same curve; the clamp is what stops one very large delta (a coarse mouse, or a
// deltaMode the app converted generously) from jumping the whole zoom range.
export function wheelFactor(deltaY) {
  if (!Number.isFinite(deltaY)) return 1;
  const bounded = Math.max(-200, Math.min(200, deltaY));
  return Math.exp(-bounded / 300);
}

// panBy — move the frame by stage pixels, through the same bounds as every
// other pan. What a Space+drag does.
export function panBy(view, dx, dy, frame, stage) {
  const s = (frame.base || 1) * (view.zoom || 1);
  return { zoom: view.zoom, ...clampPan(view.x + dx, view.y + dy, frame.w * s, frame.h * s, stage.w, stage.h) };
}

// appToStage — a point the app reported in ITS OWN pixels, in stage pixels.
// The only conversion the desktop bridge needs: the app cannot know the shell's
// pan or scale, so it always speaks its own coordinates.
export function appToStage(view, frame, x, y) {
  const s = (frame.base || 1) * (view.zoom || 1);
  return { x: view.x + x * s, y: view.y + y * s };
}
