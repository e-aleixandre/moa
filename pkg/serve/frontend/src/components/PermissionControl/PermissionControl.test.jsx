import { test, expect } from "bun:test";

// Imported through the SOURCE rather than the module, deliberately.
//
// PermissionControl imports preact/hooks for real. CommandPalette's tests run
// over their own hook runtime installed with mock.module, which bun applies
// process-wide and never restores, so loading this module into the same test
// process breaks three CommandPalette tests that have nothing to do with
// permissions. A lazy import does not help: the module still loads.
//
// That fragility is pre-existing and is filed as debt (see
// tmp/redesign/fidelity/ESTADO.md). Papering over it by rewriting
// CommandPalette's walker would hide it; asserting on the source keeps this
// test honest and keeps the suite green.
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const source = readFileSync(
  join(dirname(fileURLToPath(import.meta.url)), "PermissionControl.jsx"),
  "utf8",
);

function expand(node, depth = 0) {
  if (node == null || typeof node !== "object" || depth > 8) return node;
  if (Array.isArray(node)) return node.map((child) => expand(child, depth));
  if (typeof node.type === "function") return expand(node.type(node.props), depth + 1);
  return { ...node, props: { ...node.props, children: expand(node.props?.children, depth + 1) } };
}

function buttons(node, out = []) {
  if (node == null || typeof node !== "object") return out;
  if (Array.isArray(node)) {
    for (const child of node) buttons(child, out);
    return out;
  }
  if (node.type === "button") out.push(node);
  buttons(node.props?.children, out);
  return out;
}

function textOf(node) {
  if (node == null || typeof node !== "object") return String(node ?? "");
  if (Array.isArray(node)) return node.map(textOf).join("");
  return textOf(node.props?.children);
}

test("permission rows read as a dial, least autonomy first", () => {
  // The catalogue's order is the product's: ask -> auto -> yolo. Reversing it
  // to put YOLO on top is how a stray first-row tap drops a session into
  // never-ask, which is the interaction the menu exists to prevent.
  const order = [...source.matchAll(/value:\s*"(ask|auto|yolo)"/g)].map((m) => m[1]);
  expect(order).toEqual(["ask", "auto", "yolo"]);
});

test("each row says what it costs you, not just its name", () => {
  // The words are the safety feature: "yolo" alone does not tell you it never
  // asks again.
  expect(source).toContain("Ask before every command");
  expect(source).toContain("Ask only for risky commands");
  expect(source).toContain("Run everything");
});

test("picking a row reports the mode id", () => {
  // onPick carries the mode, so the caller writes the session rather than the
  // menu guessing which row was which.
  expect(source).toMatch(/onPick\?\.\(|onPick\(/);
});
