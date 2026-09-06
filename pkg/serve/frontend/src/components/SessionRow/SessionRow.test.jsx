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
