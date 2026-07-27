// voice-gestures.test.js — run with `bun test`.
//
// Exhaustive coverage of the push-to-talk state machine (data/voice-gestures.js).
// Every transition is exercised without a DOM: the reducer is pure, so these
// tests ARE the spec for tap-vs-hold-vs-lock and the iOS pointercancel path.
import { test, expect } from "bun:test";
import {
  reduce, ev, INITIAL, HOLD_MS, LOCK_SLIDE_PX,
  isRecordingPhase, isLockedPhase, isTranscribingPhase, showSlideHint,
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

test("thresholds are the documented iOS-tuned values", () => {
  expect(HOLD_MS).toBe(180);
  expect(LOCK_SLIDE_PX).toBe(48);
});

test("quick tap (down then up before the hold timer) sends and returns to idle", () => {
  const { state, actions } = drive([ev.pointerDown(100), ev.pointerUp()]);
  expect(actions).toEqual(["send"]);
  expect(state.phase).toBe("idle");
});

test("keyboard activation from idle sends without recording", () => {
  const { state, actions } = drive([ev.keyActivate()]);
  expect(actions).toEqual(["send"]);
  expect(state.phase).toBe("idle");
});

test("hold past HOLD_MS starts recording; release stops and transcribes", () => {
  const { state, actions } = drive([
    ev.pointerDown(100),
    ev.holdTimer(),
    ev.pointerUp(),
  ]);
  expect(actions).toEqual(["start", "stop"]);
  expect(state.phase).toBe("transcribing");
  expect(isTranscribingPhase(state)).toBe(true);
});

test("cancel before the hold fires records nothing", () => {
  const { state, actions } = drive([ev.pointerDown(100), ev.pointerCancel()]);
  expect(actions).toEqual([]);
  expect(state.phase).toBe("idle");
});

test("slide up past the threshold while recording locks hands-free (visual lock, no stop)", () => {
  // startY 200, move to 200 - 48 = 152 → exactly the threshold locks.
  const { state, actions } = drive([
    ev.pointerDown(200),
    ev.holdTimer(),
    ev.pointerMove(200 - LOCK_SLIDE_PX),
  ]);
  expect(actions).toEqual(["start", "lock"]);
  expect(state.phase).toBe("recordingLocked");
  expect(isLockedPhase(state)).toBe(true);
});

test("a small slide (under the threshold) does not lock", () => {
  const { state, actions } = drive([
    ev.pointerDown(200),
    ev.holdTimer(),
    ev.pointerMove(200 - (LOCK_SLIDE_PX - 1)),
  ]);
  expect(actions).toEqual(["start"]);
  expect(state.phase).toBe("recording");
});

test("lock only fires once even with further movement", () => {
  const { actions } = drive([
    ev.pointerDown(200),
    ev.holdTimer(),
    ev.pointerMove(150),
    ev.pointerMove(120),
    ev.pointerMove(80),
  ]);
  expect(actions).toEqual(["start", "lock"]);
});

test("slide-lock then release rests in locked mode without stopping", () => {
  const { state, actions } = drive([
    ev.pointerDown(200),
    ev.holdTimer(),
    ev.pointerMove(140),
    ev.pointerUp(),
  ]);
  expect(actions).toEqual(["start", "lock"]);
  expect(state.phase).toBe("locked");
  expect(isRecordingPhase(state)).toBe(true);
  expect(isLockedPhase(state)).toBe(true);
});

test("locked: a tap (down then up) stops and transcribes", () => {
  const afterLock = drive([
    ev.pointerDown(200),
    ev.holdTimer(),
    ev.pointerMove(140),
    ev.pointerUp(),
  ]).state;
  expect(afterLock.phase).toBe("locked");

  const { state, actions } = drive([ev.pointerDown(160), ev.pointerUp()], afterLock);
  expect(actions).toEqual(["stop", "unlock"]);
  expect(state.phase).toBe("transcribing");
});

test("locked: keyboard activation stops and transcribes", () => {
  const afterLock = drive([
    ev.pointerDown(200), ev.holdTimer(), ev.pointerMove(140), ev.pointerUp(),
  ]).state;
  const { state, actions } = drive([ev.keyActivate()], afterLock);
  expect(actions).toEqual(["stop", "unlock"]);
  expect(state.phase).toBe("transcribing");
});

test("locked tap that drifts off (pointercancel instead of up) still stops", () => {
  const afterLock = drive([
    ev.pointerDown(200), ev.holdTimer(), ev.pointerMove(140), ev.pointerUp(),
  ]).state;
  const { state, actions } = drive([ev.pointerDown(160), ev.pointerCancel()], afterLock);
  expect(actions).toEqual(["stop", "unlock"]);
  expect(state.phase).toBe("transcribing");
});

test("iOS: pointercancel mid-recording promotes to locked instead of dropping audio", () => {
  // The critical iOS Safari path: the OS fires pointercancel while the finger
  // is still down (~1-2s in) to hijack the long-press. We must keep recording.
  const { state, actions } = drive([
    ev.pointerDown(200),
    ev.holdTimer(),
    ev.pointerCancel(),
  ]);
  expect(actions).toEqual(["start", "lock"]);
  expect(state.phase).toBe("locked");
  expect(isRecordingPhase(state)).toBe(true);
  // And from there a tap stops it normally.
  const stopped = drive([ev.pointerDown(160), ev.pointerUp()], state);
  expect(stopped.actions).toEqual(["stop", "unlock"]);
  expect(stopped.state.phase).toBe("transcribing");
});

test("iOS: pointercancel after a slide-lock keeps the recording (no double stop)", () => {
  const { state, actions } = drive([
    ev.pointerDown(200),
    ev.holdTimer(),
    ev.pointerMove(140), // slide-lock
    ev.pointerCancel(),  // iOS cancel after lock
  ]);
  expect(actions).toEqual(["start", "lock"]);
  expect(state.phase).toBe("locked");
});

test("transcribing is inert: taps and keys do nothing until it resolves", () => {
  const transcribing = drive([
    ev.pointerDown(100), ev.holdTimer(), ev.pointerUp(),
  ]).state;
  expect(transcribing.phase).toBe("transcribing");

  const { state, actions } = drive([
    ev.pointerDown(100), ev.pointerUp(), ev.keyActivate(),
  ], transcribing);
  expect(actions).toEqual([]);
  expect(state.phase).toBe("transcribing");

  // TRANSCRIBE_DONE returns to idle.
  const done = reduce(transcribing, ev.transcribeDone());
  expect(done.state.phase).toBe("idle");
});

test("shortcut toggle from idle starts a hands-free recording that rests in locked", () => {
  const { state, actions } = drive([ev.shortcutToggle()]);
  expect(actions).toEqual(["start", "lock"]);
  expect(state.phase).toBe("locked");
  expect(isRecordingPhase(state)).toBe(true);
  expect(isLockedPhase(state)).toBe(true);
});

test("shortcut toggle while recording stops and transcribes", () => {
  // Held recording (finger down) — shortcut still stops it.
  const held = drive([ev.pointerDown(100), ev.holdTimer()]).state;
  const r1 = drive([ev.shortcutToggle()], held);
  expect(r1.actions).toEqual(["stop", "unlock"]);
  expect(r1.state.phase).toBe("transcribing");

  // Locked recording — shortcut stops it too.
  const locked = drive([ev.shortcutToggle()]).state;
  const r2 = drive([ev.shortcutToggle()], locked);
  expect(r2.actions).toEqual(["stop", "unlock"]);
  expect(r2.state.phase).toBe("transcribing");
});

test("shortcut toggle is inert while transcribing", () => {
  const transcribing = drive([ev.pointerDown(100), ev.holdTimer(), ev.pointerUp()]).state;
  expect(transcribing.phase).toBe("transcribing");
  const { state, actions } = drive([ev.shortcutToggle()], transcribing);
  expect(actions).toEqual([]);
  expect(state.phase).toBe("transcribing");
});

test("RESET from any phase returns to idle with no actions", () => {
  const recording = drive([ev.pointerDown(100), ev.holdTimer()]).state;
  expect(recording.phase).toBe("recording");
  const r = reduce(recording, ev.reset());
  expect(r.state.phase).toBe("idle");
  expect(r.state.locked).toBe(false);
  expect(r.actions).toEqual([]);
});

test("unknown events are a no-op in every phase", () => {
  const phases = [
    INITIAL,
    drive([ev.pointerDown(100)]).state,
    drive([ev.pointerDown(100), ev.holdTimer()]).state,
    drive([ev.pointerDown(200), ev.holdTimer(), ev.pointerMove(140), ev.pointerUp()]).state,
  ];
  for (const st of phases) {
    const r = reduce(st, { type: "NONSENSE" });
    expect(r.state).toBe(st);
    expect(r.actions).toEqual([]);
  }
});

test("predicates classify phases correctly", () => {
  expect(isRecordingPhase(INITIAL)).toBe(false);
  expect(showSlideHint(INITIAL)).toBe(false);

  const recording = drive([ev.pointerDown(100), ev.holdTimer()]).state;
  expect(isRecordingPhase(recording)).toBe(true);
  expect(isLockedPhase(recording)).toBe(false);
  expect(showSlideHint(recording)).toBe(true); // hold-recording can still lock

  const slideLocked = drive([
    ev.pointerDown(200), ev.holdTimer(), ev.pointerMove(140),
  ]).state;
  expect(showSlideHint(slideLocked)).toBe(false); // already locking
  expect(isLockedPhase(slideLocked)).toBe(true);
});
