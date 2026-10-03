import { expect, mock, test } from "bun:test";

const state = [];
let stateIndex = 0;
const resolved = [];

// Spread the real hooks first. bun's mock.module replaces the module for the
// whole process and never restores it, so a factory listing only some hooks
// deletes the rest for every file loaded afterwards -- the
// "Export named 'useMemo' not found" that only appears when these files run
// together.
const realHooks = await import("preact/hooks");
mock.module("preact/hooks", () => ({
  ...realHooks,
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
  useVoiceGesture: () => ({ handlers: {}, recording: false, transcribing: false, supported: false, cancel() {} }),
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

  descendants(tree).find((node) => node.props?.["aria-label"] === "Previous question").props.onClick();
  tree = render(ask);
  expect(card(tree).props.question).toBe("First?");
  expect(card(tree).props.currentAnswer).toBe("Yes");
});

const submitButton = (tree) => descendants(tree).find((node) => node.props?.class === "ask-user-prompt-submit");

test("multiple: ticking toggles without advancing, and Submit sends picks plus free text as one answer", async () => {
  reset();
  const ask = session([
    { question: "Which?", options: ["A", "B", "C", "D"], multiple: true },
    { question: "Sure?", options: ["Yes", "No"] },
  ]);
  let tree = render(ask);
  expect(card(tree).props.multiple).toBe(true);
  expect(card(tree).props.selected).toEqual([]);

  card(tree).props.onPick({ label: "C" });
  tree = render(ask);
  card(tree).props.onPick({ label: "A" });
  tree = render(ask);
  expect(card(tree).props.question).toBe("Which?"); // no auto-advance
  expect(card(tree).props.selected).toEqual(["C", "A"]);

  card(tree).props.onPick({ label: "C" }); // untick
  tree = render(ask);
  card(tree).props.onPick({ label: "C" });
  tree = render(ask);
  card(tree).props.onFreeChange("and more");
  tree = render(ask);
  expect(card(tree).props.freeValue).toBe("and more");

  submitButton(tree).props.onClick(); // Continue → second question
  tree = render(ask);
  expect(card(tree).props.question).toBe("Sure?");
  expect(card(tree).props.multiple).toBeFalsy();
  card(tree).props.onPick({ label: "Yes" });
  tree = render(ask);
  submitButton(tree).props.onClick();
  await Promise.resolve();
  expect(resolved).toEqual([["session-1", "ask-1", ["A; C; and more", "Yes"]]]);
});

test("multiple: a question with nothing ticked and no text is still unanswered", () => {
  reset();
  const ask = session([{ question: "Which?", options: ["A", "B"], multiple: true }]);
  let tree = render(ask);
  submitButton(tree).props.onClick();
  expect(resolved).toHaveLength(0);
  card(tree).props.onPick({ label: "B" });
  tree = render(ask);
  submitButton(tree).props.onClick();
  expect(resolved).toHaveLength(1);
});

test("multiple: free text alone is a valid answer", async () => {
  reset();
  const ask = session([{ question: "Which?", options: ["A", "B"], multiple: true }]);
  let tree = render(ask);
  card(tree).props.onFreeChange("neither");
  tree = render(ask);
  submitButton(tree).props.onClick();
  await Promise.resolve();
  expect(resolved).toEqual([["session-1", "ask-1", ["neither"]]]);
});
