import { expect, mock, test } from "bun:test";

const refs = [];
const resolved = [];

mock.module("preact/hooks", () => ({
  useState(initial) { return [typeof initial === "function" ? initial() : initial, () => {}]; },
  useEffect() {},
  useRef(initial) {
    const ref = { current: initial };
    refs.push(ref);
    return ref;
  },
  useCallback(fn) { return fn; },
}));

mock.module("../../data/session-actions.js", () => ({
  resolveAskUser: async (...args) => { resolved.push(args); },
}));

mock.module("../../hooks/useVoiceGesture.js", () => ({
  useVoiceGesture: () => ({
    handlers: {}, recording: false, transcribing: false, locked: false,
    showSlideHint: false, supported: true, toggleFromShortcut() {},
  }),
}));

mock.module("../../hooks/useCanTranscribe.js", () => ({
  useCanTranscribe: () => true,
}));

const { AskUserPrompt } = await import("./AskUserPrompt.jsx");

function descendants(node, result = []) {
  if (!node || typeof node !== "object") return result;
  if (Array.isArray(node)) {
    for (const child of node) descendants(child, result);
    return result;
  }
  result.push(node);
  descendants(node.props?.children, result);
  return result;
}

function renderPrompt() {
  refs.length = 0;
  const session = {
    id: "s1",
    pendingAsk: {
      id: "ask-1",
      questions: [
        { question: "Green light?", options: ["Yes", "No"] },
        { question: "How long?", options: ["Two minutes", "Five minutes"] },
      ],
    },
  };
  const tree = AskUserPrompt({ session });
  const skip = descendants(tree).find((node) => node.props?.class === "ask-user-prompt-skip");
  expect(skip).toBeDefined();
  return skip;
}

// Reproduces the incident the owner hit twice (session 40314282c88750bacfa21df9,
// ask at 07:45:37 resolved 25s later as four '(skipped)' answers while he was
// dictating the reply): the card discarded a question the agent needed without
// him ever pressing Skip.
//
// Skip is destructive — it throws away a question the run is blocked on — and it
// sits right below a card that moves under the finger: the voice error box above
// it appears and disappears with each attempt, so the actions row travels while
// a gesture is in progress and the click completing that gesture is delivered to
// whatever now occupies the point. Same shape as the "N queued" chip born under
// the finger (see recallActivates in src/data/composer-queue.js).
test("a click Skip did not receive its own pointerdown does not skip", async () => {
  resolved.length = 0;
  const skip = renderPrompt();

  // The gesture began somewhere else (the mic button, the composer): only the
  // click lands here.
  skip.props.onClick({ detail: 1, pointerId: 4 });
  await Promise.resolve();
  await Promise.resolve();

  expect(resolved).toHaveLength(0);
});

test("a deliberate tap on Skip still skips every blank answer", async () => {
  resolved.length = 0;
  const skip = renderPrompt();

  // The order a real finger produces, verified for the queue chip in Chromium:
  // pointerdown, then pointerleave BEFORE the click, then the click.
  skip.props.onPointerDown({ pointerId: 9 });
  skip.props.onPointerLeave?.({ pointerId: 9 });
  skip.props.onClick({ detail: 1, pointerId: 9 });
  await Promise.resolve();
  await Promise.resolve();

  expect(resolved).toHaveLength(1);
  expect(resolved[0][2]).toEqual(["(skipped)", "(skipped)"]);
});

// Keyboard and assistive technology dispatch a bare click with detail 0 and no
// pointer at all. Gating those would make Skip unreachable without a mouse.
test("an assistive-technology activation of Skip is honoured", async () => {
  resolved.length = 0;
  const skip = renderPrompt();

  skip.props.onClick({ detail: 0 });
  await Promise.resolve();
  await Promise.resolve();

  expect(resolved).toHaveLength(1);
});
