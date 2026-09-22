import { test, expect } from "bun:test";
import {
  DEFAULT_ANCHOR,
  anchorForKey,
  isVertical,
  loadDockAnchor,
  nearestAnchor,
  oppositeAnchor,
  saveDockAnchor,
} from "./dock-position.js";

// The stage of a 390×844 phone, minus the sheet's padding.
const stage = { w: 374, h: 812 };

test("a drag released anywhere lands on the nearest edge, never in between", () => {
  expect(nearestAnchor({ x: 187, y: 790 }, stage)).toBe("bottom");
  expect(nearestAnchor({ x: 187, y: 30 }, stage)).toBe("top");
  expect(nearestAnchor({ x: 20, y: 400 }, stage)).toBe("left");
  expect(nearestAnchor({ x: 360, y: 420 }, stage)).toBe("right");
  // Dropped in the upper middle of the screen: the top wins, not a free spot.
  expect(nearestAnchor({ x: 187, y: 250 }, stage)).toBe("top");
  // Dropped just above the dock's own resting place: it stays at the bottom.
  expect(nearestAnchor({ x: 200, y: 640 }, stage)).toBe("bottom");
});

test("one tap on the grip sends the dock to the other side of its axis", () => {
  expect(oppositeAnchor("bottom")).toBe("top");
  expect(oppositeAnchor("top")).toBe("bottom");
  expect(oppositeAnchor("left")).toBe("right");
  expect(oppositeAnchor("right")).toBe("left");
});

test("the side anchors make the dock vertical", () => {
  expect(isVertical("left")).toBe(true);
  expect(isVertical("right")).toBe(true);
  expect(isVertical("bottom")).toBe(false);
  expect(isVertical("top")).toBe(false);
});

test("arrow keys on the grip point where the dock goes", () => {
  expect(anchorForKey("ArrowUp")).toBe("top");
  expect(anchorForKey("ArrowLeft")).toBe("left");
  expect(anchorForKey("Enter")).toBeNull();
});

function memoryStorage() {
  const data = new Map();
  return { getItem: (k) => (data.has(k) ? data.get(k) : null), setItem: (k, v) => data.set(k, String(v)) };
}

test("the position is remembered on the device", () => {
  const storage = memoryStorage();
  expect(loadDockAnchor(storage)).toBe(DEFAULT_ANCHOR);
  saveDockAnchor("left", storage);
  expect(loadDockAnchor(storage)).toBe("left");
});

test("a stored value that is not an anchor falls back to the bottom", () => {
  const storage = memoryStorage();
  storage.setItem("moa-preview-dock", "middle");
  expect(loadDockAnchor(storage)).toBe("bottom");
  saveDockAnchor("nowhere", storage);
  expect(loadDockAnchor(storage)).toBe("bottom");
  const broken = { getItem: () => { throw new Error("denied"); }, setItem: () => { throw new Error("denied"); } };
  expect(loadDockAnchor(broken)).toBe("bottom");
  expect(() => saveDockAnchor("top", broken)).not.toThrow();
});
