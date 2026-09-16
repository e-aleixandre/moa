// voice-gestures.test.js — run with `bun test`.
//
// Exhaustive coverage of the tap-to-talk state machine (data/voice-gestures.js).
// Every transition is exercised without a DOM: the reducer is pure, so these
// tests ARE the spec for tap-to-start / tap-to-stop, Esc-to-discard, and the
// iOS pointercancel path that the toggle model made a non-event.
import { test, expect } from "bun:test";
import {
  reduce, ev, INITIAL, isRecordingPhase, isTranscribingPhase,
} from "./voice-gestures.js";

// drive — fold a sequence of events through the reducer, collecting the actions
// emitted at each step. Returns { state, actions } where actions is the flat,
// ordered list across the whole sequence.
function drive(events, start = INITIAL) {
  let state = start;
  const actions = [];
  for (const e of events) {
    const r = reduce(state, e);
    state = r.state;
    for (const a of r.actions) actions.push(a.type);
  }
  return { state, actions };
}

test("a tap from idle starts recording", () => {
  const { state, actions } = drive([ev.toggle()]);
  expect(actions).toEqual(["start"]);
  expect(state.phase).toBe("recording");
  expect(isRecordingPhase(state)).toBe(true);
});

test("a second tap stops and transcribes", () => {
  const { state, actions } = drive([ev.toggle(), ev.toggle()]);
  expect(actions).toEqual(["start", "stop"]);
  expect(state.phase).toBe("transcribing");
  expect(isTranscribingPhase(state)).toBe(true);
});

test("the full round trip returns to idle, ready to record again", () => {
  const { state, actions } = drive([ev.toggle(), ev.toggle(), ev.transcribeDone()]);
  expect(actions).toEqual(["start", "stop"]);
  expect(state.phase).toBe("idle");
  // And the machine is genuinely reusable, not merely idle-looking.
  expect(drive([ev.toggle()], state).actions).toEqual(["start"]);
});

test("Esc while recording discards the audio instead of transcribing it", () => {
  // The escape hatch the slide-to-cancel gesture used to provide: stop the mic
  // with nothing reaching the field.
  const { state, actions } = drive([ev.toggle(), ev.cancel()]);
  expect(actions).toEqual(["start", "discard"]);
  expect(state.phase).toBe("idle");
  expect(isRecordingPhase(state)).toBe(false);
});

test("Esc is inert when nothing is recording", () => {
  // No phantom discard from idle...
  expect(drive([ev.cancel()]).actions).toEqual([]);
  // ...and none from transcribing either: the audio is already gone.
  const transcribing = drive([ev.toggle(), ev.toggle()]).state;
  const { state, actions } = drive([ev.cancel()], transcribing);
  expect(actions).toEqual([]);
  expect(state.phase).toBe("transcribing");
});

test("iOS pointercancel is no longer a case at all", () => {
  // The whole reason the old machine existed: iOS Safari fires pointercancel
  // mid-hold to hijack the long press for Haptic Touch, which used to threaten
  // a live recording. A toggle has no long press, so the recorder is driven by
  // discrete clicks and an unknown/cancelled pointer event changes nothing.
  const recording = drive([ev.toggle()]).state;
  const r = reduce(recording, { type: "POINTER_CANCEL" });
  expect(r.state).toBe(recording);
  expect(r.actions).toEqual([]);
  expect(isRecordingPhase(r.state)).toBe(true);
});

test("transcribing is inert: taps do nothing until it resolves", () => {
  const transcribing = drive([ev.toggle(), ev.toggle()]).state;
  expect(transcribing.phase).toBe("transcribing");

  const { state, actions } = drive([ev.toggle(), ev.toggle()], transcribing);
  expect(actions).toEqual([]);
  expect(state.phase).toBe("transcribing");

  // TRANSCRIBE_DONE returns to idle.
  const done = reduce(transcribing, ev.transcribeDone());
  expect(done.state.phase).toBe("idle");
  expect(done.actions).toEqual([]);
});

test("TRANSCRIBE_DONE outside transcribing changes nothing", () => {
  const recording = drive([ev.toggle()]).state;
  const r = reduce(recording, ev.transcribeDone());
  expect(r.state).toBe(recording);
  expect(r.actions).toEqual([]);
});

test("RESET from any phase returns to idle with no actions", () => {
  const phases = [
    INITIAL,
    drive([ev.toggle()]).state,
    drive([ev.toggle(), ev.toggle()]).state,
  ];
  for (const st of phases) {
    const r = reduce(st, ev.reset());
    expect(r.state.phase).toBe("idle");
    expect(r.actions).toEqual([]);
  }
});

test("unknown events are a no-op in every phase", () => {
  const phases = [
    INITIAL,
    drive([ev.toggle()]).state,
    drive([ev.toggle(), ev.toggle()]).state,
  ];
  for (const st of phases) {
    const r = reduce(st, { type: "NONSENSE" });
    expect(r.state).toBe(st);
    expect(r.actions).toEqual([]);
  }
});

test("predicates classify phases correctly", () => {
  expect(isRecordingPhase(INITIAL)).toBe(false);
  expect(isTranscribingPhase(INITIAL)).toBe(false);

  const recording = drive([ev.toggle()]).state;
  expect(isRecordingPhase(recording)).toBe(true);
  expect(isTranscribingPhase(recording)).toBe(false);

  const transcribing = drive([ev.toggle(), ev.toggle()]).state;
  expect(isRecordingPhase(transcribing)).toBe(false);
  expect(isTranscribingPhase(transcribing)).toBe(true);
});

test("predicates tolerate a missing state", () => {
  expect(isRecordingPhase(undefined)).toBe(false);
  expect(isTranscribingPhase(null)).toBe(false);
});
