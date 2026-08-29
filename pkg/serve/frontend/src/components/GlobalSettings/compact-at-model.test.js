import { expect, test } from "bun:test";
import {
  TOKENS_PER_UNIT,
  clampToFloor,
  floorUnits,
  formatTokens,
  modeForCompactAt,
  parseUnits,
} from "./compact-at-model.js";

test("no stored threshold means automatic, not zero tokens", () => {
  expect(modeForCompactAt(0)).toBe("auto");
  expect(modeForCompactAt(undefined)).toBe("auto");
  expect(modeForCompactAt(120000)).toBe("custom");
});

test("the floor rounds up, never down under the engine's minimum", () => {
  // 40384 tokens is 40.384k: offering 40k would be 384 tokens short of a
  // threshold the engine honors.
  expect(floorUnits(40384)).toBe(41);
  expect(floorUnits(40000)).toBe(40);
  expect(floorUnits(0)).toBe(1);
});

test("only a usable positive number commits", () => {
  expect(parseUnits("120")).toBe(120);
  expect(parseUnits(" 120 ")).toBe(120);
  expect(parseUnits("120.4")).toBe(120);
  expect(parseUnits("")).toBe(null);
  expect(parseUnits("abc")).toBe(null);
  expect(parseUnits("0")).toBe(null);
  expect(parseUnits("-5")).toBe(null);
});

test("a value under the floor is raised and reported as raised", () => {
  const min = 40384;
  expect(clampToFloor(10000, min)).toEqual({ tokens: 41 * TOKENS_PER_UNIT, clamped: true });
  // Exactly at the rounded-up floor is honored as asked.
  expect(clampToFloor(41000, min)).toEqual({ tokens: 41000, clamped: false });
  expect(clampToFloor(120000, min)).toEqual({ tokens: 120000, clamped: false });
  // 0 is "automatic", never floored into a real threshold.
  expect(clampToFloor(0, min)).toEqual({ tokens: 0, clamped: false });
});

test("thresholds read the way model windows are quoted", () => {
  expect(formatTokens(120000)).toBe("120k tokens");
  expect(formatTokens(0)).toBe("auto");
});
