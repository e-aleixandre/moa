// voice-face.source.test.js — run with `bun test`.
//
// The owner could not tell "recording" from "ready to send" on his phone,
// because both wore the accent and only a 2px ring separated them. That is a
// visual bug, but it is testable as a source fact: the two faces must not share
// their fill. This guards the distinction so a later tidy-up cannot quietly
// collapse them back together.
//
// The mic and Send now sit SIDE BY SIDE on the composer's control row instead
// of taking turns on one button, so the distinction matters more, not less:
// the two faces are on screen at the same time. The assertions moved from
// `.zl-send.recording` (the phone's old takeover face, gone) to `.zl-mic`,
// which is the only button that records.
import { test, expect } from "bun:test";
import { readFileSync } from "node:fs";

const css = readFileSync(new URL("./Composer.css", import.meta.url), "utf8");

// rule — the declaration block of the first selector matching `name`.
function rule(name) {
  const i = css.indexOf(name);
  expect(i).toBeGreaterThan(-1);
  const open = css.indexOf("{", i);
  return css.slice(open + 1, css.indexOf("}", open));
}

test("recording is peach and send is the accent — never the same fill", () => {
  const recording = rule(".zl-mic.recording {");
  const armedSend = rule(".zl-composer.is-armed .zl-send:not(.recording):not(.transcribing) {");

  expect(recording).toMatch(/background:\s*var\(--zl-peach\)/);
  expect(armedSend).toMatch(/background:\s*var\(--zl-accent\)/);
  // The decisive assertion: neither borrows the other's colour family.
  expect(recording).not.toMatch(/--zl-accent/);
  expect(armedSend).not.toMatch(/--zl-peach/);
});

test("recording also differs in SHAPE, so it reads without relying on colour", () => {
  // A phone has no hover and a glance has no time to compare two hues. The
  // circle is the part of the answer that survives both.
  expect(rule(".zl-mic.recording {")).toMatch(/border-radius:\s*var\(--radius-full\)/);
  expect(rule(".zl-send {")).toMatch(/border-radius:\s*var\(--zl-r-md\)/);
});

test("red stays reserved: a live mic is not an error and not a destructive act", () => {
  // The reasoning the previous pass wrote down and got right; only the
  // ambiguity it left behind was wrong.
  expect(rule(".zl-mic.recording {")).not.toMatch(/--zl-red/);
});

test("the recording motion is disabled under reduced motion, and the state is not", () => {
  const reduced = css.slice(css.indexOf("@media (prefers-reduced-motion: reduce)"));
  expect(reduced).toMatch(/\.zl-mic\.recording/);
  // The halo is kept as a still box-shadow, so nothing that carries meaning
  // depends on the animation running.
  expect(reduced).toMatch(/box-shadow:\s*0 0 0 4px var\(--zl-peach-glow\)/);
});

test("every colour used by the recording face is a real token, with no var() fallbacks", () => {
  // Project rule: `var(--zl-*, fallback)` is banned — a fallback is a second
  // copy of a value that can drift from the token it shadows.
  expect(css).not.toMatch(/var\(--zl-[a-z0-9-]+\s*,/);
  const tokens = readFileSync(new URL("../../tokens/tokens.css", import.meta.url), "utf8");
  for (const name of ["--zl-peach", "--zl-peach-ink", "--zl-peach-line", "--zl-peach-glow"]) {
    expect(tokens).toMatch(new RegExp(`${name}:`));
  }
});
