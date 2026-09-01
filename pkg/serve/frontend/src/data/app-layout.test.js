import { expect, test } from "bun:test";
import {
  globalPaletteContext,
  isDesktopGridShortcut,
  shouldLockMobileDocument,
} from "./app-layout.js";

test("mobile palette context wins over a grid URL and keeps the document locked", () => {
  const state = { isMobile: true, view: "grid" };
  expect(globalPaletteContext(state)).toBe("mobile");
  expect(shouldLockMobileDocument(state)).toBe(true);
});

test("desktop grid shortcut accepts command/control only outside blocking overlays", () => {
  expect(isDesktopGridShortcut({ key: "G", metaKey: true, ctrlKey: false }, { isMobile: false }, false)).toBe(true);
  expect(isDesktopGridShortcut({ key: "g", metaKey: false, ctrlKey: true }, { isMobile: false }, false)).toBe(true);
  expect(isDesktopGridShortcut({ key: "g", metaKey: true, ctrlKey: false }, { isMobile: true }, false)).toBe(false);
  expect(isDesktopGridShortcut({ key: "g", metaKey: true, ctrlKey: false }, { isMobile: false }, true)).toBe(false);
});
