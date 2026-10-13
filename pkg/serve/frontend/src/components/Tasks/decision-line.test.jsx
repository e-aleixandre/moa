import { test, expect, mock, beforeEach } from "bun:test";

// The pieces are walked as vnode trees (no DOM), so the hooks are stubbed:
// `pick` decides what each useState returns, in call order.
const { hooks, runtime } = await import("./hook-runtime.js");
mock.module("preact/hooks", () => hooks);
let pick = () => undefined;
let calls = 0;
beforeEach(() => {
  runtime.useState = (initial) => {
    const chosen = pick(calls++);
    return [chosen === undefined ? (typeof initial === "function" ? initial() : initial) : chosen, () => {}];
  };
  runtime.useRef = (initial) => ({ current: initial });
});

const { MoveList, CompleteNote } = await import("./parts.jsx");
const { MobileTasksView } = await import("./MobileTasksView.jsx");
const { TaskDetail } = await import("./TaskDetail.jsx");
const { store, setState, TASKS_INITIAL } = await import("../../data/store.js");
const { peekDraft, takeDraft } = await import("../../data/tasks.js");

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

test("phone creation inside a session filter targets that session, like desktop", () => {
  const previous = store.get();
  setState({ tasks: { ...TASKS_INITIAL, session: "live" }, sessions: SESSIONS });
  calls = 0;
  // The first three states subscribe to tasks, the directory and owners.
  pick = (i) => (i === 3 ? [{ kind: "new" }] : undefined);
  try {
    const editor = flat(MobileTasksView({ onBack() {} })).find((n) => n.type === TaskDetail);
    expect(editor).toBeDefined();
    expect(editor.props.newDest).toEqual({ place: "agent", sessionId: "live" });
  } finally {
    setState(previous);
    pick = () => undefined;
  }
});

test("opening Where on an existing phone task stashes its unsaved edits", () => {
  const previous = store.get();
  const draft = { title: "Unsaved title", description: "Unsaved details", subtasks: [{ title: "Keep it", done: false }], waits_for: [] };
  setState({ tasks: { ...TASKS_INITIAL, details: { [TASK.id]: TASK } }, sessions: SESSIONS });
  calls = 0;
  // After the three store subscriptions: stash, base, then the draft.
  pick = (i) => (i === 5 ? draft : undefined);
  let pushed = false;
  try {
    const tree = TaskDetail({ taskId: TASK.id, phone: true, onPushMove: () => { pushed = true; } });
    flat(tree).find((n) => n.type === "button" && n.props.class === "tk-prop-btn").props.onClick();
    expect(pushed).toBe(true);
    expect(peekDraft(TASK.id)?.draft).toEqual(draft);
    expect(takeDraft(TASK.id)?.base).toEqual(TASK);
  } finally {
    takeDraft(TASK.id);
    setState(previous);
    pick = () => undefined;
  }
});

test("project search reports the matches beyond its result cap", () => {
  const sessions = Object.fromEntries(Array.from({ length: 45 }, (_, i) => {
    const id = `s${i}`;
    return [id, { id, title: `Matching session ${i}`, state: "saved", cwd: "/d/project" }];
  }));
  calls = 0;
  pick = (i) => (i === 0 ? "Matching" : i === 2 ? "p" : undefined);
  const tree = expand(MoveList({ task: TASK, projects: [{ key: "p", cwd: "/d/project" }], sessions, onPick() {} }));
  expect(buttons(tree).filter((b) => b.label.startsWith("Matching session"))).toHaveLength(40);
  expect(text(tree)).toContain("5 more. Keep typing to narrow it.");
});

test("a new task picks a saved destination directly; creation owns the delivery choice", () => {
  const picks = [];
  calls = 0;
  pick = (i) => (i === 0 ? "Saved one" : undefined);
  const tree = expand(MoveList({ task: TASK, projects: [], sessions: SESSIONS, direct: true, phone: true, onPick: (...args) => picks.push(args) }));
  buttons(tree).find((b) => b.label.startsWith("Saved one")).node.props.onClick();
  expect(picks).toEqual([[{ place: "agent", sessionId: "gone" }, null]]);
  expect(text(tree)).not.toContain("Assign and wake");
});

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

// ── Tasks between owners ─────────────────────────────────────────────────

test("Can't do stays disabled until there is a reason", () => {
  const done = [];
  calls = 0;
  pick = () => undefined;
  const empty = expand(CompleteNote({ who: "moa", label: "Can't do", required: true, onCancel() {}, onDone: (note) => done.push(note) }));
  const btn = (tree) => buttons(tree).find((b) => b.label.startsWith("Can't do")).node;
  expect(btn(empty).props.disabled).toBe(true);
  calls = 0;
  pick = (i) => (i === 0 ? "pve01 has no RAM" : undefined);
  const filled = expand(CompleteNote({ who: "moa", label: "Can't do", required: true, onCancel() {}, onDone: (note) => done.push(note) }));
  expect(btn(filled).props.disabled).toBe(false);
  btn(filled).props.onClick();
  expect(done).toEqual(["pve01 has no RAM"]);
});

const OWNERS = [{ id: "own_moa", name: "moa", session_id: "s_moa" }, { id: "own_infra", name: "Infra", session_id: "s_infra" }];
const HANDOFF = { from_owner_id: "own_moa", from_owner_name: "moa", to_owner_id: "own_infra", to_owner_name: "Infra" };

function renderDetail(task, owners = OWNERS) {
  const previous = store.get();
  setState({ tasks: { ...TASKS_INITIAL, details: { [task.id]: task } }, sessions: SESSIONS, owners: { ...previous.owners, list: owners, loaded: true } });
  calls = 0;
  pick = () => undefined;
  try {
    return TaskDetail({ taskId: task.id, onPushMove() {} });
  } finally {
    setState(previous);
  }
}

test("a request waiting for permission answers Send, Always allow or Decline, and never moves", () => {
  const tree = renderDetail({ ...TASK, id: 7, requester_session_id: "s_moa", handoff: { ...HANDOFF, state: "awaiting_permission" } });
  const labels = buttons(tree).map((b) => b.label);
  expect(labels).toEqual(expect.arrayContaining(["Send to Infra", "Always allow moa → Infra", "Decline"]));
  expect(labels.some((l) => l.startsWith("Move"))).toBe(false);
  expect(labels.some((l) => l.startsWith("Done"))).toBe(false);
  expect(flat(tree).some((n) => n.type === "button" && n.props.class === "tk-prop-btn")).toBe(false);
});

test("a sent task closes as Done or Can't do and has no Move", () => {
  const tree = renderDetail({ ...TASK, id: 8, place: "agent", assignee_session_id: "s_infra", requester_session_id: "s_moa", handoff: { ...HANDOFF, state: "sent" } });
  const labels = buttons(tree).map((b) => b.label.replace(/Alt\+↵|⌘↵/, ""));
  expect(labels).toEqual(expect.arrayContaining(["Can't do", "Done"]));
  expect(labels.some((l) => l.startsWith("Move"))).toBe(false);
});

test("sent to an owner that is gone: nobody will receive it, Can't do or delete", () => {
  const tree = renderDetail({ ...TASK, id: 9, place: "agent", assignee_session_id: "s_infra", requester_session_id: "s_moa", handoff: { ...HANDOFF, state: "sent" } }, [OWNERS[0]]);
  expect(text(tree)).toContain("Nobody will receive this");
  const labels = buttons(tree).map((b) => b.label.replace(/Alt\+↵|⌘↵/, ""));
  expect(labels).toContain("Can't do");
  expect(labels).not.toContain("Done");
  expect(flat(tree).some((n) => n.type === "button" && n.props["aria-label"] === "Delete task")).toBe(true);
});
