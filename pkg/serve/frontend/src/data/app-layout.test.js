import { expect, test } from "bun:test";
import {
  globalPaletteContext,
  isDesktopGridShortcut,
  isPaneFocusShortcut,
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
  expect(isDesktopGridShortcut({ key: "g", metaKey: true, ctrlKey: false }, { isMobile: false, paletteOpen: true }, false)).toBe(false);
});

test("pane focus shortcuts defer to the palette and blocking overlays", () => {
  const command = { key: "1", metaKey: true, ctrlKey: false, altKey: false };
  const control = { key: "2", metaKey: false, ctrlKey: true, altKey: false };
  const alternate = { key: "3", metaKey: false, ctrlKey: false, altKey: true };

  expect(isPaneFocusShortcut(command, { paletteOpen: false }, false)).toBe(true);
  expect(isPaneFocusShortcut(control, { paletteOpen: false }, false)).toBe(true);
  expect(isPaneFocusShortcut(alternate, { paletteOpen: false }, false)).toBe(true);
  expect(isPaneFocusShortcut(command, { paletteOpen: true }, false)).toBe(false);
  expect(isPaneFocusShortcut(command, { paletteOpen: false }, true)).toBe(false);
  expect(isPaneFocusShortcut({ ...command, key: "0" }, { paletteOpen: false }, false)).toBe(false);
});
