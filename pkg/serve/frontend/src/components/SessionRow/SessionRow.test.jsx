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
// The dot is a child component, so its class lives one call away. The row's
// own responsibility is the state it hands over.
const findDot = (node) => {
  if (!node || typeof node !== "object") return null;
  if (Array.isArray(node)) {
    for (const child of node) {
      const hit = findDot(child);
      if (hit) return hit;
    }
    return null;
  }
  if (node.type?.name === "Dot") return node;
  return findDot(node.props?.children);
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
  const tree = SessionRow({ title: "CI rojo en main", state: "idle", when: "1m", origin: "event:autoprueba" });
  expect(findByClass(tree, "zl-row-origin-event")).toBeTruthy();
  expect(findByClass(tree, "zl-row-origin")).toBeNull();
  expect(textOf(tree)).not.toContain("event:");
  expect(JSON.stringify(tree).includes("started by event:autoprueba")).toBe(true);
});

test("a non-event origin keeps its text badge", () => {
  const tree = SessionRow({ title: "nightly", state: "idle", origin: "automation" });
  expect(textOf(findByClass(tree, "zl-row-origin"))).toBe("automation");
  expect(findByClass(tree, "zl-row-origin-event")).toBeNull();
});

// The second line is the reason the session wants you, and its colour is the
// state it names — carried on the line itself rather than on the whole row, so
// a reason can be coloured without tinting the title beside it.
test("the reason line carries the tone of the state it names", () => {
  const tree = SessionRow({ title: "Deploy", state: "permission", brief: "Needs your answer", briefTone: "yellow" });
  const brief = findByClass(tree, "zl-row-brief tone-yellow");
  expect(textOf(brief)).toBe("Needs your answer");
});

test("a running row reports in the neutral tone, not in an attention colour", () => {
  const tree = SessionRow({ title: "ws race fix", state: "running", brief: "Running · 4m", briefTone: "neutral" });
  expect(findByClass(tree, "zl-row-brief tone-neutral")).toBeTruthy();
  expect(findByClass(tree, "zl-row-brief tone-yellow")).toBeNull();
  expect(findByClass(tree, "zl-row-brief tone-red")).toBeNull();
});

// A caller that has no tone to give (the inbox's candidate rows) must still get
// a plain brief, or every surface sharing this component would have to be
// changed at once.
test("a brief with no tone renders unclassed", () => {
  const tree = SessionRow({ title: "x", state: "idle", brief: "Running checks" });
  expect(textOf(findByClass(tree, "zl-row-brief"))).toBe("Running checks");
});

// A working row's second line is its brief, not its path, so nothing on it
// said which project the session belongs to. The name rides at the end of that
// line and reaches the accessible name too, since the path it stands in for
// was never read out either.
test("a row with a brief names its project at the end of that line, and in its name", () => {
  const tree = SessionRow({ title: "x", state: "running", brief: "Running · 4m", briefTone: "neutral", project: "moa" });
  expect(textOf(findByClass(tree, "zl-row-proj zl-data"))).toBe("moa");
  expect(findByClass(tree, "zl-row").props["aria-label"]).toBe("x, in moa");
  // Without a project there is no empty slot at the end of the line.
  expect(findByClass(SessionRow({ title: "x", state: "running", brief: "Running" }), "zl-row-proj zl-data")).toBeNull();
});

// State and age share the trailing slot so the dot lands on the same x in every
// row; the age alone must not push it out of the column.
test("state and age travel together at the end of the title line", () => {
  const tree = SessionRow({ title: "x", state: "error", when: "18m" });
  const meta = findByClass(tree, "zl-row-meta");
  expect(meta).toBeTruthy();
  expect(textOf(meta)).toBe("18m");
});

// The row IS the button: one hit target for the whole row, which is what gives
// a thumb its 52px. The close ✕ cannot nest inside another button, so it is a
// sibling — and the slot around them is the only thing that is not the row.
test("the whole row is one button, with the close action as its sibling", () => {
  const withClose = SessionRow({ title: "x", state: "idle", onClose: () => {} });
  const row = findByClass(withClose, "zl-row");
  expect(row.type).toBe("button");
  expect(findByClass(withClose, "zl-row-x").type).toBe("button");
  // Without onClose there is no second button at all: a row you cannot close
  // must not leave an invisible target on top of the one you can press.
  expect(findByClass(SessionRow({ title: "x", state: "idle" }), "zl-row-x")).toBeNull();
});

// The state word that reaches the dot is PRODUCTION's, not the prototype's:
// the catalogue called a waiting permission "needs", and a row that translated
// at its door would be the drift this migration removed. Asserted on the state
// the row hands to <Dot>, which is what this component decides — the class is
// one component call away, where the walker cannot reach.
test("the dot is given production's own state word", () => {
  const dotState = (props) => findDot(SessionRow(props))?.props.state;
  expect(dotState({ title: "x", state: "permission" })).toBe("permission");
  expect(dotState({ title: "x", state: "saved" })).toBe("saved");
});

// An unread answer is its own display state, whatever the run did afterwards,
// and a colour-only mark is not an announcement: the name has to say it too.
test("an unread result shows as unseen and says so in the accessible name", () => {
  const tree = SessionRow({ title: "x", state: "idle", unseen: true });
  expect(findDot(tree).props.state).toBe("unseen");
  expect(JSON.stringify(tree)).toContain("new result");
});
