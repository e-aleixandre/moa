import { test, expect, mock, beforeEach, afterEach } from "bun:test";

// Review fixes for tasks between owners (R6–R10). Components are walked as
// vnode trees with the shared hook stand-in.
const { hooks, runtime } = await import("./hook-runtime.js");
mock.module("preact/hooks", () => hooks);
let setters = [];
beforeEach(() => {
  setters = [];
  runtime.useState = (initial) => {
    const i = setters.length;
    setters.push([]);
    return [typeof initial === "function" ? initial() : initial, (v) => setters[i].push(v)];
  };
  runtime.useRef = (initial) => ({ current: initial });
  runtime.useEffect = () => {};
});

const { PinnedTaskLine } = await import("./PinnedTaskLine.jsx");
const { TaskDetail } = await import("./TaskDetail.jsx");
const { TaskRow } = await import("./parts.jsx");
const { ThreadComposer } = await import("./Thread.jsx");
const { handoffOrphan } = await import("../../data/tasks-model.js");
const LIVE = [{ id: "own_moa", name: "moa" }];
const tasksData = await import("../../data/tasks.js");
const { store, setState, TASKS_INITIAL } = await import("../../data/store.js");

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
const labels = (tree) => flat(tree).filter((n) => n.type === "button").map((n) => text(n).trim());

let previous;
let previousFetch;
beforeEach(() => { previous = store.get(); previousFetch = globalThis.fetch; });
afterEach(() => { setState(previous); globalThis.fetch = previousFetch; });

// What GET /api/sessions/{id}/tasks answers for a request still waiting on
// the owner's permission (pkg/tasks TaskHandoff: names and state, no ids).
const PROJECTION = {
  checklist: [
    { id: 2, title: "Sent one", status: "done", comment_count: 1, handoff: { state: "sent", outcome: "cant_do", from_owner_name: "moa", to_owner_name: "Infra" } },
  ],
  requests: [
    { id: 1, title: "LXC for the demo", status: "pending", created_at: 1,
      handoff: { state: "awaiting_permission", from_owner_name: "moa", to_owner_name: "Infra" } },
  ],
  scheduled: [],
};

// ── R6 ────────────────────────────────────────────────────────────────────

test("R6: the real session projection makes the pinned line offer Answer, not Done", async () => {
  globalThis.fetch = async () => new Response(JSON.stringify(PROJECTION), { status: 200 });
  setState({ tasks: { ...TASKS_INITIAL } });
  await tasksData.loadSessionTasks("s-moa");
  const tree = expand(PinnedTaskLine({ sessionId: "s-moa", onOpenTask() {} }));
  expect(labels(tree)).toContain("Answer");
  expect(labels(tree)).not.toContain("Done");
});

test("R6: a projected row of a closed handoff reads Can't do and from the sender", async () => {
  globalThis.fetch = async () => new Response(JSON.stringify(PROJECTION), { status: 200 });
  setState({ tasks: { ...TASKS_INITIAL } });
  await tasksData.loadSessionTasks("s-moa");
  const row = store.get().tasks.bySession["s-moa"].checklist[0];
  const tree = expand(TaskRow({ task: row, owners: [], lookup: () => null, onSelect() {}, onCheck() {}, context: "session" }));
  expect(text(tree)).toContain("from moa");
  expect(flat(tree).some((n) => n.type === "button" && /Reopen/.test(n.props["aria-label"] || "") && /is-cant/.test(n.props.class || ""))).toBe(true);
});

test("R6: a projected handoff names owners without ids and is not taken for an orphan", () => {
  const projected = PROJECTION.checklist[0];
  expect(handoffOrphan({ ...projected, status: "pending" }, LIVE, true)).toBe(false);
});

// ── R7 / R8 ───────────────────────────────────────────────────────────────

const HANDOFF = { id: 7, title: "t", place: "you", status: "pending", revision: 1, requester_session_id: "s-moa",
  handoff: { state: "sent", from_owner_id: "own_moa", from_owner_name: "moa", to_owner_id: "own_infra", to_owner_name: "Infra" } };
const field = (tree) => flat(tree).find((n) => n.type === "textarea");

test("R7: an unsent reply is still there when the composer mounts again", () => {
  setState({ tasks: { ...TASKS_INITIAL } });
  const props = { task: HANDOFF, owners: [], sessions: {}, phone: false };
  field(expand(ThreadComposer(props))).props.onInput({ currentTarget: { value: "Keep this", scrollHeight: 10, style: {} } });
  expect(field(expand(ThreadComposer(props))).props.value).toBe("Keep this");
  expect(field(expand(ThreadComposer({ ...props, task: { ...HANDOFF, id: 8 } }))).props.value).toBe("");
});

test("R8: a POST that succeeds clears the reply only if it is still what was sent", async () => {
  setState({ tasks: { ...TASKS_INITIAL } });
  let release;
  globalThis.fetch = (path, opts) => {
    if (opts.method === "POST") return new Promise((r) => { release = () => r(new Response(JSON.stringify({ id: 1, body: "first" }), { status: 201 })); });
    return Promise.resolve(new Response(JSON.stringify({ comments: [] }), { status: 200 }));
  };
  tasksData.setThreadDraft(7, "first");
  const sent = tasksData.sendThreadDraft(7);
  tasksData.setThreadDraft(7, "second, typed while sending");
  release();
  await sent;
  expect(tasksData.threadDraftOf(store.get().tasks, 7)).toBe("second, typed while sending");

  tasksData.setThreadDraft(7, "third");
  const again = tasksData.sendThreadDraft(7);
  release();
  await again;
  expect(tasksData.threadDraftOf(store.get().tasks, 7)).toBe("");
});

// ── R9 ────────────────────────────────────────────────────────────────────
