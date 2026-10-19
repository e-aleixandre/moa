import { expect, mock, test } from "bun:test";
import { readFileSync } from "node:fs";

// Called as a plain function outside a render: declare the hooks it uses (see
// AskUserCard.selection.test.jsx for why the real ones are spread first).
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

const { AskContext, AskUserCard } = await import("./AskUserCard.jsx");

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

test("a long context is folded with More; a short one is shown whole", () => {
  const long = AskContext({ text: "one\ntwo\nthree\nfour\nfive" });
  expect(long.props.class).toContain("is-folded");
  expect(descendants(long).some((n) => n.type === "button" && n.props.children === "More")).toBe(true);
  const short = AskContext({ text: "Staging is green." });
  expect(short.props.class).not.toContain("is-folded");
  expect(descendants(short).some((n) => n.type === "button")).toBe(false);
});

test("the card shows the context under the question, and nothing without one", () => {
  const withCtx = descendants(AskUserCard({ question: "Ship?", context: "Staging is green.", options: [] }));
  expect(withCtx.some((n) => n.type === AskContext && n.props.text === "Staging is green.")).toBe(true);
  const without = descendants(AskUserCard({ question: "Ship?", options: [] }));
  expect(without.some((n) => n.type === AskContext)).toBe(false);
});

test("folded means four lines on the card, one line in a For you row (two on the phone)", () => {
  const card = readFileSync(new URL("./AskUserCard.css", import.meta.url), "utf8");
  expect(card).toMatch(/\.ask-ctx\.is-folded \.ask-ctx-t \{[^}]*-webkit-line-clamp: 4/);
  const fy = readFileSync(new URL("../ForYou/ForYou.css", import.meta.url), "utf8");
  expect(fy).toMatch(/\.fy-ctx \{[^}]*-webkit-line-clamp: 1/);
  expect(fy).toMatch(/\.fy-phone \.fy-ctx \{[^}]*-webkit-line-clamp: 2/);
  const row = readFileSync(new URL("../ForYou/ForYou.jsx", import.meta.url), "utf8");
  expect(row).toContain('item.questions?.[0]?.context && <span class="fy-ctx">');
});

test("answering from For you drops the row at once", () => {
  const src = readFileSync(new URL("../ForYou/ForYou.jsx", import.meta.url), "utf8");
  expect(src).toMatch(/forgetForYouItem\(item\.id\)/);
});

test("on the phone the pin answers in a bottom sheet; on the desktop it opens in place", async () => {
  const { AsyncAskPin } = await import("./AsyncAskPin.jsx");
  const { MobileSheet } = await import("../../layout/mobile/MobileSheet/MobileSheet.jsx");
  const session = { id: "s1", pendingAsk: { id: "q1", async: true, questions: [{ question: "Ship?", context: "Green." }] } };
  const phone = descendants(AsyncAskPin({ session, phone: true }));
  const sheet = phone.find((n) => n.type === MobileSheet);
  expect(sheet).toBeTruthy();
  expect(sheet.props.open).toBe(false);
  expect(phone.some((n) => n.props?.class === "tk-pin-bar")).toBe(true);
  const desktop = descendants(AsyncAskPin({ session }));
  expect(desktop.some((n) => n.type === MobileSheet)).toBe(false);
});
