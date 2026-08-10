import { expect, mock, test } from "bun:test";

const refs = [];
const sent = [];
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
  steerSubagent: async () => {},
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
