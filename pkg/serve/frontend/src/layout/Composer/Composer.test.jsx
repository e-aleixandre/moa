import { expect, mock, test } from "bun:test";

const refs = [];
const sent = [];
const steered = [];
// Controls what the steer endpoint answers, so a test can exercise a refusal
// ({queued:false}) as well as an acceptance.
let steerResult = {};
let voiceOptions;

mock.module("preact/hooks", () => ({
  useRef(initial) {
    const ref = { current: initial };
    refs.push(ref);
    return ref;
  },
  useCallback(fn) { return fn; },
  useEffect() {},
  useState(initial) { return [typeof initial === "function" ? initial() : initial, () => {}]; },
}));

mock.module("../../hooks/useVoiceGesture.js", () => ({
  useVoiceGesture: (options) => {
    voiceOptions = options;
    return ({
    handlers: {}, recording: false, transcribing: false, locked: false,
    showSlideHint: false, supported: false, toggleFromShortcut() {},
    });
  },
}));

mock.module("../../data/session-actions.js", () => ({
  sendMessage: async (...args) => { sent.push(args); },
  newSteerId: () => "test-id",
  cancelRun: async () => ({}),
  cancelSteers: async () => {},
  execCommand: async () => ({ ok: true }),
  execShell: async () => {},
  steerSubagent: async (...args) => { steered.push(args); return steerResult; },
}));

const { Composer } = await import("./Composer.jsx");

function descendants(node, result = []) {
  if (!node || typeof node === "string") return result;
  result.push(node);
  const children = node.props?.children;
  for (const child of Array.isArray(children) ? children : [children]) descendants(child, result);
  return result;
}

test("ordinary textarea Enter sends through the Composer component", async () => {
  refs.length = 0;
  sent.length = 0;
  const tree = Composer({ sessionId: "s1", session: { state: "idle" } });
  const textarea = descendants(tree).find((node) => node.type === "textarea");
  expect(textarea).toBeDefined();
  // Composer's first ref is its textarea ref. Supplying the DOM-shaped value
  // executes the real onKeyDown -> send barrier -> handleSend path, rather than
  // only testing its extracted activation reducer.
  refs[0].current = { value: "ship this", style: {}, scrollHeight: 24 };
  let prevented = false;
  textarea.props.onKeyDown({
    key: "Enter", shiftKey: false, altKey: false, metaKey: false,
    isComposing: false, preventDefault() { prevented = true; },
  });
  await Promise.resolve();
  await Promise.resolve();
  expect(prevented).toBe(true);
  expect(sent).toHaveLength(1);
  expect(sent[0]).toEqual(["s1", "ship this", []]);
});

test("successive voice transcripts append at the caret without replacing the draft", () => {
  refs.length = 0;
  Composer({ sessionId: "s1", session: { state: "idle" } });
  const textarea = {
    value: "already",
    selectionStart: 7,
    selectionEnd: 7,
    focus() {},
    dispatchEvent() {},
  };
  refs[0].current = textarea;

  voiceOptions.onTranscript("first");
  voiceOptions.onTranscript("second");

  expect(textarea.value).toBe("already first second");
  expect(textarea.selectionStart).toBe("already first second".length);
  expect(textarea.selectionEnd).toBe("already first second".length);
});

// A steer the server refused must not look like it was sent: the composer keeps
// the text so the user can resend it, instead of silently emptying the box on
// any HTTP 200. This is the case that made a lost message indistinguishable
// from a delivered one.
test("a refused subagent steer keeps the user's text in the composer", async () => {
  refs.length = 0;
  steered.length = 0;
  steerResult = { queued: false };
  const tree = Composer({
    sessionId: "s1",
    session: { state: "running" },
    steer: { jobId: "sa-1", name: "child" },
  });
  const textarea = descendants(tree).find((node) => node.type === "textarea");
  refs[0].current = { value: "please stop", style: {}, scrollHeight: 24 };
  textarea.props.onKeyDown({
    key: "Enter", shiftKey: false, altKey: false, metaKey: false,
    isComposing: false, preventDefault() {},
  });
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
  expect(steered).toHaveLength(1);
  expect(refs[0].current.value).toBe("please stop");
});

test("an accepted subagent steer clears the composer", async () => {
  refs.length = 0;
  steered.length = 0;
  steerResult = { queued: true };
  const tree = Composer({
    sessionId: "s1",
    session: { state: "running" },
    steer: { jobId: "sa-1", name: "child" },
  });
  const textarea = descendants(tree).find((node) => node.type === "textarea");
  refs[0].current = { value: "look at the image", style: {}, scrollHeight: 24 };
  textarea.props.onKeyDown({
    key: "Enter", shiftKey: false, altKey: false, metaKey: false,
    isComposing: false, preventDefault() {},
  });
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
  expect(steered).toHaveLength(1);
  expect(refs[0].current.value).toBe("");
});
