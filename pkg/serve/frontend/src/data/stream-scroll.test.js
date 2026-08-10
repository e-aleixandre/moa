import { expect, test } from "bun:test";
import {
  AT_BOTTOM_PX,
  bottomScrollTop,
  isAtBottom,
  shouldPinForSessionChange,
  shouldRepinAfterContentResize,
} from "./stream-scroll-policy.js";

test("the bottom threshold is 80 pixels", () => {
  expect(AT_BOTTOM_PX).toBe(80);
  expect(isAtBottom(821, 1000, 100)).toBe(true);
  expect(isAtBottom(820, 1000, 100)).toBe(false);
});

test("manual scrolling away from the bottom releases the pin and returning restores it", () => {
  expect(isAtBottom(700, 1000, 100)).toBe(false);
  expect(isAtBottom(bottomScrollTop(1000, 100), 1000, 100)).toBe(true);
});

test("a content height change repins only a reader who is still following", () => {
  expect(shouldRepinAfterContentResize(true)).toBe(true);
  expect(shouldRepinAfterContentResize(false)).toBe(false);
});

test("a session change always starts at the latest message", () => {
  expect(shouldPinForSessionChange()).toBe(true);
});
