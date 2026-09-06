import { test, expect } from 'bun:test';
import { applyGesture, chainPan, chainResidual, clampPan, clampZoom, pinchState, stageGesture, zoomAt, IDENTITY, MAX_ZOOM } from './zoom.js';

// The pinch is measured by the shell's own overlay over the iframe (a gesture
// crossing the frame boundary is split between two touch documents), so it
// arrives in STAGE px and is converted here. This is the math that turns it
// into the stage's transform, so it is the part worth testing without a browser.

const stage = { w: 390, h: 800 };
const frame = { base: 1, w: 390, h: 800, stage };

test('zoom is bounded to 1..4 — a preview is not a microscope', () => {
  expect(clampZoom(0.2)).toBe(1);
  expect(clampZoom(9)).toBe(MAX_ZOOM);
  expect(clampZoom(NaN)).toBe(1);
  expect(clampZoom(2.5)).toBe(2.5);
});

test('pan never drags the app off the stage, and is zero when it already fits', () => {
  expect(clampPan(50, 50, 390, 800, 390, 800)).toEqual({ x: 0, y: 0 });
  // Content twice the stage: the left edge can go to -390 and no further.
  expect(clampPan(100, 0, 780, 1600, 390, 800)).toEqual({ x: 0, y: 0 });
  expect(clampPan(-1000, -50, 780, 1600, 390, 800)).toEqual({ x: -390, y: -50 });
});

test('a pinch with no movement changes nothing', () => {
  const view = applyGesture(IDENTITY, { scale: 1, dx: 0, dy: 0, cx: 195, cy: 400 }, frame, stage);
  expect(view).toEqual({ zoom: 1, x: 0, y: 0 });
});

test('the anchor point stays under the fingers while zooming', () => {
  const cx = 100;
  const cy = 200;
  const view = applyGesture(IDENTITY, { scale: 2, dx: 0, dy: 0, cx, cy }, frame, stage);
  expect(view.zoom).toBe(2);
  // The app pixel (cx,cy) maps to view.x + cx*scale — it must land back on the
  // same stage coordinate it started at.
  expect(view.x + cx * 2).toBeCloseTo(cx, 5);
  expect(view.y + cy * 2).toBeCloseTo(cy, 5);
});

test('a two-finger drag while zoomed moves the app by that much', () => {
  const start = { zoom: 2, x: -100, y: -100 };
  const view = applyGesture(start, { scale: 1, dx: -20, dy: -30, cx: 100, cy: 200 }, frame, stage);
  expect(view.zoom).toBe(2);
  expect(view.x).toBeCloseTo(-140, 5); // 20 iframe px at scale 2
  expect(view.y).toBeCloseTo(-160, 5);
});

test('zooming out returns to a flush corner rather than a gap', () => {
  const start = { zoom: 3, x: -300, y: -400 };
  const view = applyGesture(start, { scale: 0.1, dx: 0, dy: 0, cx: 195, cy: 400 }, frame, stage);
  expect(view.zoom).toBe(1);
  expect(view).toEqual({ zoom: 1, x: 0, y: 0 });
});

test('a scaled-down fixed width is the BASE, and the pinch multiplies it', () => {
  // 390 wide app on a 195 wide stage: base 0.5. A 2x pinch means 1:1 pixels.
  const half = { base: 0.5, w: 390, h: 1600, stage: { w: 195, h: 800 } };
  const view = applyGesture(IDENTITY, { scale: 2, dx: 0, dy: 0, cx: 0, cy: 0 }, half, half.stage);
  expect(view.zoom).toBe(2);
  expect(view.x).toBe(0);
  expect(view.y).toBe(0);
});

test('pinchState reads the fingers: distance and midpoint, in stage px', () => {
  const s = pinchState({ clientX: 100, clientY: 100 }, { clientX: 140, clientY: 130 });
  expect(s.d).toBeCloseTo(50, 5);
  expect(s.cx).toBe(120);
  expect(s.cy).toBe(115);
});

test('two fingers moving apart over the overlay zoom around their midpoint', () => {
  const start = IDENTITY;
  const a0 = { clientX: 150, clientY: 300 };
  const b0 = { clientX: 250, clientY: 300 }; // mid 200,300 · d 100
  const a1 = { clientX: 100, clientY: 300 };
  const b1 = { clientX: 300, clientY: 300 }; // same mid · d 200
  const g = stageGesture(start, frame, pinchState(a0, b0), pinchState(a1, b1));
  expect(g.scale).toBeCloseTo(2, 5);
  const view = applyGesture(start, g, frame, stage);
  expect(view.zoom).toBe(2);
  // The app pixel under the midpoint stays under the midpoint.
  expect(view.x + 200 * 2).toBeCloseTo(200, 5);
});

test('client touch coordinates are normalized to the offset overlay before anchoring', () => {
  const offset = { left: 8, top: 62 };
  const a0 = { clientX: 145, clientY: 250 };
  const b0 = { clientX: 245, clientY: 250 }; // client midpoint 195,250
  const a1 = { clientX: 95, clientY: 250 };
  const b1 = { clientX: 295, clientY: 250 };
  const initial = pinchState(a0, b0, offset);
  const gesture = stageGesture(IDENTITY, frame, initial, pinchState(a1, b1, offset));
  const view = applyGesture(IDENTITY, gesture, frame, stage);

  expect(initial).toMatchObject({ cx: 187, cy: 188 });
  expect(view).toMatchObject({ zoom: 2, x: -187, y: -188 });
  expect(view.x + initial.cx * 2).toBeCloseTo(initial.cx, 5);
  expect(view.y + initial.cy * 2).toBeCloseTo(initial.cy, 5);
});

test('stage px become iframe px at the scale the gesture STARTED at', () => {
  // base 0.5, zoom 2 → S 1: a finger travelling 40 stage px is 40 app px.
  const half = { base: 0.5, w: 780, h: 1600 };
  const g = stageGesture({ zoom: 2, x: 0, y: 0 }, half,
    { d: 100, cx: 100, cy: 100 }, { d: 100, cx: 140, cy: 100 });
  expect(g.dx).toBeCloseTo(40, 5);
  expect(g.cx).toBeCloseTo(100, 5);
});

test('the +/- buttons zoom around the centre of the stage', () => {
  const view = zoomAt(IDENTITY, 2, null, frame, stage);
  expect(view.zoom).toBe(2);
  // Stage centre (195,400) must still show the same app pixel.
  expect(view.x + 195 * 2).toBeCloseTo(195, 5);
  expect(view.y + 400 * 2).toBeCloseTo(400, 5);
  // And back out lands flush at 1:1, not on a gap.
  expect(zoomAt(view, 0.5, null, frame, stage)).toEqual({ zoom: 1, x: 0, y: 0 });
});

test('a zoom step given a point anchors on that point', () => {
  const view = zoomAt(IDENTITY, 2, { x: 100, y: 200 }, frame, stage);
  expect(view.x + 100 * 2).toBeCloseTo(100, 5);
  expect(view.y + 200 * 2).toBeCloseTo(200, 5);
});

// Zoomed, the app scroller is asked first and only what it could NOT take moves
// the frame. These are the two halves of that: what is left over, and where it
// puts the view.

test('an app that scrolled the whole request leaves the frame where it was', () => {
  expect(chainResidual(-150, -150)).toBe(0);
  const view = { zoom: 2, x: 0, y: -250 };
  expect(chainPan(view, { dx: 0, dy: -150, scale: 2 }, { dx: 0, dy: -150 }, frame, stage)).toEqual(view);
});

test('an app at its edge hands the whole request back as finger travel', () => {
  // 300 stage px down at 2x is 150 app px up; the app takes none, so the frame
  // moves the same 300 stage px — and stops at the top, 250 away.
  const view = chainPan({ zoom: 2, x: 0, y: -250 }, { dx: 0, dy: -150, scale: 2 }, { dx: 0, dy: 0 }, frame, stage);
  expect(view).toEqual({ zoom: 2, x: 0, y: 0 });
});

test('a partial consumption moves the frame by the remainder only', () => {
  const view = chainPan({ zoom: 2, x: 0, y: -250 }, { dx: 0, dy: -150, scale: 2 }, { dx: 0, dy: -100 }, frame, stage);
  expect(view.y).toBeCloseTo(-150, 5); // 50 app px left * scale 2
});

test('the residual is read at the scale the packet was SENT at, base included', () => {
  // base 0.5, zoom 2 → S 1: 40 unconsumed app px are 40 stage px.
  const half = { base: 0.5, w: 780, h: 1600 };
  const halfStage = { w: 195, h: 800 };
  const view = chainPan({ zoom: 2, x: -100, y: 0 }, { dx: -40, dy: 0, scale: 1 }, { dx: 0, dy: 0 }, half, halfStage);
  expect(view.x).toBeCloseTo(-60, 5);
});

test('x and y are independent: one axis consumed does not hide the other', () => {
  const view = chainPan({ zoom: 2, x: -100, y: -100 }, { dx: -30, dy: -40, scale: 2 }, { dx: -30, dy: 0 }, frame, stage);
  expect(view.x).toBeCloseTo(-100, 5);
  expect(view.y).toBeCloseTo(-20, 5);
});

// Scroll snap can land somewhere other than where it was asked to. Neither case
// may turn into movement the finger did not ask for.
test('an overshooting or reversing scroller never pans the frame backwards', () => {
  expect(chainResidual(-150, -400)).toBe(0);
  expect(chainResidual(-150, 200)).toBe(-150);
  const overshoot = chainPan({ zoom: 2, x: 0, y: -250 }, { dx: 0, dy: -150, scale: 2 }, { dx: 0, dy: -400 }, frame, stage);
  expect(overshoot).toEqual({ zoom: 2, x: 0, y: -250 });
});

test('a residual can never be worth more than the request that produced it', () => {
  expect(chainResidual(100, -100)).toBe(100);
  expect(chainResidual(-100, 100)).toBe(-100);
  expect(chainResidual(0, 50)).toBe(0);
  expect(chainResidual(Number.NaN, 10)).toBe(0);
  expect(chainResidual(-50, Number.NaN)).toBe(-50);
});

test('the chained pan obeys the same bounds as every other pan', () => {
  // A huge residual downward cannot expose the void past the bottom edge.
  const view = chainPan({ zoom: 2, x: 0, y: -400 }, { dx: 0, dy: 5000, scale: 2 }, { dx: 0, dy: 0 }, frame, stage);
  expect(view.y).toBe(-800); // frame 800*2 against an 800 stage
  expect(chainPan({ zoom: 1, x: 0, y: 0 }, { dx: 0, dy: -100, scale: 1 }, { dx: 0, dy: 0 }, frame, stage)).toEqual({ zoom: 1, x: 0, y: 0 });
});
