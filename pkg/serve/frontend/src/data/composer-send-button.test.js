import { test, expect } from "bun:test";
import {
  SEND_BUTTON_INITIAL, sendButtonEvent, usesVoiceSendButton,
  reduceContentSendActivation,
} from "./composer-send-button.js";

function drive(events, start = SEND_BUTTON_INITIAL) {
  let state = start;
  const actions = [];
  for (const event of events) {
    const result = reduceContentSendActivation(state, event);
    state = result.state;
    actions.push(...result.actions.map((action) => action.type));
  }
  return { state, actions };
}

const down = (id) => sendButtonEvent.pointerDown(id);
const up = (id) => sendButtonEvent.pointerUp(id);
const cancel = (id) => sendButtonEvent.pointerCancel(id);
const click = () => sendButtonEvent.click();

// The phone has room for exactly one 44px control at the end of the pill, so
// the mic takes it and hold-to-talk is the gesture. The desktop bar has room
// for two, so the arrow keeps the primary action and the mic sits beside it —
// what the catalogue draws and what every other desktop chat app does. Holding
// a mouse button down to dictate is a worse gesture than clicking, too.
test("voice takes over the send button only where there is room for one control", () => {
  expect(usesVoiceSendButton({ canVoice: true, compact: true })).toBe(true);
  expect(usesVoiceSendButton({ canVoice: true, compact: false })).toBe(false);
  expect(usesVoiceSendButton({ canVoice: false, compact: true })).toBe(false);
  expect(usesVoiceSendButton({ canVoice: false, compact: false })).toBe(false);
});

test("a pointercancel fallback is tied to its pointer and latches its terminal activation", () => {
  const { state, actions } = drive([down(7), cancel(7)]);
  expect(actions).toEqual(["send"]);
  expect(state).toEqual({
    activePointerId: null,
    terminalActivation: { kind: "pointer", pointerId: 7 },
    sendInFlight: true,
  });
  expect(drive([cancel(7)]).actions).toEqual([]); // no pointer identity, no fallback
});

test("every adversarial terminal ordering emits exactly one send", () => {
  const cases = [
    [down(1), cancel(1), cancel(1)], // repeated POINTER_CANCEL
    [down(1), cancel(1), up(1)], // pointercancel → pointerup
    [down(1), cancel(1), click()], // pointercancel → click
    [down(1), up(1), click()], // pointerup → iOS synthetic click
  ];
  for (const events of cases) {
    expect(drive(events).actions).toEqual(["send"]);
  }
});

test("double taps and rapid multi-pointer input cannot re-enter an in-flight send", () => {
  expect(drive([
    down(1), up(1), click(),
    down(2), up(2), click(),
  ]).actions).toEqual(["send"]);

  expect(drive([
    down(1), down(2), cancel(2), cancel(1), up(1), click(),
  ]).actions).toEqual(["send"]);
});

test("keyboard activation and pointer/click follow-ups cannot double-send", () => {
  expect(drive([
    sendButtonEvent.keyActivate(), click(), // Enter plus its native click
  ]).actions).toEqual(["send"]);

  expect(drive([
    sendButtonEvent.keyActivate(), down(1), cancel(1), click(),
  ]).actions).toEqual(["send"]);
});

test("textarea Enter, modified Enter, and the button share one composer-wide barrier", () => {
  // Composer routes both textarea Enter forms through KEY_ACTIVATE, exactly as
  // its button keyboard handler does. Interleaving either with a pointer
  // terminal event must still execute one content send.
  for (const events of [
    [sendButtonEvent.keyActivate(), down(1), up(1), click()],
    [down(1), up(1), click(), sendButtonEvent.keyActivate()],
    [sendButtonEvent.keyActivate(), sendButtonEvent.keyActivate()],
  ]) {
    expect(drive(events).actions).toEqual(["send"]);
  }
});

test("a tap while a send is in flight is inert, then a later distinct activation can send", () => {
  const first = drive([down(1), up(1), click()]);
  const whileInFlight = drive([down(2), cancel(2), click()], first.state);
  expect([...first.actions, ...whileInFlight.actions]).toEqual(["send"]);

  const finished = drive([sendButtonEvent.sendFinished()], whileInFlight.state);
  expect(finished.state.sendInFlight).toBe(false);
  expect(drive([down(3), up(3), click()], finished.state).actions).toEqual(["send"]);
});

test("an attachment-only message can only be submitted once by one physical activation", () => {
  // The reducer owns the guard before Composer's async handler closes over and
  // clears attachments. Model that handler with a counter: all duplicate DOM
  // terminal events for this one touch still invoke it once.
  let attachmentOnlySends = 0;
  const { actions } = drive([down(42), cancel(42), cancel(42), up(42), click()]);
  for (const action of actions) {
    if (action === "send") attachmentOnlySends += 1;
  }
  expect(attachmentOnlySends).toBe(1);
});
