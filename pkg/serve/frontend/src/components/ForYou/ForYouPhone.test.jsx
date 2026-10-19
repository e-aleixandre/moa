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
