import { test, expect } from "bun:test";

const css = await Bun.file(new URL("./UserWaypoint.css", import.meta.url)).text();
const jsx = await Bun.file(new URL("./UserWaypoint.jsx", import.meta.url)).text();

// These guard three decisions the owner made about the user's message. Each
// one is mechanical -- a reviewer cannot reliably catch a regression by
// reading a diff -- and each was a real defect before, not a hypothetical.

// ── rewind is anchored to the cell, not to the measure ────────────────────
// Measured on the old full-measure row: 311px from the end of a short message
// on the desktop, and a 27px OVERLAP with the text on the phone. Anchoring it
// inside the cell is what fixes both, so the anchor is what is asserted.

test("rewind is positioned inside the cell, not on a full-measure row", () => {
  // The rail is absolutely positioned, which only means anything if its
  // containing block is the cell -- so the cell must establish one.
  expect(css).toMatch(/\.zl-user-cell\s*\{[^}]*position:\s*relative\s*;/s);
  expect(css).toMatch(/\.zl-user-rail\s*\{[^}]*position:\s*absolute\s*;/s);
  expect(css).toMatch(/\.zl-user-rail\s*\{[^}]*right:/s);
});

test("the rewind button lives inside the cell element in the markup", () => {
  const cell = jsx.indexOf('class="zl-user-cell"');
  const rail = jsx.indexOf('class="zl-user-rail"');
  const rewind = jsx.indexOf('class="wp-rewind"');
  expect(cell).toBeGreaterThan(-1);
  expect(rail).toBeGreaterThan(cell);
  expect(rewind).toBeGreaterThan(rail);
});

test("the old full-measure timestamp row is gone, not left empty", () => {
  expect(css).not.toContain("zl-user-when");
  expect(jsx).not.toContain("zl-user-when");
});

// ── no copy on the user's own message ─────────────────────────────────────
// Copying is the assistant turn's foot. A copy control beside every message
// the owner ever typed is the repetition he rejected, so its absence here is
// a decision and not an omission.

test("the user's message carries no copy control", () => {
  expect(jsx).not.toMatch(/copyToClipboard|navigator\.clipboard/);
  expect(jsx).not.toMatch(/\bCopy\b.*from "lucide-preact"|Copy as CopyIcon/);
  expect(css).not.toMatch(/\.zl-user[^{]*\bcopy\b/i);
});

// ── the gutter keeps one width whatever the hour says ─────────────────────
// `09:14` and `11:08` must occupy the same box, and nothing else may share
// the gutter: the date is revealed on demand, not stacked above the hour.

test("the hour is mono and tabular, so every hour is the same width", () => {
  expect(css).toMatch(/\.zl-user-hhmm\s*\{[^}]*font-variant-numeric:\s*tabular-nums\s*;/s);
  expect(css).toMatch(/\.zl-user\s+\.zl-data\s*\{[^}]*font-family:\s*var\(--mono\)\s*;/s);
});

test("the gutter draws the hour and nothing else", () => {
  // A second mono line under a 44px gutter read as cramped, so the date left
  // the gutter entirely. Both the element and its rule must be gone, or it
  // comes back the next time someone has a date to show.
  expect(jsx).not.toContain('zl-user-day');
  expect(css).not.toContain('.zl-user-day');
  expect(jsx).toContain('class="zl-user-hhmm zl-data"');
});

test("the full date is reachable by pointer AND by keyboard", () => {
  // A title attribute alone is a mouse-only affordance; the hour is focusable
  // and carries the same string as its accessible name.
  expect(jsx).toMatch(/title=\{full \|\| undefined\}/);
  expect(jsx).toMatch(/aria-label=\{full \|\| undefined\}/);
  expect(jsx).toMatch(/tabIndex=\{full \? 0 : undefined\}/);
  expect(css).toMatch(/\.zl-user-hhmm:focus-visible\s*\{[^}]*outline:/s);
});

test("the gutter is 64px on the desktop and 44px on the phone", () => {
  expect(css).toMatch(/\.zl-user\s*\{[^}]*grid-template-columns:\s*64px\s+1fr\s*;/s);
  expect(css).toMatch(/\.mconv\s+\.zl-user\s*\{[^}]*grid-template-columns:\s*44px\s+1fr\s*;/s);
});

// ── the tokens rule: one source, no inline fallbacks ──────────────────────

test("no token is referenced with an inline fallback", () => {
  // `var(--x, <anything>)` -- the form that lets a sheet carry its own private
  // copy of a value tokens.css is supposed to own.
  const withFallback = [...css.matchAll(/var\(\s*--[\w-]+\s*,/g)].map((m) => m[0]);
  expect(withFallback).toEqual([]);
});

test("peach is no longer part of the user message's own chrome", () => {
  // The cell, the gutter, the hour and the rewind: none of them wear it.
  const ownChrome = css.slice(0, css.indexOf("/* the preview reference"));
  expect(ownChrome).not.toMatch(/var\(--peach\)/);
});
