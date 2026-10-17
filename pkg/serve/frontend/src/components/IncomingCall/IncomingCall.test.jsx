import { test, expect, mock } from "bun:test";

mock.module("preact/hooks", () => ({
  useState(initial) { return [typeof initial === "function" ? initial() : initial, () => {}]; },
  useEffect() {}, useLayoutEffect() {}, useRef(initial) { return { current: initial }; },
  useCallback(cb) { return cb; }, useMemo(f) { return f(); }, useDebugValue() {}, useContext(c) { return c?._defaultValue; },
  useReducer(r, initial) { return [initial, () => {}]; }, useErrorBoundary() { return [undefined, () => {}]; }, useId() { return "id"; },
  useImperativeHandle() {},
}));

const { IncomingCallView, HeldCallView } = await import("./IncomingCall.jsx");
const { CallLevelField } = await import("../Owners/CallLevel.jsx");

function walk(node, visit) {
  if (Array.isArray(node)) return node.forEach((n) => walk(n, visit));
  if (!node || typeof node !== "object") return;
  visit(node);
  walk(node.props?.children, visit);
}
const find = (tree, pred) => { const out = []; walk(tree, (n) => { if (pred(n)) out.push(n); }); return out; };
const text = (n) => [n.props?.children].flat(5).filter((c) => typeof c === "string").join("").trim();

const req = { id: "r1", session_id: "s1", owner_id: "o1", owner_name: "Infra", reason: "Deploy is blocked", level: "ask_first", state: "ringing" };

test("incoming: the reason is shown and there are exactly two buttons, Answer and Write it instead", () => {
  let answered = 0; let wrote = 0;
  const tree = IncomingCallView({ request: req, owner: null, phone: false, onAnswer: () => { answered++; }, onWrite: () => { wrote++; } });
  const buttons = find(tree, (n) => n.type === "button");
  expect(buttons.map(text)).toEqual(["Answer", "Write it instead"]);
  buttons[0].props.onClick(); buttons[1].props.onClick();
  expect([answered, wrote]).toEqual([1, 1]);
  expect(find(tree, (n) => n.props?.class === "icall-reason").map(text)).toEqual(["Deploy is blocked"]);
  expect(find(tree, (n) => n.props?.class === "icall-verb").map(text)).toEqual(["wants to talk"]);
});

test("incoming: Ring says is calling", () => {
  const tree = IncomingCallView({ request: { ...req, level: "ring" }, owner: null, phone: true, onAnswer() {}, onWrite() {} });
  expect(find(tree, (n) => n.props?.class === "icall-verb").map(text)).toEqual(["is calling"]);
  expect(tree.props.class).toContain("is-phone");
});

test("held: Call and Cancel, with who and why", () => {
  const tree = HeldCallView({ request: { ...req, state: "held" } });
  expect(find(tree, (n) => n.props?.class === "hcall-title").map(text)).toEqual(["Infra wanted to talk"]);
  expect(find(tree, (n) => n.type === "button").map(text)).toEqual(["Call", "Cancel"]);
});

test("call level: three radios, Never by default", () => {
  const tree = CallLevelField({ owner: { id: "o1", name: "Infra" }, status: { default: "chatgpt-plan" } });
  const radios = find(tree, (n) => n.props?.role === "radio");
  expect(radios.map(text)).toEqual(["Never", "Ask first", "Ring"]);
  expect(radios.map((n) => n.props["aria-checked"])).toEqual([true, false, false]);
  expect(find(tree, (n) => n.type === "p").map(text)).toContain("This owner won't ask to call you.");
});

test("call level: the hint describes only the chosen level", () => {
  const hint = (call_level) => find(CallLevelField({ owner: { id: "o1", name: "Infra", call_level }, status: {} }), (n) => n.props?.class === "ow-hint").map(text);
  expect(hint("ask_first")).toEqual(["When it wants to talk, you get a notification. Call back if you want."]);
  expect(hint("ring")).toEqual(["When it wants to talk, your phone rings."]);
});

test("incoming: a failed Write shows a recoverable message on the card", () => {
  const tree = IncomingCallView({ request: req, owner: null, phone: true, onAnswer() {}, onWrite() {}, failure: "Could not write it instead. Try again." });
  expect(find(tree, (n) => n.props?.class === "icall-fail").map(text)).toEqual(["Could not write it instead. Try again."]);
  expect(find(tree, (n) => n.type === "button").map(text)).toEqual(["Answer", "Write it instead"]);
});
