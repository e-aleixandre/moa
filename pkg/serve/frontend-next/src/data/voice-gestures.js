// voice-gestures.js — the pure push-to-talk gesture state machine.
//
// This is the single most fragile piece of the voice feature (5M): a tap must
// send, a press-and-hold must record, a slide-up must lock hands-free, and iOS
// Safari's habit of firing `pointercancel` mid-hold (it hijacks the long-press
// for Haptic Touch / the callout menu) must NOT drop the recording. The old SPA
// tangled all of that into ref-driven pointer handlers inside InputBar; here it
// lives as a pure reducer so every transition — including the iOS cancel path —
// is unit-tested without a DOM.
//
// The reducer owns only the DECISIONS. The thin hook around it (useVoice /
// Composer) owns the EFFECTS: pointer capture, window fallback listeners,
// preventDefault, timers, and the actual MediaRecorder via useVoice. On every
// event the reducer returns the next state plus an ordered list of `actions`
// the wrapper must perform:
//   - "start"        begin recording (useVoice.start)
//   - "stop"         stop + transcribe (useVoice.stop)
//   - "send"         submit the composer (a plain tap)
//   - "lock"         reflect hands-free lock in the UI (visual only)
//   - "unlock"       clear the hands-free lock in the UI
// Recordings shorter than ~400ms are dropped by useVoice itself, so an
// accidental tap that briefly crosses HOLD_MS still can't fire a transcription.
//
// Phases:
//   idle           nothing pressed, not recording.
//   holding        pressed, hold timer running — not yet a recording. Releasing
//                  now is a quick tap (send); crossing HOLD_MS becomes recording.
//   recording      held past HOLD_MS, finger still down, not locked.
//   recordingLocked slid up past LOCK_SLIDE_PX while holding — will stay recording
//                  hands-free once the finger lifts.
//   locked         hands-free recording, no finger down. A tap stops it.
//   lockedPressed  a tap on a locked recording is in progress (down, awaiting up).
//   transcribing   recording stopped, audio is being transcribed. Inert to taps
//                  so a trailing click can't submit stale text.

export const HOLD_MS = 180; // press longer than this = intentional hold (record)
export const LOCK_SLIDE_PX = 48; // slide up at least this far while holding = lock

export const INITIAL = { phase: "idle", startY: 0, locked: false };

// Event creators (documentation of the event shape the reducer accepts).
export const ev = {
  pointerDown: (y = 0) => ({ type: "POINTER_DOWN", y }),
  holdTimer: () => ({ type: "HOLD_TIMER" }),
  pointerMove: (y = 0) => ({ type: "POINTER_MOVE", y }),
  pointerUp: () => ({ type: "POINTER_UP" }),
  pointerCancel: () => ({ type: "POINTER_CANCEL" }),
  keyActivate: () => ({ type: "KEY_ACTIVATE" }),
  // Keyboard shortcut (⌘. / Alt+.) — a pointer-free toggle: from idle it starts
  // a hands-free (locked) recording; while recording it stops + transcribes.
  shortcutToggle: () => ({ type: "SHORTCUT_TOGGLE" }),
  transcribeDone: () => ({ type: "TRANSCRIBE_DONE" }),
  reset: () => ({ type: "RESET" }),
};

function out(phase, patch, actions) {
  return { state: { phase, startY: 0, locked: false, ...patch }, actions: actions || [] };
}

// reduce(state, event) → { state, actions } — pure. Never touches the DOM,
// timers, or MediaRecorder. Unknown events are a no-op (state unchanged, no
// actions) so the wrapper can forward events liberally.
export function reduce(state, event) {
  const s = state || INITIAL;

  // A RESET always returns to idle and clears any lock (unmount, transcription
  // finished, hard error). Emitted with no side actions — the wrapper decides
  // whether to also clear its own visual lock via the returned state.
  if (event.type === "RESET") return out("idle", {}, []);

  // SHORTCUT_TOGGLE (⌘. / Alt+.) is pointer-free, so it can't ride the normal
  // down→hold→up path. From idle it starts a hands-free recording that rests
  // directly in `locked` (no finger is holding). While the mic is already live
  // in ANY recording phase it stops + transcribes. It's inert while a
  // transcription is in flight (same guard as a trailing tap).
  if (event.type === "SHORTCUT_TOGGLE") {
    if (s.phase === "idle") {
      return out("locked", { locked: true }, [{ type: "start" }, { type: "lock" }]);
    }
    if (isRecordingPhase(s)) {
      return out("transcribing", {}, [{ type: "stop" }, { type: "unlock" }]);
    }
    return { state: s, actions: [] };
  }

  switch (s.phase) {
    case "idle":
      switch (event.type) {
        case "POINTER_DOWN":
          // Start a potential hold; the wrapper arms the HOLD_MS timer.
          return out("holding", { startY: event.y });
        case "KEY_ACTIVATE":
          // Keyboard activation (Enter/Space) with no pointer sequence = send.
          return { state: s, actions: [{ type: "send" }] };
        default:
          return { state: s, actions: [] };
      }

    case "holding":
      switch (event.type) {
        case "HOLD_TIMER":
          // Crossed the intentional-hold threshold → begin recording.
          return out("recording", { startY: s.startY }, [{ type: "start" }]);
        case "POINTER_UP":
          // Released before HOLD_MS → quick tap → send.
          return out("idle", {}, [{ type: "send" }]);
        case "POINTER_CANCEL":
          // Cancelled before anything was recorded → nothing to do.
          return out("idle", {}, []);
        case "POINTER_MOVE":
          // Not recording yet — movement is irrelevant until the hold fires.
          return { state: s, actions: [] };
        default:
          return { state: s, actions: [] };
      }

    case "recording":
      switch (event.type) {
        case "POINTER_MOVE":
          // Slide up far enough → lock hands-free (still holding). Visual only;
          // the recorder keeps running. Only fire once.
          if (s.startY - event.y >= LOCK_SLIDE_PX) {
            return out("recordingLocked", { startY: s.startY, locked: true }, [{ type: "lock" }]);
          }
          return { state: s, actions: [] };
        case "POINTER_UP":
          // Plain hold-and-release → stop + transcribe.
          return out("transcribing", {}, [{ type: "stop" }]);
        case "POINTER_CANCEL":
          // iOS hijacked the long-press while the finger is STILL down. Do NOT
          // drop the recording — promote it to locked (hands-free) and keep the
          // audio flowing; a later tap stops it.
          return out("locked", { locked: true }, [{ type: "lock" }]);
        default:
          return { state: s, actions: [] };
      }

    case "recordingLocked":
      switch (event.type) {
        case "POINTER_UP":
          // Finger lifts after a slide-lock → rest in hands-free locked mode,
          // recorder still running (no stop).
          return out("locked", { locked: true }, []);
        case "POINTER_CANCEL":
          // Already locked; a cancel changes nothing — keep recording.
          return out("locked", { locked: true }, []);
        default:
          return { state: s, actions: [] };
      }

    case "locked":
      switch (event.type) {
        case "POINTER_DOWN":
          // A press on a locked recording is a tap-to-stop in progress.
          return out("lockedPressed", { locked: true });
        case "KEY_ACTIVATE":
          // Keyboard activation while locked = stop + transcribe.
          return out("transcribing", {}, [{ type: "stop" }, { type: "unlock" }]);
        default:
          return { state: s, actions: [] };
      }

    case "lockedPressed":
      switch (event.type) {
        case "POINTER_UP":
        case "POINTER_CANCEL":
          // Release (or drift-off) completes the tap → stop + transcribe.
          return out("transcribing", {}, [{ type: "stop" }, { type: "unlock" }]);
        default:
          return { state: s, actions: [] };
      }

    case "transcribing":
      // Inert to input until the transcription resolves (TRANSCRIBE_DONE/RESET).
      // A trailing click must never submit stale composer text.
      if (event.type === "TRANSCRIBE_DONE") return out("idle", {}, []);
      return { state: s, actions: [] };

    default:
      return { state: s, actions: [] };
  }
}

// isRecordingPhase / isLockedPhase / isTranscribingPhase — small predicates the
// wrapper uses to pick the button icon/title without reaching into phase
// strings. "recording" here means the mic is live (held, slide-locked, locked,
// or a locked tap in progress).
export function isRecordingPhase(state) {
  const p = (state || INITIAL).phase;
  return p === "recording" || p === "recordingLocked" || p === "locked" || p === "lockedPressed";
}

export function isLockedPhase(state) {
  const p = (state || INITIAL).phase;
  return p === "recordingLocked" || p === "locked" || p === "lockedPressed";
}

export function isTranscribingPhase(state) {
  return (state || INITIAL).phase === "transcribing";
}

// showSlideHint — true only while a plain hold is recording and could still be
// locked by sliding up (drives the "Slide up to lock" affordance).
export function showSlideHint(state) {
  return (state || INITIAL).phase === "recording";
}
