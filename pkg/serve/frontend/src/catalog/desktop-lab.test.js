import { test, expect } from "bun:test";
import { scaleForViewport, layoutWidth, DESKTOP_LAB_WIDTH } from "./desktop-lab.jsx";

test("a laptop viewport shows the frame at 1:1", () => {
  expect(scaleForViewport(1440)).toBe(1);
  expect(scaleForViewport(DESKTOP_LAB_WIDTH + 24)).toBe(1);
});

test("a phone viewport scales the frame to fit with a gutter", () => {
  // 390 − 24 = 366, / 1280 ≈ 0.286. If this drifts up to 1 the frame overflows
  // the phone; if it ignores the gutter the sides clip.
  expect(scaleForViewport(390)).toBeCloseTo((390 - 24) / 1280);
  expect(scaleForViewport(390)).toBeLessThan(1);
});

test("a viewport narrower than the gutter does not invert the scale", () => {
  expect(scaleForViewport(0)).toBe(0);
  expect(scaleForViewport(10)).toBe(0);
});

test("pinch-zoom does not shrink the fitted width", () => {
  expect(layoutWidth(390, 390, 1)).toBe(390);
  expect(layoutWidth(390, 195, 2)).toBe(390);
  expect(layoutWidth(195, 195, 2)).toBe(390);
});
