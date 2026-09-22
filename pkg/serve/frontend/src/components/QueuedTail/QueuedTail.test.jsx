import { expect, mock, test } from "bun:test";

// These tests call QueuedTail as a plain function, outside any render, and it
// keeps its pointer arming in a useRef. Spread the real hooks first: bun's
// mock.module replaces the module for the whole process and never restores it,
// so a factory that lists only some hooks deletes the rest for every file
// loaded afterwards.
const realHooks = await import("preact/hooks");
mock.module("preact/hooks", () => ({
  ...realHooks,
  useRef(initial) { return { current: initial }; },
}));

const { QueuedTail } = await import("./QueuedTail.jsx");

function descendants(node, result = []) {
  if (!node || typeof node === "string") return result;
  // The traces arrive as an array child (one .map), so arrays are walked
  // through rather than counted as nodes.
  if (Array.isArray(node)) {
    for (const child of node) descendants(child, result);
    return result;
  }
  result.push(node);
  const children = node.props?.children;
  for (const child of Array.isArray(children) ? children : [children]) descendants(child, result);
  return result;
}

function marker(tree) {
  return descendants(tree).find((node) => node.props?.class === "queued-line");
}

test("no queue, no marker", () => {
  expect(QueuedTail({ queue: null })).toBeNull();
  expect(QueuedTail({ queue: [] })).toBeNull();
});

test("the marker counts the queue and traces every message once, in order", () => {
  const tree = QueuedTail({
    queue: [
      { id: "q1", text: "check the drawer" },
      { id: "q2", text: "/model opus", command: true },
      { id: "q3", text: "regenerate the bundle" },
    ],
  });
  const count = descendants(tree).find((n) => String(n.props?.class || "").startsWith("queued-count"));
  expect(count.props.children).toBe("3 queued · read at the next step");
  const traces = descendants(tree).filter((n) => String(n.props?.class || "").startsWith("queued-trace"));
  expect(traces).toHaveLength(3);
  expect(traces.map((t) => descendants(t).find((n) => n.props?.class === "queued-text").props.children))
    .toEqual(["check the drawer", "/model opus", "regenerate the bundle"]);
  // A command keeps its own face; an ordinary message does not.
  expect(traces[1].props.class).toContain("is-command");
  expect(traces[0].props.class).not.toContain("is-command");
});

// The marker is a single gesture for the WHOLE queue: it names Alt+↑ because
// this row and that key are the same action.
test("the whole row is one gesture for the whole queue", () => {
  const tree = QueuedTail({ queue: [{ id: "q1", text: "only one" }] });
  const line = marker(tree);
  expect(line.props["aria-label"]).toBe("1 queued message — bring it back to the input");
  expect(line.props.title).toContain("Alt+↑");
  // Nothing is offered per message: no cancel, no edit, no reorder.
  const buttons = descendants(tree).filter((n) => n.type === "button");
  expect(buttons).toHaveLength(1);
});

// A touch tap fires pointerout/pointerleave BEFORE the click, so the arming
// must survive it; only its own pointerdown may authorise a recall, because a
// recall destroys the queued messages server-side.
test("a tap recalls, an inherited click does not, the keyboard always does", () => {
  const calls = [];
  const tree = QueuedTail({ queue: [{ id: "q1", text: "one" }], onBringBack: () => calls.push("recall") });
  const line = marker(tree);

  line.props.onPointerDown({ pointerId: 2 });
  line.props.onClick({ pointerId: 2, detail: 1 });
  expect(calls).toHaveLength(1);

  // A click whose gesture began elsewhere (no pointerdown here) is ignored.
  line.props.onClick({ pointerId: 3, detail: 1 });
  expect(calls).toHaveLength(1);

  // Enter on the focused row, and a synthesised activation (detail 0) from
  // assistive technology, are always honoured.
  let prevented = false;
  line.props.onKeyDown({ key: "Enter", preventDefault() { prevented = true; } });
  expect(prevented).toBe(true);
  line.props.onClick({ detail: 0 });
  expect(calls).toHaveLength(3);
});
