import { expect, mock, test } from "bun:test";

const state = [];
let stateIndex = 0;
const resolved = [];

mock.module("preact/hooks", () => ({
  useState(initial) {
    const index = stateIndex++;
    if (!(index in state)) state[index] = typeof initial === "function" ? initial() : initial;
    return [state[index], (value) => { state[index] = typeof value === "function" ? value(state[index]) : value; }];
  },
  useEffect() {},
  useRef(initial) { return { current: initial }; },
  useCallback(fn) { return fn; },
}));
mock.module("../../data/session-actions.js", () => ({
  resolveAskUser: async (...args) => { resolved.push(args); },
}));
mock.module("../../hooks/useVoiceGesture.js", () => ({
  useVoiceGesture: () => ({ handlers: {}, recording: false, transcribing: false, locked: false, showSlideHint: false, supported: false }),
}));
mock.module("../../hooks/useCanTranscribe.js", () => ({ useCanTranscribe: () => false }));

const { AskUserPrompt } = await import("./AskUserPrompt.jsx");
const { AskUserCard } = await import("./AskUserCard.jsx");

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

function reset() {
  state.length = 0;
  stateIndex = 0;
  resolved.length = 0;
}

function render(session) {
  stateIndex = 0;
  return AskUserPrompt({ session });
}

function card(tree) {
  return descendants(tree).find((node) => node.type === AskUserCard);
}

function session(questions) {
  return { id: "session-1", pendingAsk: { id: "ask-1", questions } };
}

test("picking changes the pending selection and only Submit resolves a single question", async () => {
  reset();
  const ask = session([{ question: "Continue?", options: ["Yes", "No"] }]);
  let tree = render(ask);
  expect(card(tree).props.currentAnswer).toBe("");

  card(tree).props.onPick({ label: "Yes" });
  tree = render(ask);
  expect(card(tree).props.currentAnswer).toBe("Yes");
  expect(resolved).toHaveLength(0);

  card(tree).props.onPick({ label: "No" });
  tree = render(ask);
  expect(card(tree).props.currentAnswer).toBe("No");
  expect(resolved).toHaveLength(0);

  descendants(tree).find((node) => node.props?.class === "ask-user-prompt-submit").props.onClick();
  await Promise.resolve();
  expect(resolved).toEqual([["session-1", "ask-1", ["No"]]]);
});

test("free text replaces an option selection", () => {
  reset();
  const ask = session([{ question: "Continue?", options: ["Yes", "No"] }]);
  let tree = render(ask);
  card(tree).props.onPick({ label: "Yes" });
  tree = render(ask);
  card(tree).props.onFreeChange("Actually, wait");
  tree = render(ask);

  expect(card(tree).props.currentAnswer).toBe("Actually, wait");
  expect(card(tree).props.freeValue).toBe("Actually, wait");
});

test("an option still auto-advances and is restored after Back", () => {
  reset();
  const ask = session([
    { question: "First?", options: ["Yes", "No"] },
    { question: "Second?", options: ["One", "Two"] },
  ]);
  let tree = render(ask);
  card(tree).props.onPick({ label: "Yes" });
  tree = render(ask);
  expect(card(tree).props.question).toBe("Second?");

  descendants(tree).find((node) => node.props?.children === "← Back").props.onClick();
  tree = render(ask);
  expect(card(tree).props.question).toBe("First?");
  expect(card(tree).props.currentAnswer).toBe("Yes");
});
