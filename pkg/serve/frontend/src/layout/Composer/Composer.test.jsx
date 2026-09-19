import { expect, mock, test } from "bun:test";

const refs = [];
const sent = [];
const steered = [];
// Controls what the steer endpoint answers, so a test can exercise a refusal
// ({queued:false}) as well as an acceptance.
let steerResult = {};
// Controls what the command endpoint answers (and records the calls), so a test
// can exercise the deferred-outcome contract ({queued:true} without an id) and
// a transport failure separately.
let commandResult = { ok: true };
let commandError = null;
const commands = [];
// Lets a test hold a send in flight, reproducing the window in which the box
// can legitimately acquire other text before the server answers.
let sendResult;

let voiceOptions;

// Spread the real hooks first. bun's mock.module replaces the module for the
// whole process and never restores it, so a factory listing only some hooks
// deletes the rest for every file loaded afterwards -- the
// "Export named 'useMemo' not found" that only appears when these files run
// together.
const realHooks = await import("preact/hooks");
mock.module("preact/hooks", () => ({
  ...realHooks,
  useRef(initial) {
    const ref = { current: initial };
    refs.push(ref);
    return ref;
  },
  useCallback(fn) { return fn; },
  useEffect() {},
  useState(initial) { return [typeof initial === "function" ? initial() : initial, () => {}]; },
}));

// The slash menu's skills come from the server; this suite exercises the
// composer's own behaviour, so keep it off the network.
mock.module("../../hooks/useSessionSkills.js", () => ({
  useSessionSkills: () => ({ skills: [], refreshSkills() {} }),
}));

mock.module("../../hooks/useVoiceGesture.js", () => ({
  useVoiceGesture: (options) => {
    voiceOptions = options;
    return ({
    handlers: {}, recording: false, transcribing: false,
    supported: false, toggleFromShortcut() {}, cancel() {},
    });
  },
}));

// Spread the real module first. bun's mock.module REPLACES the whole module
// for the entire process and is never restored, so a partial factory deletes
// every export it does not list -- which is how AskUserPrompt's tests started
// dying on "Export named 'resolveAskUser' not found" whenever this file
// happened to load first. Overriding only what we simulate keeps the rest.
const realSessionActions = await import("../../data/session-actions.js");
mock.module("../../data/session-actions.js", () => ({
  ...realSessionActions,
  sendMessage: async (...args) => { sent.push(args); await sendResult; },
  newSteerId: () => "test-id",
  cancelRun: async () => ({}),
  cancelSteers: async () => {},
  execCommand: async (...args) => {
    commands.push(args);
    if (commandError) throw commandError;
    return commandResult;
  },
  execShell: async () => {},
  steerSubagent: async (...args) => { steered.push(args); return steerResult; },
}));

const { Composer, keepCommandSuggestionVisible } = await import("./Composer.jsx");
// The real store: the recall reads pendingSteers from it, so seeding it is
// closer to the running app than faking the module.
const { updateSession, setState, store } = await import("../../data/store.js");
const { getToasts } = await import("../../data/notifications.js");

test("keyboard command navigation keeps the selected row inside the menu", () => {
  const list = {
    scrollTop: 0,
    clientHeight: 132,
    children: Array.from({ length: 8 }, (_, i) => ({ offsetTop: i * 44, offsetHeight: 44 })),
  };

  keepCommandSuggestionVisible(list, 5);
  expect(list.scrollTop).toBe(132);
  keepCommandSuggestionVisible(list, 2);
  expect(list.scrollTop).toBe(88);
});

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

test("a transcript returns focus when the textarea had it when recording began", () => {
  refs.length = 0;
  Composer({ sessionId: "s1", session: { state: "idle" } });
  let focused = 0;
  const textarea = {
    value: "draft", selectionStart: 5, selectionEnd: 5,
    focus() { focused += 1; }, dispatchEvent() {},
  };
  refs[0].current = textarea;

  voiceOptions.onRecordingStart(textarea);
  voiceOptions.onTranscript("more");

  expect(textarea.value).toBe("draft more");
  expect(focused).toBe(1);
});

test("a transcript leaves focus alone when recording began away from the textarea", () => {
  refs.length = 0;
  Composer({ sessionId: "s1", session: { state: "idle" } });
  let focused = 0;
  const textarea = {
    value: "draft", selectionStart: 5, selectionEnd: 5,
    focus() { focused += 1; }, dispatchEvent() {},
  };
  refs[0].current = textarea;

  voiceOptions.onRecordingStart({});
  voiceOptions.onTranscript("more");

  expect(textarea.value).toBe("draft more");
  expect(textarea.selectionStart).toBe("draft more".length);
  expect(textarea.selectionEnd).toBe("draft more".length);
  expect(focused).toBe(0);
});

// Dictation used to be switched off in steer mode, on the grounds that a steer
// "targets a subagent, not the parent run" — which describes the rule instead
// of justifying it. The thing that could have justified it is the only thing
// worth defending: a transcript must land in the composer that asked for it.
// Each Composer holds its own voice gesture and writes its OWN textarea, so
// dictating into a subagent's steer box cannot spill into the parent's.
test("a steer composer's transcript lands in its own box, never in the parent's", () => {
  refs.length = 0;
  Composer({ sessionId: "s1", session: { state: "running" } });
  const parentVoice = voiceOptions;
  const parentTextarea = {
    value: "parent draft", selectionStart: 12, selectionEnd: 12,
    focus() {}, dispatchEvent() {},
  };
  refs[0].current = parentTextarea;
  const afterParent = refs.length;

  Composer({
    sessionId: "s1",
    session: { state: "running" },
    steer: { jobId: "sa-1", name: "child" },
  });
  const steerVoice = voiceOptions;
  const steerTextarea = {
    value: "", selectionStart: 0, selectionEnd: 0,
    focus() {}, dispatchEvent() {},
  };
  refs[afterParent].current = steerTextarea;

  // Two mounts, two gestures: the steer box did not inherit the parent's.
  expect(steerVoice).not.toBe(parentVoice);

  steerVoice.onTranscript("stop and report");
  expect(steerTextarea.value).toBe("stop and report");
  expect(parentTextarea.value).toBe("parent draft");
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

// Reproduces the message-destroying race caught in production (session
// 83aa2746): an ordinary send waits for the server before emptying the box, and
// in that window a queue recall cancelled the message server-side and restored
// its text to the textarea. The late clear then wiped that restored text, so
// the message was gone from both the server and the screen.
test("a send that resolves late does not wipe text written meanwhile", async () => {
  refs.length = 0;
  sent.length = 0;
  let releaseSend;
  sendResult = new Promise((resolve) => { releaseSend = resolve; });
  const tree = Composer({ sessionId: "race1", session: { state: "running" } });
  const textarea = descendants(tree).find((node) => node.type === "textarea");
  refs[0].current = {
    value: "queued message", style: {}, scrollHeight: 24,
    focus() {}, dispatchEvent() {},
  };
  textarea.props.onKeyDown({
    key: "Enter", shiftKey: false, altKey: false, metaKey: false,
    isComposing: false, preventDefault() {},
  });
  await Promise.resolve();

  // A voice transcript lands while the send is still in flight — the same
  // ownership question as a queue recall, through a route the first fix missed.
  // It goes through the composer's real write path, so the epoch it bumps is
  // the one the send captured.
  voiceOptions.onTranscript("second thought");
  expect(refs[0].current.value).toContain("second thought");

  releaseSend();
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
  expect(refs[0].current.value).toContain("second thought");
});

test("a send still clears the composer when its text is untouched", async () => {
  refs.length = 0;
  sent.length = 0;
  sendResult = Promise.resolve();
  const tree = Composer({ sessionId: "s1", session: { state: "idle" } });
  const textarea = descendants(tree).find((node) => node.type === "textarea");
  refs[0].current = { value: "ship it", style: {}, scrollHeight: 24 };
  textarea.props.onKeyDown({
    key: "Enter", shiftKey: false, altKey: false, metaKey: false,
    isComposing: false, preventDefault() {},
  });
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
  expect(refs[0].current.value).toBe("");
});

// A touch tap on the chip fires pointerout/pointerleave BEFORE the click. The
// first version of the guard disarmed on pointerleave, so on a phone the recall
// was killed by the very gesture activating it and the queue could not be
// reached at all. This drives the chip's real handlers in the browser's touch
// order, which the pure recallActivates tests cannot catch: they were green
// while the chip was dead.
test("tapping the queue chip recalls it despite the touch pointerleave", async () => {
  refs.length = 0;
  const sessionId = "touch-recall";
  // The recall reads the queue from the store, so the session must exist there;
  // updateSession ignores ids it doesn't know.
  setState({
    sessions: {
      ...store.get().sessions,
      [sessionId]: { id: sessionId, pendingSteers: [{ id: "a", text: "queued thought" }] },
    },
  });
  const tree = Composer({
    sessionId,
    session: { state: "running", pendingSteers: [{ id: "a", text: "queued thought" }] },
  });
  const chip = descendants(tree).find((node) => node.props?.class === "queue-note");
  expect(chip).toBeDefined();
  refs[0].current = { value: "", style: {}, scrollHeight: 24, focus() {}, dispatchEvent() {} };

  // The exact sequence a real finger produces (verified in Chromium): the
  // leave arrives BEFORE the click. Called only if the chip listens for it, so
  // the test states the browser's order rather than the current wiring.
  chip.props.onPointerDown({ pointerId: 2 });
  chip.props.onPointerLeave?.({ pointerId: 2 });
  chip.props.onClick({ pointerId: 2, detail: 1 });
  await Promise.resolve();

  expect(refs[0].current.value).toContain("queued thought");
});

// `/compact` is answered immediately now ({ok:true, queued:true} with no id:
// the compaction was STARTED, its outcome arrives over the WS). The composer
// must come back to life as soon as that response lands — the bug was that the
// POST only answered when the whole compaction finished, leaving the textarea
// readOnly for tens of seconds — and a message typed next must go to the queue.
test("a started /compact frees the composer and the next message is queued", async () => {
  refs.length = 0;
  sent.length = 0;
  commands.length = 0;
  commandError = null;
  commandResult = { ok: true, queued: true };
  sendResult = Promise.resolve();
  const sessionId = "compact-async";
  setState({
    sessions: {
      ...store.get().sessions,
      [sessionId]: { id: sessionId, state: "running", compacting: true },
    },
  });
  const session = { state: "running", compacting: true };
  let tree = Composer({ sessionId, session });
  let textarea = descendants(tree).find((node) => node.type === "textarea");
  refs[0].current = { value: "/compact", style: {}, scrollHeight: 24 };
  textarea.props.onKeyDown({
    key: "Enter", shiftKey: false, altKey: false, metaKey: false,
    isComposing: false, preventDefault() {},
  });
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
  expect(commands).toHaveLength(1);

  // The input is usable while the session is running/compacting: readOnly is
  // driven by the in-flight send, which the fast response already released.
  refs.length = 0;
  tree = Composer({ sessionId, session });
  textarea = descendants(tree).find((node) => node.type === "textarea");
  expect(textarea.props.readOnly).toBeFalsy();

  // A message written right after goes out (the server queues it as a steer).
  refs[0].current = { value: "meanwhile", style: {}, scrollHeight: 24 };
  textarea.props.onKeyDown({
    key: "Enter", shiftKey: false, altKey: false, metaKey: false,
    isComposing: false, preventDefault() {},
  });
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
  expect(sent).toHaveLength(1);
  expect(sent[0][1]).toBe("meanwhile");
});

// Chip reconciliation against the two shapes of `queued`. A command ENQUEUED
// behind a run echoes its id and will be retired by command_dequeued, so the
// optimistic chip is confirmed. A command ACCEPTED AND STARTED answers queued
// without an id and no command_dequeued ever follows: keeping its chip would
// leave a phantom queue entry forever.
test("a queued command with an id keeps its chip; one without an id leaves none", async () => {
  const chipsFor = async (result) => {
    refs.length = 0;
    commands.length = 0;
    commandError = null;
    commandResult = result;
    const sessionId = `chip-${Math.random()}`;
    setState({
      sessions: { ...store.get().sessions, [sessionId]: { id: sessionId, state: "running" } },
    });
    const tree = Composer({ sessionId, session: { state: "running" } });
    const textarea = descendants(tree).find((node) => node.type === "textarea");
    refs[0].current = { value: "/compact", style: {}, scrollHeight: 24 };
    textarea.props.onKeyDown({
      key: "Enter", shiftKey: false, altKey: false, metaKey: false,
      isComposing: false, preventDefault() {},
    });
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
    return store.get().sessions[sessionId].pendingSteers;
  };

  // Enqueued behind a run: the chip survives, confirmed, until command_dequeued.
  const queuedWithId = await chipsFor({ ok: true, queued: true, id: "test-id" });
  expect(queuedWithId).toHaveLength(1);
  expect(queuedWithId[0].id).toBe("test-id");
  expect(queuedWithId[0].confirmed).toBe(true);

  // Started now: no chip may remain.
  expect(await chipsFor({ ok: true, queued: true })).toBeFalsy();

  // Ran immediately (unchanged behaviour): no chip either.
  expect(await chipsFor({ ok: true })).toBeFalsy();
});

// A backgrounded PWA aborts its in-flight fetches, but the server keeps
// compacting and finishes fine. Reporting that abort as "Command error" told
// the user a compaction had failed when it had not: only an ANSWERED request
// (which carries an HTTP status) proves a rejection.
test("a transport failure raises no command error, an answered rejection does", async () => {
  const toastsAfter = async (error) => {
    refs.length = 0;
    commands.length = 0;
    commandError = error;
    commandResult = { ok: true };
    const before = getToasts().length;
    const sessionId = `transport-${Math.random()}`;
    setState({
      sessions: { ...store.get().sessions, [sessionId]: { id: sessionId, state: "idle" } },
    });
    const tree = Composer({ sessionId, session: { state: "idle" } });
    const textarea = descendants(tree).find((node) => node.type === "textarea");
    refs[0].current = { value: "/compact", style: {}, scrollHeight: 24 };
    textarea.props.onKeyDown({
      key: "Enter", shiftKey: false, altKey: false, metaKey: false,
      isComposing: false, preventDefault() {},
    });
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
    return getToasts().slice(before);
  };

  // No status: the request never got an answer — say nothing, the WS carries
  // the truth.
  expect(await toastsAfter(new TypeError("Failed to fetch"))).toHaveLength(0);

  // Answered with a rejection: surfaced exactly as before.
  const rejected = Object.assign(new Error("409: session is busy"), { status: 409 });
  const shown = await toastsAfter(rejected);
  expect(shown).toHaveLength(1);
  expect(shown[0].title).toBe("Command error");
  commandError = null;
});

// A command with no deferred outcome has no event coming to reveal its fate:
// swallowing its transport failure loses the error entirely.
test("a dead request still reports commands whose outcome is not deferred", async () => {
  refs.length = 0;
  commands.length = 0;
  commandError = new TypeError("Failed to fetch");
  commandResult = { ok: true };
  const before = getToasts().length;
  const sessionId = `rename-${Math.random()}`;
  setState({ sessions: { ...store.get().sessions, [sessionId]: { id: sessionId, state: "idle" } } });
  const tree = Composer({ sessionId, session: { state: "idle" } });
  const textarea = descendants(tree).find((node) => node.type === "textarea");
  refs[0].current = { value: "/rename Nuevo nombre", style: {}, scrollHeight: 24 };
  textarea.props.onKeyDown({
    key: "Enter", shiftKey: false, altKey: false, metaKey: false,
    isComposing: false, preventDefault() {},
  });
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
  const shown = getToasts().slice(before);
  commandError = null;
  expect(shown).toHaveLength(1);
});

test("the desktop + opens the file picker directly, with no menu", () => {
  refs.length = 0;
  const tree = Composer({ sessionId: "s1", session: { state: "idle" } });
  const nodes = descendants(tree);
  expect(nodes.some((node) => node.type?.name === "ActionMenu")).toBe(false);
  const plus = nodes.find((node) => node.props?.class === "zl-attach");
  expect(plus.props["aria-label"]).toBe("Attach");

  // Composer's second ref is the hidden file input; clicking + must reach it.
  let clicked = 0;
  refs[1].current = { click() { clicked += 1; } };
  plus.props.onClick();
  expect(clicked).toBe(1);
});

test("the slab is the catalogue's: zl-composer, zl-ta, zl-attach, zl-send — and send is never peach", () => {
  refs.length = 0;
  const tree = Composer({ sessionId: "s1", session: { state: "idle" } });
  const nodes = descendants(tree);
  const root = nodes.find((node) => String(node.props?.class || "").split(/\s+/).includes("zl-composer"));
  expect(root).toBeDefined();
  expect(nodes.some((node) => node.type === "textarea" && node.props?.class === "zl-ta")).toBe(true);
  expect(nodes.some((node) => node.props?.class === "zl-attach")).toBe(true);
  const send = nodes.find((node) => String(node.props?.class || "").split(/\s+/).includes("zl-send"));
  expect(send).toBeDefined();
  expect(send.props.class).not.toMatch(/peach/);
});

// The bug this shape exists to fix: the pill had room for one control at its
// end, so on the phone that button was the mic OR Send, and a draft made
// dictation unreachable. Both now sit on a control row of their own, in every
// density — so `+`, mic and Send are siblings there and Send is never the mic.
test("the controls live on their own row, and Send is always Send", () => {
  refs.length = 0;
  const tree = Composer({ sessionId: "s1", session: { state: "idle" } });
  const row = descendants(tree).find((node) => node.props?.class === "zl-controls");
  expect(row).toBeDefined();
  const inRow = descendants(row);
  const plus = inRow.find((node) => node.props?.class === "zl-attach");
  const send = inRow.find((node) => String(node.props?.class || "").split(/\s+/).includes("zl-send"));
  expect(plus).toBeDefined();
  expect(send).toBeDefined();
  // No takeover left: the send button carries none of the faces the phone's
  // mic-or-send button used to borrow, and its label never says "Record".
  expect(send.props.class).toBe("zl-send");
  expect(send.props["aria-label"]).toBe("Send");
});

// The mic is conditioned on voice being usable and on nothing else. A density
// check here would be the old takeover coming back through the side door.
test("the mic button is offered whenever voice works, in both densities", async () => {
  const source = await Bun.file(new URL("./Composer.jsx", import.meta.url)).text();
  expect(source).toMatch(/\{canVoice && \(/);
  expect(source).not.toMatch(/voiceButtonMode|usesVoiceSendButton|micMode|voiceOwnsButton/);
});

test("plusActions turn + into a menu whose first entry is still Attach files", () => {
  refs.length = 0;
  let previewed = 0;
  const tree = Composer({
    sessionId: "s1",
    session: { state: "idle" },
    compact: true,
    plusActions: [{ id: "preview", icon: () => null, label: "Live preview", onClick: () => { previewed += 1; } }],
  });
  const menu = descendants(tree).find((node) => node.type?.name === "ActionMenu");
  expect(menu).toBeTruthy();
  expect(menu.props.placement).toBe("up");
  expect(menu.props.actions.map((a) => a.label)).toEqual(["Attach files", "Live preview"]);

  let clicked = 0;
  refs[1].current = { click() { clicked += 1; } };
  menu.props.actions[0].onClick();
  expect(clicked).toBe(1);
  menu.props.actions[1].onClick();
  expect(previewed).toBe(1);
});
