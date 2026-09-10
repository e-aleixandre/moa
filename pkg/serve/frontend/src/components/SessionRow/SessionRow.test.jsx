import { test, expect } from "bun:test";
import { SessionRow } from "./SessionRow.jsx";

// Walks the returned vnode tree looking for a node with the given class.
const findByClass = (node, cls) => {
  if (!node || typeof node !== "object") return null;
  if (Array.isArray(node)) {
    for (const child of node) {
      const hit = findByClass(child, cls);
      if (hit) return hit;
    }
    return null;
  }
  if (node.props?.class === cls) return node;
  return findByClass(node.props?.children, cls);
};
const textOf = (node) => {
  if (node == null || node === false) return "";
  if (typeof node === "string" || typeof node === "number") return String(node);
  if (Array.isArray(node)) return node.map(textOf).join("");
  return textOf(node.props?.children);
};

// A session started by an event is marked, not labelled: "event:sentry-tienda"
// never fitted the badge and truncating it to "event:a…" named nothing.
test("an event origin is shown as a mark and keeps the source in the accessible name", () => {
  const tree = SessionRow({ variant: "card", title: "CI rojo en main", state: "idle", when: "1m", origin: "event:autoprueba" });
  expect(findByClass(tree, "origin-event")).toBeTruthy();
  expect(findByClass(tree, "origin")).toBeNull();
  expect(textOf(tree)).not.toContain("event:");
  expect(JSON.stringify(tree).includes("started by event:autoprueba")).toBe(true);
});

test("a non-event origin keeps its text badge", () => {
  const tree = SessionRow({ variant: "card", title: "nightly", state: "idle", origin: "automation" });
  expect(textOf(findByClass(tree, "origin"))).toBe("automation");
  expect(findByClass(tree, "origin-event")).toBeNull();
});

// The second line is the reason the session wants you, and its colour is the
// state it names — carried on the line itself rather than on the whole row, so
// a reason can be coloured without tinting the title beside it.
test("the reason line carries the tone of the state it names", () => {
  const tree = SessionRow({ variant: "card", title: "Deploy", state: "permission", brief: "Needs your answer", briefTone: "yellow" });
  const brief = findByClass(tree, "brief tone-yellow");
  expect(textOf(brief)).toBe("Needs your answer");
});

test("a running row reports in the neutral tone, not in an attention colour", () => {
  const tree = SessionRow({ variant: "card", title: "ws race fix", state: "running", brief: "Running · 4m", briefTone: "neutral" });
  expect(findByClass(tree, "brief tone-neutral")).toBeTruthy();
  expect(findByClass(tree, "brief tone-yellow")).toBeNull();
  expect(findByClass(tree, "brief tone-red")).toBeNull();
});

// A caller that has no tone to give (the inbox's candidate rows) must still get
// a plain brief, or every surface sharing this component would have to be
// changed at once.
test("a brief with no tone renders unclassed", () => {
  const tree = SessionRow({ variant: "card", title: "x", state: "idle", brief: "Running checks" });
  expect(textOf(findByClass(tree, "brief"))).toBe("Running checks");
});

// The monogram is the project's identity: the hue travels as a custom property
// so one class can paint any project without a rule per folder.
//
// This file walks the returned vnode tree WITHOUT rendering it, and the row
// hands the square to a <Monogram> child, so its text and style live one
// component call away where findByClass cannot reach. The assertion is on what
// this component is actually responsible for: the data it hands over, and
// handing over nothing when there is no project to name.
test("the monogram paints from the hue the caller derived, and is absent without one", () => {
  const tree = SessionRow({ variant: "card", title: "x", state: "idle", mono: { text: "mo", hue: 210 } });
  const json = JSON.stringify(tree);
  expect(json).toContain('"text":"mo"');
  expect(json).toContain('"hue":210');
  expect(JSON.stringify(SessionRow({ variant: "card", title: "x", state: "idle" }))).not.toContain('"mono"');
});

// State and age share the trailing slot so the dot lands on the same x in every
// row; the age alone must not push it out of the column.
test("state and age travel together at the end of the title line", () => {
  const tree = SessionRow({ variant: "card", title: "x", state: "error", when: "18m" });
  const edge = findByClass(tree, "edge");
  expect(edge).toBeTruthy();
  expect(textOf(edge)).toBe("18m");
});
