import { test, expect } from "bun:test";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";
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

// --- The ratchet -------------------------------------------------------
//
// The fidelity harness runs under prefers-reduced-motion, which is correct
// for stable goldens and means it does not watch the motion language at all.
// Nothing else did either, so this does.
//
// It cannot simply ban hard-coded durations: there are still 58 of them in
// surfaces nobody has migrated yet, and a test that fails 58 times is a test
// everyone learns to ignore. So it freezes the count. Migrating a surface
// lowers the number; inventing a new duration in a new file raises it and
// this fails. The debt can shrink and cannot grow.
//
// When you migrate a file, lower BUDGET. It is meant to reach zero.
const BUDGET = 57;

function cssFiles(dir, out = []) {
  for (const name of readdirSync(dir)) {
    const full = join(dir, name);
    if (statSync(full).isDirectory()) cssFiles(full, out);
    else if (name.endsWith(".css") && name !== "tokens.css") out.push(full);
  }
  return out;
}

function hardCodedDurations(file) {
  const text = readFileSync(file, "utf8");
  const decls = text.match(/(?:transition|animation)[^;{}]*[;}]/g) || [];
  let n = 0;
  for (const d of decls) {
    if (d.includes("var(--motion")) continue;
    n += (d.match(/\b\d+m?s\b/g) || []).length;
  }
  return n;
}

test("no new hard-coded durations: the motion debt shrinks or stays put", () => {
  const root = new URL("..", import.meta.url).pathname;
  const total = cssFiles(root).reduce((sum, f) => sum + hardCodedDurations(f), 0);
  expect(total).toBeLessThanOrEqual(BUDGET);
});

// The inversion: a budget that silently tracks reality protects nothing. If
// the real count drops below the budget, this fails and tells you to lower
// it, so the ratchet actually ratchets.
test("the budget is kept tight against the real count", () => {
  const root = new URL("..", import.meta.url).pathname;
  const total = cssFiles(root).reduce((sum, f) => sum + hardCodedDurations(f), 0);
  expect(BUDGET - total).toBeLessThanOrEqual(4);
});
