import { expect, mock, test } from "bun:test";

// Rendered as plain functions outside a render, like AskContext.test.jsx.
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

const { Row } = await import("./ForYou.jsx");
const { AsyncAskPin } = await import("../AskUserCard/AsyncAskPin.jsx");

function descendants(node, result = []) {
  if (node == null || typeof node !== "object") return result;
  if (Array.isArray(node)) {
    node.forEach((child) => descendants(child, result));
    return result;
  }
  result.push(node);
  descendants(node.props?.children, result);
  return result;
}
const text = (tree) => descendants(tree).flatMap((n) => [n.props?.children].flat()).filter((c) => typeof c === "string").join(" ");

const tapItem = {
  kind: "question", id: "q1", session_id: "s1", since: new Date().toISOString(), async: true, requires_tap: true,
  questions: [{ question: "¿Despliego fix/webhook-retries @ 4be1c09 a producción?" }],
};

test("on the phone a For you row says Needs your tap in words, not only with a lock", () => {
  const row = { item: tapItem, owner: null, ownConversation: false, session: "Tienda" };
  expect(text(Row({ row, phone: true, open: false }))).toContain("Needs your tap");
});

test("on the phone the pin says Needs your tap in words", () => {
  const session = { id: "s1", pendingAsk: { id: "q1", async: true, requires_tap: true, questions: tapItem.questions } };
  const bar = descendants(AsyncAskPin({ session, phone: true })).find((n) => n.props?.class === "tk-pin-bar");
  expect(text(bar)).toContain("Needs your tap");
});

test("on the phone the pin never cuts the question to one line: it takes two lines, clamped on a span (WebKit ignores the clamp on a button), with its needs in one row below", async () => {
  const { readFileSync } = await import("node:fs");
  const session = { id: "s1", pendingAsk: { id: "q1", async: true, requires_tap: true, questions: tapItem.questions } };
  const nodes = descendants(AsyncAskPin({ session, phone: true }));
  const main = nodes.find((n) => n.props?.class === "aq-pin-main");
  expect(main).toBeTruthy();
  const inMain = descendants(main);
  expect(inMain.some((n) => n.props?.class === "tk-pin-t")).toBe(true);
  expect(inMain.some((n) => n.props?.class === "aq-pin-meta")).toBe(true);
  // The text lives in a span inside the button, and the clamp is on the span.
  const button = inMain.find((n) => n.props?.class === "tk-pin-t");
  expect(descendants(button.props.children).some((n) => n.props?.class === "aq-pin-q")).toBe(true);
  const css = readFileSync(new URL("../AskUserCard/AsyncAskPin.css", import.meta.url), "utf8");
  expect(css).toMatch(/\.aq-pin-q \{[^}]*-webkit-line-clamp: 2/);
  expect(css).not.toMatch(/\.tk-pin-t \{[^}]*-webkit-line-clamp/);
  expect(css).toMatch(/\.aq-pin-meta \{[^}]*flex-wrap: nowrap/);
});

test("the meta has no separator elements of its own: each item carries its dot, clipped at a line start", async () => {
  const { readFileSync } = await import("node:fs");
  const item = { ...tapItem, call_state: "pending", call_at: new Date(Date.now() + 8 * 60000).toISOString(), questions: [...tapItem.questions, { question: "Second?" }] };
  const tree = Row({ row: { item, owner: null, ownConversation: false, session: "Tienda" }, phone: true, open: false });
  const meta = descendants(tree).find((n) => n.props?.class === "fy-meta");
  const kids = [meta.props.children].flat(Infinity).filter(Boolean);
  expect(kids.every((k) => typeof k === "object" && /\bfy-mi\b/.test(k.props?.class || ""))).toBe(true);
  expect(text(meta)).not.toMatch(/^·|·\s*$/);
  const css = readFileSync(new URL("./ForYou.css", import.meta.url), "utf8");
  expect(css).toMatch(/\.fy-mi::before \{[^}]*content: "·"/);
  expect(css).toMatch(/\.fy-row-head \.fy-meta \{[^}]*margin-left: -14px;[^}]*clip-path: inset\(0 0 0 14px\)/);
});

test("an idle owner's question call can be stopped from the pin and the For you row; the question stays", async () => {
  const { StopQuestionCall } = await import("./QuestionCall.jsx");
  const { setState } = await import("../../data/store.js");
  const calling = { ...tapItem, call_state: "pending", call_at: new Date(Date.now() + 8 * 60000).toISOString() };
  const row = { item: calling, owner: null, ownConversation: true, session: "Infra" };
  const inRow = descendants(Row({ row, phone: true, open: true })).find((n) => n.type === StopQuestionCall);
  expect(inRow?.props.item).toMatchObject({ id: "q1", session_id: "s1" });
  const session = { id: "s1", pendingAsk: { id: "q1", async: true, call_state: "pending", call_at: calling.call_at, questions: tapItem.questions } };
  const inPin = descendants(AsyncAskPin({ session })).find((n) => n.type === StopQuestionCall);
  expect(inPin?.props.item).toMatchObject({ id: "q1", session_id: "s1", call_state: "pending" });

  setState({ sessions: { s1: { id: "s1", state: "idle" } } });
  const posts = [];
  const original = globalThis.fetch;
  globalThis.fetch = (path, opts = {}) => {
    if (opts.method === "POST") posts.push(path);
    return Promise.resolve(new Response(opts.method === "POST" ? null : JSON.stringify({ items: [], news: [] }), { status: opts.method === "POST" ? 204 : 200 }));
  };
  try {
    const button = StopQuestionCall({ item: calling });
    expect(text(button)).toContain("Don't call");
    button.props.onClick({ stopPropagation() {} });
    await new Promise((r) => setTimeout(r, 0));
    expect(posts).toEqual(["/api/sessions/s1/cancel"]);
    setState({ sessions: { s1: { id: "s1", state: "running" } } });
    expect(StopQuestionCall({ item: calling })).toBeNull();
    setState({ sessions: { s1: { id: "s1", state: "idle" } } });
    expect(StopQuestionCall({ item: { ...calling, call_state: "missed" } })).toBeNull();
  } finally {
    globalThis.fetch = original;
    setState({ sessions: {} });
  }
});

// The header's sheet must close when an action takes the user to a
// conversation, also the one already open (the P2 of the review): the row's
// buttons are pressed here, not read from the source.
// The buttons really navigate; the global store is put back so no other test
// sees the session they open.
async function untouched(run) {
  const { store, setState } = await import("../../data/store.js");
  const before = store.get();
  try { run(); } finally { setState(before); }
}
function press(tree, match) {
  const b = descendants(tree).find((n) => n.type === "button" && match(n));
  expect(b).toBeTruthy();
  b.props.onClick();
}
const rendered = (node) => (typeof node.type === "function" ? node.type(node.props) : node);

test("Open conversation and Enter password close the sheet through onLeave", () => untouched(() => {
  const secret = { kind: "secret", id: "s1", session_id: "s1", since: new Date().toISOString(), blocking: true, label: "token" };
  const row = { item: secret, owner: null, ownConversation: false, session: "Tienda" };
  let left = 0;
  const tree = Row({ row, open: true, phone: true, onLeave: () => { left++; } });
  press(tree, (n) => n.props.class === "fy-open");
  expect(left).toBe(1);
  const answer = descendants(tree).find((n) => typeof n.type === "function" && n.props.row);
  press(rendered(answer), (n) => String(n.props.class).includes("is-primary"));
  expect(left).toBe(2);
}));

test("a task request's Open in Tasks closes the sheet too", async () => {
  const { RequestRow } = await import("./Requests.jsx");
  await untouched(() => {
    const item = { kind: "task_request", id: "task:1", task_id: 1, task_revision: 1, session_id: "s1", since: new Date().toISOString(), title: "Review" };
    let left = 0;
    const tree = RequestRow({ row: { item, owner: null, ownConversation: false, session: "Tienda" }, open: true, phone: true, onLeave: () => { left++; } });
    press(tree, (n) => n.props["data-act"] === "tasks");
    expect(left).toBe(1);
  });
});
