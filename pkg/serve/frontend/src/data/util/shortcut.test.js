import { test, expect } from "bun:test";
import { formatShortcut, formatShortcutFor } from "./shortcut.js";

test("formatShortcutFor renders ⌘ on mac, Alt+ elsewhere", () => {
  expect(formatShortcutFor(true, "k", { mod: true })).toBe("⌘K");
  expect(formatShortcutFor(false, "k", { mod: true })).toBe("Alt+K");
});

test("formatShortcutFor renders shift (⇧ mac, Shift+ elsewhere)", () => {
  expect(formatShortcutFor(true, "g", { mod: true, shift: true })).toBe("⌘⇧G");
  expect(formatShortcutFor(false, "g", { mod: true, shift: true })).toBe("Alt+Shift+G");
});

test("formatShortcutFor with shift only (no mod)", () => {
  expect(formatShortcutFor(true, "a", { shift: true })).toBe("⇧A");
  expect(formatShortcutFor(false, "a", { shift: true })).toBe("Shift+A");
});

test("formatShortcutFor upper-cases single-char keys but keeps named keys", () => {
  expect(formatShortcutFor(true, "enter")).toBe("enter");
  expect(formatShortcutFor(false, "Enter")).toBe("Enter");
  expect(formatShortcutFor(true, "k")).toBe("K");
});

test("formatShortcutFor passes glyph keys through (upper-case is a no-op)", () => {
  expect(formatShortcutFor(true, ".", { mod: true })).toBe("⌘.");
  expect(formatShortcutFor(false, ".", { mod: true })).toBe("Alt+.");
  expect(formatShortcutFor(false, "1", { mod: true })).toBe("Alt+1");
});

test("formatShortcut resolves against the current platform (no throw)", () => {
  const out = formatShortcut("k", { mod: true });
  expect(out === "⌘K" || out === "Alt+K").toBe(true);
});
