import { test, expect, mock } from "bun:test";

// The pieces are walked as vnode trees (no DOM), so the hooks are stubbed:
// `pick` decides what each useState returns, in call order.
const realHooks = await import("preact/hooks");
let pick = () => undefined;
let calls = 0;
mock.module("preact/hooks", () => ({
  ...realHooks,
  useState(initial) {
    const chosen = pick(calls++);
    return [chosen === undefined ? (typeof initial === "function" ? initial() : initial) : chosen, () => {}];
  },
  useEffect() {},
  useLayoutEffect() {},
  useRef(initial) { return { current: initial }; },
  useCallback(cb) { return cb; },
  useMemo(f) { return f(); },
}));

const { MoveList, CompleteNote } = await import("./parts.jsx");

function expand(node, depth = 0) {
  if (node == null || typeof node !== "object" || depth > 12) return node;
  if (Array.isArray(node)) return node.map((c) => expand(c, depth));
  if (typeof node.type === "function") return expand(node.type(node.props), depth + 1);
  return { ...node, props: { ...node.props, children: expand(node.props?.children, depth) } };
}
function flat(node, out = []) {
  if (node == null || typeof node !== "object") return out;
  if (Array.isArray(node)) { node.forEach((n) => flat(n, out)); return out; }
  out.push(node);
  flat(node.props?.children, out);
  return out;
}
function text(node) {
  if (node == null || node === false || node === true) return "";
  if (typeof node === "string" || typeof node === "number") return String(node);
  if (Array.isArray(node)) return node.map(text).join("");
  return text(node.props?.children);
}
const buttons = (tree) => flat(tree).filter((n) => n.type === "button").map((n) => ({ label: text(n).trim(), node: n }));

const SESSIONS = {
  live: { id: "live", title: "Live one", state: "idle", cwd: "/x/moa/main" },
  gone: { id: "gone", title: "Saved one", state: "saved", cwd: "/x/moa/main" },
};
const TASK = { id: 1, title: "t", place: "you", status: "pending", revision: 2 };

function renderMove(pending, onPick = () => {}) {
  calls = 0;
  // MoveList: useState 0 = query, 1 = pending session
  pick = (i) => (i === 1 ? pending : undefined);
  return expand(MoveList({ task: TASK, projects: [], sessions: SESSIONS, phone: true, onPick }));
}

test("assigning to a saved session confirms once, with wake and hold as the confirmation", () => {
  const picks = [];
  const tree = renderMove("gone", (dest, choice) => picks.push([dest.sessionId, choice]));
  const labels = buttons(tree).map((b) => b.label);
  expect(labels).not.toContain("Assign and notify");
  expect(labels).toContain("Assign and wake");
  expect(labels).toContain("Assign, notify when opened");
  expect(text(tree)).toContain("Saved one is saved");
  buttons(tree).find((b) => b.label === "Assign and wake").node.props.onClick();
  buttons(tree).find((b) => b.label === "Assign, notify when opened").node.props.onClick();
  expect(picks).toEqual([["gone", "wake"], ["gone", "hold"]]);
});

test("assigning to a live session is one Assign and notify, with no delivery choice", () => {
  const picks = [];
  const tree = renderMove("live", (dest, choice) => picks.push([dest.sessionId, choice]));
  const labels = buttons(tree).map((b) => b.label);
  expect(labels).toContain("Assign and notify");
  expect(labels).not.toContain("Assign and wake");
  buttons(tree).find((b) => b.label === "Assign and notify").node.props.onClick();
  expect(picks).toEqual([["live", null]]);
});

test("Done with a note to a saved session: the note's buttons are wake and hold", () => {
  const done = [];
  calls = 0;
  pick = (i) => (i === 0 ? "the key is in 1Password" : undefined);
  const tree = expand(CompleteNote({ who: "Saved one", saved: true, onCancel() {}, onDone: (note, choice) => done.push([note, choice]) }));
  const labels = buttons(tree).map((b) => b.label);
  expect(labels).not.toContain("Done");
  expect(labels).toEqual(expect.arrayContaining(["Done and wake", "Done, notify when opened", "Cancel"]));
  buttons(tree).find((b) => b.label === "Done, notify when opened").node.props.onClick();
  expect(done).toEqual([["the key is in 1Password", "hold"]]);
});

test("Done with a note to a live session is a single Done with no choice", () => {
  const done = [];
  calls = 0;
  pick = () => undefined;
  const tree = expand(CompleteNote({ who: "Live one", onCancel() {}, onDone: (note, choice) => done.push([note, choice]) }));
  const labels = buttons(tree).map((b) => b.label.replace(/Alt\+↵|⌘↵/, ""));
  expect(labels).toContain("Done");
  expect(labels).not.toContain("Done and wake");
  buttons(tree).find((b) => b.label.startsWith("Done")).node.props.onClick();
  expect(done).toEqual([["", null]]);
});
