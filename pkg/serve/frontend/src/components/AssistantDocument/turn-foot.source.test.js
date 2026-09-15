import { test, expect } from "bun:test";
import { readFileSync } from "node:fs";
import { turnFinalResponse } from "../../data/stream-model.js";

const jsx = readFileSync(new URL("./TurnFoot.jsx", import.meta.url), "utf8");
const css = readFileSync(new URL("./TurnFoot.css", import.meta.url), "utf8");
const stream = readFileSync(new URL("../../layout/Stream/ConversationStream.jsx", import.meta.url), "utf8");

const prose = (text) => ({ type: "prose", text });
const ledger = () => ({ type: "ledger", rows: [] });

// ── the foot marks the END of a turn ──────────────────────────────────────
// A running turn has no final response and no hour to stamp, so it has no
// foot. The foot appearing is what makes a turn read as finished, and that
// signal is destroyed the moment it also shows while the turn is live.

test("only a finished turn is given a foot, never a streaming one", () => {
  // The render is gated on the block kind, and 'streaming' is the live turn.
  expect(stream).toMatch(/block\.kind === "document" &&\s*\(?\s*<TurnFoot/s);
  // And nothing renders a TurnFoot unconditionally beside it.
  const feet = [...stream.matchAll(/<TurnFoot/g)];
  expect(feet).toHaveLength(1);
});

test("the foot with nothing to say renders nothing at all", () => {
  expect(jsx).toMatch(/if \(!hhmm && !text\) return null;/);
});

// ── what copy puts on the clipboard ───────────────────────────────────────
// "solo el último de sus mensajes, lo que sería la response final, no todo".

test("copies the closing prose, not the narration before the work", () => {
  expect(turnFinalResponse([
    prose("I'll hang it on the strip."),
    ledger(),
    prose("Done. It sits after the permission chip."),
  ])).toBe("Done. It sits after the permission chip.");
});

test("everything after the last tool row comes along, fence included", () => {
  const out = turnFinalResponse([
    prose("first"),
    ledger(),
    prose("What I did instead:"),
    prose("```css\n.a{}\n```"),
    prose("On the phone it goes in the capsule."),
  ]);
  expect(out).toBe("What I did instead:\n\n```css\n.a{}\n```\n\nOn the phone it goes in the capsule.");
});

test("a turn ending in a tool row copies the prose above it", () => {
  expect(turnFinalResponse([
    prose("Rerunning only the unit suite."),
    ledger(),
  ])).toBe("Rerunning only the unit suite.");
});

test("a turn that is only prose copies that prose", () => {
  expect(turnFinalResponse([prose("one"), prose("two")])).toBe("one\n\ntwo");
});

test("a turn with no words copies nothing", () => {
  expect(turnFinalResponse([ledger()])).toBe("");
  expect(turnFinalResponse([])).toBe("");
  expect(turnFinalResponse(undefined)).toBe("");
});

// ── the code block keeps its own copy ─────────────────────────────────────

test("the foot does not replace the code block's own copy button", () => {
  const codeBlock = readFileSync(new URL("../CodeBlock/CodeBlock.jsx", import.meta.url), "utf8");
  // The fence keeps its own control, and its scope stays the CODE alone --
  // `writeText(code)`, not the surrounding prose. Two units, two scopes.
  expect(codeBlock).toMatch(/class="copy"/);
  expect(codeBlock).toMatch(/clipboard\.writeText\(code\)/);
});

// ── cost and reach ────────────────────────────────────────────────────────

test("the foot is a line, not a bar: no background, no border, no block padding", () => {
  const rule = css.slice(css.indexOf(".zl-foot {"), css.indexOf("}", css.indexOf(".zl-foot {")));
  expect(rule).not.toMatch(/background|border|padding/);
  expect(rule).toMatch(/height:\s*20px/);
  expect(rule).toMatch(/margin-top:\s*6px/);
});

test("the block above the foot gives up its trailing margin, so it is not charged twice", () => {
  expect(css).toMatch(/\.zl-turn > :nth-last-child\(2\)\s*\{\s*margin-bottom:\s*0\s*;/);
});

test("the touch target is a halo that overflows the line instead of growing it", () => {
  // 20px of ink + 12px of halo on each side = 44px of target.
  expect(css).toMatch(/\.zl-foot-act\s*\{[^}]*width:\s*20px\s*;/s);
  expect(css).toMatch(/\.zl-foot-act::before\s*\{[^}]*inset:\s*-12px\s*;/s);
});

test("the foot reserves no horizontal space on the turn", () => {
  // Racimo: the cluster's width is its content's. Nothing pushes to the right
  // edge, which is what a rail would do and what the owner rejected.
  expect(css).not.toMatch(/margin-left:\s*auto/);
  expect(css).not.toMatch(/\.zl-turn\s*\{[^}]*padding-right/s);
});
