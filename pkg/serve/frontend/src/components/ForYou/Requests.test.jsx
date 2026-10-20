import { expect, mock, test } from "bun:test";

// Rendered as plain functions outside a render, like ForYouPhone.test.jsx;
// the buttons' handlers are pressed and the requests they send are read.
const realHooks = await import("preact/hooks");
mock.module("preact/hooks", () => ({
  ...realHooks,
  useState(initial) { return [typeof initial === "function" ? initial() : initial, () => {}]; },
  useEffect() {},
  useLayoutEffect() {},
  useRef(initial) { return { current: initial }; },
  useCallback(callback) { return callback; },
  useMemo(factory) { return factory(); },
}));

const { RequestRow, Requests, groupByProject } = await import("./Requests.jsx");
const { forYouCount, forYouSlice, loadForYou, __resetForYouForTests } = await import("../../data/for-you.js");
const { forYouForSession } = await import("../../data/for-you-session.js");
const { store, setState } = await import("../../data/store.js");
const { closeTasksView } = await import("../../data/tasks-view.js");
const { navigate } = await import("../../data/router.js");

function descendants(node, result = []) {
  if (node == null || typeof node !== "object") return result;
  if (Array.isArray(node)) { node.forEach((c) => descendants(c, result)); return result; }
  result.push(node);
  descendants(node.props?.children, result);
  return result;
}
const button = (tree, act) => descendants(tree).find((n) => n.type === "button" && n.props["data-act"] === act);
const tick = () => new Promise((r) => setTimeout(r, 0));

const DAY = 86400000;
const req = (id, days, extra = {}) => ({
  kind: "task_request", id: `task:${id}`, task_id: id, task_revision: 3, session_id: "s1",
  since: new Date(Date.now() - days * DAY).toISOString(), title: `Request ${id}`, project: "winerim", ...extra,
});
const question = { kind: "question", id: "q1", session_id: "s1", since: new Date().toISOString(), async: true, questions: [{ question: "Ship?" }] };
const rowOf = (item) => ({ item, owner: null, ownConversation: false, session: "Albaranes" });

// fetchLog answers the tasks API with `status` and GET /api/for-you with
// `listed`, recording every request.
function withFetch({ status = 200, listed = [] }, run) {
  const calls = [];
  const original = globalThis.fetch;
  globalThis.fetch = (path, opts = {}) => {
    const method = opts.method || "GET";
    calls.push({ method, path, body: opts.body ? JSON.parse(opts.body) : null });
    if (method === "GET") return Promise.resolve(new Response(JSON.stringify({ items: listed, news: [] }), { status: 200 }));
    const body = status === 409 ? JSON.stringify({ error: "conflict" }) : status === 204 ? null : "{}";
    return Promise.resolve(new Response(body, { status }));
  };
  const before = store.get();
  __resetForYouForTests();
  return Promise.resolve(run(calls)).finally(() => { globalThis.fetch = original; setState(before); });
}

test("the amber counts only what asks or stops a session, never a task request", () => {
  const state = { forYou: { items: [question, req(1, 3), req(2, 12)], news: [], loaded: true }, sessions: { s1: { id: "s1" } } };
  expect(forYouCount(state)).toBe(1);
  expect(forYouForSession(state, "s1", []).waiting).toBe(1);
});

test("Done in a request's row closes the task with the revision shown, and the row leaves at once", () =>
  withFetch({ status: 200, listed: [req(7, 3)] }, async (calls) => {
    setState({ forYou: { items: [req(7, 3)], news: [], loaded: true } });
    button(RequestRow({ row: rowOf(req(7, 3)), phone: true, open: true }), "done").props.onClick({ stopPropagation() {} });
    expect(forYouSlice(store.get()).items).toEqual([]);
    // A GET that answers before the PATCH does must not bring it back.
    await loadForYou();
    expect(forYouSlice(store.get()).items).toEqual([]);
    await tick();
    expect(calls.find((c) => c.method === "PATCH")).toEqual({ method: "PATCH", path: "/api/tasks/7", body: { revision: 3, status: "done" } });
  }));

test("Dismiss on the desktop row deletes the request at the revision shown", () =>
  withFetch({ status: 204 }, async (calls) => {
    setState({ forYou: { items: [req(8, 3)], news: [], loaded: true } });
    button(RequestRow({ row: rowOf(req(8, 3)), phone: false, open: false }), "dismiss").props.onClick({ stopPropagation() {} });
    await tick();
    expect(calls.find((c) => c.method === "DELETE")?.path).toBe("/api/tasks/8?revision=3");
    expect(forYouSlice(store.get()).items).toEqual([]);
  }));

test("a 409 brings the request back as it is now", () =>
  withFetch({ status: 409, listed: [req(9, 3, { task_revision: 4, title: "Edited" })] }, async () => {
    setState({ forYou: { items: [req(9, 3)], news: [], loaded: true } });
    const { closeOne } = await import("./Requests.jsx");
    const outcome = await closeOne(req(9, 3), "done");
    expect(outcome).toBe("conflict");
    await tick(); await tick();
    expect(forYouSlice(store.get()).items.map((i) => [i.id, i.task_revision, i.title])).toEqual([["task:9", 4, "Edited"]]);
  }));

test("Open in Tasks leaves the sheet, and Back from Tasks returns to For you and reopens it", () => {
  const before = store.get();
  try {
    navigate("foryou");
    let left = 0; let reopened = 0;
    button(RequestRow({ row: rowOf(req(5, 3)), phone: true, open: true, onLeave: () => { left++; }, onReturn: () => { reopened++; } }), "tasks").props.onClick();
    expect(left).toBe(1);
    expect(store.get().view).toBe("tasks");
    closeTasksView();
    expect(store.get().view).toBe("foryou");
    expect(reopened).toBe(1);
  } finally { setState(before); }
});

test("requests group by project, newest first, with those older than a week apart", () => {
  const rows = [req(1, 19), req(2, 3), req(3, 4, { project: "infra" }), req(4, 9)].map(rowOf);
  const groups = groupByProject(rows);
  expect(groups.map((g) => [g.project, g.recent.map((r) => r.item.task_id), g.old.map((r) => r.item.task_id)]))
    .toEqual([["winerim", [2], [4, 1]], ["infra", [3], []]]);
});

test("dismissing the old requests asks first: the first tap sends nothing", () =>
  withFetch({ status: 204 }, async (calls) => {
    const tree = Requests({ rows: [req(1, 19), req(2, 3), req(4, 9), req(5, 1)].map(rowOf), phone: true, openId: null, setOpenId() {} });
    // More than three fold the list: open it as the user would.
    const fold = descendants(tree).find((n) => n.type === "button" && /fy-sec-fold/.test(n.props.class));
    expect(fold.props["aria-expanded"]).toBe(false);
    const open = Requests({ rows: [req(1, 19), req(2, 3), req(4, 9)].map(rowOf), phone: true, openId: null, setOpenId() {} });
    const ask = button(open, "ask-dismiss-old");
    expect(ask.props.children.join("")).toContain("Dismiss the 2 older than a week");
    ask.props.onClick();
    await tick();
    expect(calls.filter((c) => c.method === "DELETE")).toEqual([]);
  }));
