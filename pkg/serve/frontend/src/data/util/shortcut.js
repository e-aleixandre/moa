// shortcut.js — platform-aware keyboard-shortcut LABELS (display only).
//
// The real bindings live in the components and already accept both modifiers
// (`e.metaKey || e.altKey`), so they work on every platform. This module only
// decides how the shortcut is SHOWN: ⌘ on Mac, Alt on Linux/Windows — the
// project rule (FUNCTIONALITY-AUDIT §13, COHERENCE-DECISIONS P6). Ctrl is
// deliberately never used because it clashes with browser shortcuts
// (Ctrl+K opens search in Chrome/Firefox, Ctrl+N a new window, etc.).
//
// Ported from the old SPA's hooks/useHotkeys.js (isMac + formatShortcut).

// SSR-safe: `navigator` is undefined outside the browser (tests, prerender).
export const isMac =
  typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.userAgent);

// modLabel / shiftLabel — the bare modifier glyphs, for callers that render
// each key as its own <kbd> chip (e.g. the command palette's shortcut arrays).
export const modLabel = isMac ? "⌘" : "Alt";
export const shiftLabel = isMac ? "⇧" : "Shift";

// formatShortcutFor — the pure formatter, parameterised by platform so it can
// be unit-tested for both mac and non-mac without touching global navigator.
// Single-character keys are upper-cased ("k" → "K"); named keys ("Enter") and
// glyphs ("⏎", ".", "1–9") are left as given so they read well inside copy.
export function formatShortcutFor(mac, key, { mod = false, shift = false } = {}) {
  const parts = [];
  if (mod) parts.push(mac ? "⌘" : "Alt+");
  if (shift) parts.push(mac ? "⇧" : "Shift+");
  const label = String(key);
  parts.push(label.length === 1 ? label.toUpperCase() : label);
  return parts.join("");
}

// formatShortcut(key, { mod, shift }) → a single display string on THIS device.
//   formatShortcut("K", { mod: true })              → "⌘K"   / "Alt+K"
//   formatShortcut("G", { mod: true, shift: true }) → "⌘⇧G" / "Alt+Shift+G"
//   formatShortcut("Enter")                         → "Enter"
// The key is upper-cased for single letters; glyphs (⏎, ., 1–9) pass through.
export function formatShortcut(key, opts) {
  return formatShortcutFor(isMac, key, opts);
}
