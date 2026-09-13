import { test, expect } from "bun:test";
import { readFileSync } from "node:fs";
import { MOTION } from "../hooks/motion.js";

// The motion language is declared once, in tokens.css, and mirrored once, in
// hooks/motion.js, for the code that has to wait for a transition. Two copies
// of a number is one copy too many unless something checks them against each
// other; this does.
const css = readFileSync(new URL("./tokens.css", import.meta.url), "utf8");

function token(name) {
  const m = css.match(new RegExp(`--${name}:\\s*([^;]+);`));
  if (!m) throw new Error(`token --${name} not declared in tokens.css`);
  return m[1].trim();
}

test("hooks/motion.js mirrors the --motion-* tokens", () => {
  expect(token("motion-ease")).toBe(MOTION.ease);
  expect(token("motion-ease-exit")).toBe(MOTION.easeExit);
  expect(token("motion-hover")).toBe(`${MOTION.hover}ms`);
  expect(token("motion-fast")).toBe(`${MOTION.fast}ms`);
  expect(token("motion-base")).toBe(`${MOTION.base}ms`);
  expect(token("motion-exit-fast")).toBe(`${MOTION.exitFast}ms`);
  expect(token("motion-exit-base")).toBe(`${MOTION.exitBase}ms`);
});

test("exits are shorter than their entrances (rule 2)", () => {
  expect(MOTION.exitFast).toBeLessThan(MOTION.fast);
  expect(MOTION.exitBase).toBeLessThan(MOTION.base);
});

test("the legacy names resolve to the language rather than to a second curve", () => {
  expect(token("ease")).toBe("var(--motion-ease)");
  expect(token("duration")).toBe("var(--motion-hover)");
  expect(token("zl-ease")).toBe("var(--motion-ease)");
});

test("reduced motion is honoured once, globally", () => {
  const at = css.indexOf("@media (prefers-reduced-motion: reduce)");
  expect(at).toBeGreaterThan(-1);
  const block = css.slice(at, css.indexOf("}", css.indexOf("}", at) + 1));
  expect(block).toContain("animation-duration: 0.01ms !important");
  expect(block).toContain("transition-duration: 0.01ms !important");
});
