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

test("on the phone the pin never cuts the question to one line: it wraps, with its needs below", async () => {
  const { readFileSync } = await import("node:fs");
  const session = { id: "s1", pendingAsk: { id: "q1", async: true, requires_tap: true, questions: tapItem.questions } };
  const nodes = descendants(AsyncAskPin({ session, phone: true }));
  const main = nodes.find((n) => n.props?.class === "aq-pin-main");
  expect(main).toBeTruthy();
  const inMain = descendants(main);
  expect(inMain.some((n) => n.props?.class === "tk-pin-t")).toBe(true);
  expect(inMain.some((n) => n.props?.class === "aq-pin-meta")).toBe(true);
  const css = readFileSync(new URL("../AskUserCard/AsyncAskPin.css", import.meta.url), "utf8");
  expect(css).toMatch(/\.aq-pin\.is-phone \.tk-pin-t \{[^}]*white-space: normal[^}]*-webkit-line-clamp: 3/);
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
