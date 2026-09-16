// voice-gestures.js — the pure tap-to-talk state machine.
//
// Voice is a toggle: tap the mic to start recording, tap it again to stop and
// transcribe. That is the whole model. It used to be press-and-hold with a
// slide-up to lock hands-free, which needed a hold timer, a slide threshold,
// four extra phases, and a delicate rule for iOS Safari's habit of firing
// `pointercancel` mid-hold (it hijacks the long press for Haptic Touch). A
// toggle has no long press, so that entire class of problem is gone rather than
// handled: `pointercancel` is now a no-op.
//
// The reducer owns only the DECISIONS. The thin hook around it
// (useVoiceGesture) owns the EFFECTS: the actual MediaRecorder via useVoice.
// On every event the reducer returns the next state plus an ordered list of
// `actions` the wrapper must perform:
//   - "start"    begin recording (useVoice.start)
//   - "stop"     stop + transcribe (useVoice.stop)
//   - "discard"  stop and throw the audio away (useVoice.cancel)
// Sending a typed message is NOT this machine's business any more. The host
// decides which control the button is: with a draft it is Send and wears the
// composer's own send handlers, with an empty field it is the mic and wears
// these. One button, but never two meanings for one tap.
//
// Recordings shorter than ~400ms are dropped by useVoice itself, so a
// double-tap that starts and immediately stops can't fire a transcription.
//
// Phases:
//   idle          not recording.
//   recording     the mic is live, hands-free. A tap stops it.
//   transcribing  recording stopped, audio is being transcribed. Inert to taps
//                 so a trailing click can't retrigger the mic.

export const INITIAL = { phase: "idle" };

// Event creators (documentation of the event shape the reducer accepts).
export const ev = {
  // The one gesture: a tap on the mic button, an Enter/Space activation, or the
  // ⌘. / Alt+. shortcut. They are the same decision, so they are one event.
  toggle: () => ({ type: "TOGGLE" }),
  // Abandon a live recording without transcribing it (Esc).
  cancel: () => ({ type: "CANCEL" }),
  transcribeDone: () => ({ type: "TRANSCRIBE_DONE" }),
  reset: () => ({ type: "RESET" }),
};

// reduce(state, event) → { state, actions } — pure. Never touches the DOM,
// timers, or MediaRecorder. Unknown events are a no-op (state unchanged, no
// actions) so the wrapper can forward events liberally.
export function reduce(state, event) {
  const s = state || INITIAL;

  // A RESET always returns to idle (unmount, hard error). No side actions — the
  // caller that resets owns whatever cleanup it already did.
  if (event.type === "RESET") return { state: INITIAL, actions: [] };

  switch (s.phase) {
    case "idle":
      if (event.type === "TOGGLE") {
        return { state: { phase: "recording" }, actions: [{ type: "start" }] };
      }
      return { state: s, actions: [] };

    case "recording":
      if (event.type === "TOGGLE") {
        return { state: { phase: "transcribing" }, actions: [{ type: "stop" }] };
      }
      if (event.type === "CANCEL") {
        // Esc drops the audio on the floor: back to idle with nothing to
        // transcribe, so no text ever reaches the caret.
        return { state: INITIAL, actions: [{ type: "discard" }] };
      }
      return { state: s, actions: [] };

    case "transcribing":
      // Inert to input until the transcription resolves (TRANSCRIBE_DONE/RESET).
      // A trailing click must never start a new recording on top of it.
      if (event.type === "TRANSCRIBE_DONE") return { state: INITIAL, actions: [] };
      return { state: s, actions: [] };

    default:
      return { state: s, actions: [] };
  }
}

// isRecordingPhase / isTranscribingPhase — small predicates the wrapper uses to
// pick the button face without reaching into phase strings.
export function isRecordingPhase(state) {
  return (state || INITIAL).phase === "recording";
}

export function isTranscribingPhase(state) {
  return (state || INITIAL).phase === "transcribing";
}
