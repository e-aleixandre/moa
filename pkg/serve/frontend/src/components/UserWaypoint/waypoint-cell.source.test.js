import { test, expect } from "bun:test";

const css = await Bun.file(new URL("./UserWaypoint.css", import.meta.url)).text();
const jsx = await Bun.file(new URL("./UserWaypoint.jsx", import.meta.url)).text();

// These guard three decisions the owner made about the user's message. Each
// one is mechanical -- a reviewer cannot reliably catch a regression by
// reading a diff -- and each was a real defect before, not a hypothetical.

// ── rewind is anchored to the foot, not to the measure ────────────────────
// Measured on the old full-measure row: 311px from the end of a short message
// on the desktop, and a 27px OVERLAP with the text on the phone. Sharing the
// timestamp's foot keeps it a fixed short distance from its own message.

test("rewind is positioned on the timestamp foot, not on a full-measure row", () => {
  expect(css).toMatch(/\.zl-user-rail\s*\{[^}]*position:\s*absolute\s*;/s);
  expect(css).toMatch(/\.zl-user-rail\s*\{[^}]*left:\s*46px\s*;/s);
});

test("the rewind button lives beside the hour in the foot markup", () => {
  const cell = jsx.indexOf('class="zl-user-cell"');
  const foot = jsx.indexOf('class="zl-user-foot"');
  const rail = jsx.indexOf('class="zl-user-rail"');
  expect(cell).toBeGreaterThan(-1);
  expect(foot).toBeGreaterThan(cell);
  expect(rail).toBeGreaterThan(foot);
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

// ── the foot keeps one width whatever the hour says ───────────────────────
// `09:14` and `11:08` must occupy the same box, and nothing else may share
// the foot: the date is revealed on demand, not stacked above the hour.

test("the hour is mono and tabular, so every hour is the same width", () => {
  expect(css).toMatch(/\.zl-user-hhmm\s*\{[^}]*font-variant-numeric:\s*tabular-nums\s*;/s);
  expect(css).toMatch(/\.zl-user\s+\.zl-data\s*\{[^}]*font-family:\s*var\(--mono\)\s*;/s);
});

test("the foot draws the hour and nothing else", () => {
  // A second mono line in the foot would be cramped, so the date stays out of
  // it entirely. Both the element and its rule must be gone, or it
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

test("the foot shares the assistant's 20px line after 6px of air", () => {
  expect(css).toMatch(/\.zl-user-foot\s*\{[^}]*height:\s*20px\s*;[^}]*margin-top:\s*6px\s*;/s);
  expect(css).toMatch(/\.zl-user-foot\s*\{[^}]*grid-row:\s*2\s*;/s);
  expect(css).not.toMatch(/\.zl-user-foot\s*\{[^}]*padding-left:/s);
});

// ── the tokens rule: one source, no inline fallbacks ──────────────────────

test("no token is referenced with an inline fallback", () => {
  // `var(--x, <anything>)` -- the form that lets a sheet carry its own private
  // copy of a value tokens.css is supposed to own.
  const withFallback = [...css.matchAll(/var\(\s*--[\w-]+\s*,/g)].map((m) => m[0]);
  expect(withFallback).toEqual([]);
});

test("peach is no longer part of the user message's own chrome", () => {
  // The cell, the foot, the hour and the rewind: none of them wear it.
  const ownChrome = css.slice(0, css.indexOf("/* the preview reference"));
  expect(ownChrome).not.toMatch(/var\(--peach\)/);
});
