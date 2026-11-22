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

// The count is "N queued" plus a droppable tail, in two nodes.
function countText(tree) {
  const count = descendants(tree).find((n) => String(n.props?.class || "").startsWith("queued-count zl-data"));
  const parts = (Array.isArray(count.props.children) ? count.props.children : [count.props.children]);
  return parts.map((p) => (typeof p === "string" ? p : p.props.children)).join("");
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
  expect(countText(tree)).toBe("3 queued · read at the next step");
  const traces = descendants(tree).filter((n) => String(n.props?.class || "").startsWith("queued-trace"));
  expect(traces).toHaveLength(3);
  expect(traces.map((t) => descendants(t).find((n) => n.props?.class === "queued-text").props.children))
    .toEqual(["check the drawer", "/model opus", "regenerate the bundle"]);
  // A command keeps its own face; an ordinary message does not.
  expect(traces[1].props.class).toContain("is-command");
  expect(traces[0].props.class).not.toContain("is-command");
});

// The marker's Bring back is a single gesture for the WHOLE queue: it names
// Alt+↑ because this row and that key are the same action.
test("Bring back is one gesture for the whole queue, with a pointer-only twin", () => {
  const tree = QueuedTail({ queue: [{ id: "q1", text: "only one" }], variant: "b" });
  const line = marker(tree);
  expect(line.props["aria-label"]).toBe("1 queued message — bring it back to the input");
  expect(line.props.title).toContain("Alt+↑");
  // Nothing is offered per message: no cancel, no edit, no reorder. The only
  // other button is the label side of the same gesture, hidden from assistive
  // tech and the tab order.
  const buttons = descendants(tree).filter((n) => n.type === "button");
  expect(buttons).toHaveLength(2);
  expect(buttons[0]).toBe(line);
  expect(buttons[1].props["aria-hidden"]).toBe("true");
  expect(buttons[1].props.tabIndex).toBe(-1);
});

test("the twin shares the arming of the count side", () => {
  const calls = [];
  const tree = QueuedTail({ queue: [{ id: "q1", text: "one" }], variant: "b", onBringBack: () => calls.push("recall") });
  const twin = descendants(tree).find((n) => n.props?.class === "queued-bring queued-act");
  twin.props.onPointerDown({ pointerId: 5 });
  twin.props.onClick({ pointerId: 5, detail: 1 });
  expect(calls).toHaveLength(1);
  // An inherited click on the twin does nothing either.
  twin.props.onClick({ pointerId: 6, detail: 1 });
  expect(calls).toHaveLength(1);
});

function redirectButton(tree) {
  return descendants(tree).find((n) => n.props?.class === "queued-redirect");
}

// Variant A: a separate "Redirect now" sibling, only while something is left to
// redirect. It acts on the whole queue and has its own arming.
test("variant A offers Redirect now while the queue can still be redirected", () => {
  const queue = [{ id: "q1", text: "one" }, { id: "q2", text: "/model opus", command: true }];
  const tree = QueuedTail({ queue, variant: "a" });
  const btn = redirectButton(tree);
  expect(btn.props["aria-label"]).toBe("Redirect now — the agent reads the queue before its next step");
  expect(btn.props.children).toBe("Redirect now");
  expect(countText(tree)).toBe("2 queued · read at the next step");
  // Placed between the count and the Bring back label.
  const order = descendants(tree).filter((n) => n.type === "button").map((b) => b.props.class);
  expect(order).toEqual(["queued-line", "queued-redirect", "queued-bring queued-act"]);

  // A tap fires it only with its own pointerdown; a click that began elsewhere
  // (or on the Bring back side) does not; the keyboard always does.
  const calls = [];
  const armed = QueuedTail({ queue, variant: "a", onRedirect: () => calls.push("redirect"), onBringBack: () => calls.push("recall") });
  const r = redirectButton(armed);
  r.props.onClick({ pointerId: 1, detail: 1 });
  expect(calls).toEqual([]);
  marker(armed).props.onPointerDown({ pointerId: 1 });
  r.props.onClick({ pointerId: 1, detail: 1 });
  expect(calls).toEqual([]);
  r.props.onPointerDown({ pointerId: 2 });
  r.props.onClick({ pointerId: 2, detail: 1 });
  expect(calls).toEqual(["redirect"]);
  r.props.onKeyDown({ key: " ", preventDefault() {} });
  expect(calls).toEqual(["redirect", "redirect"]);
});

test("variant B never offers Redirect now on the marker", () => {
  expect(redirectButton(QueuedTail({ queue: [{ id: "q1", text: "one" }], variant: "b" }))).toBeUndefined();
});

test("once every message is marked the count says redirecting and the button goes", () => {
  const tree = QueuedTail({
    queue: [{ id: "q1", text: "one", redirect: true }, { id: "q2", text: "two", redirect: true }],
    variant: "a",
  });
  expect(countText(tree)).toBe("2 queued · redirecting");
  const count = descendants(tree).find((n) => String(n.props?.class || "").startsWith("queued-count zl-data"));
  expect(count.props.class).toContain("is-redirecting");
  expect(redirectButton(tree)).toBeUndefined();
});

test("a queue with one message still unmarked keeps the plain count and the button", () => {
  const tree = QueuedTail({
    queue: [{ id: "q1", text: "one", redirect: true }, { id: "q2", text: "two" }],
    variant: "a",
  });
  expect(countText(tree)).toBe("2 queued · read at the next step");
  expect(redirectButton(tree)).toBeDefined();
});

// Commands and task notices are never marked server-side: they neither count
// toward "redirecting" nor keep the button alive.
test("commands and notices are ignored by the redirect state", () => {
  const onlyThose = QueuedTail({
    queue: [{ id: "c1", text: "/model opus", command: true }, { id: "n1", text: "notice", non_recallable: true }],
    variant: "a",
  });
  expect(redirectButton(onlyThose)).toBeUndefined();
  expect(countText(onlyThose)).toBe("2 queued · read at the next step");

  const marked = QueuedTail({
    queue: [{ id: "q1", text: "one", redirect: true }, { id: "c1", text: "/model opus", command: true }],
    variant: "a",
  });
  expect(countText(marked)).toBe("2 queued · redirecting");
  expect(redirectButton(marked)).toBeUndefined();
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
