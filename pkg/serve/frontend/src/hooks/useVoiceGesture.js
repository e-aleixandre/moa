import { useState, useRef, useCallback, useEffect } from "preact/hooks";
import { useVoice } from "./useVoice.js";
import {
  reduce, ev, INITIAL, isRecordingPhase, isTranscribingPhase,
} from "../data/voice-gestures.js";

/**
 * useVoiceGesture — the thin EFFECT wrapper around the pure tap-to-talk state
 * machine (data/voice-gestures.js) and useVoice (MediaRecorder → transcription).
 *
 * Voice is a toggle: tap to record, tap again to stop and transcribe. Because
 * there is no long press any more, this hook no longer needs pointer capture,
 * window fallback listeners, a hold timer, or the click-suppression guard that
 * existed to tell a tap apart from a hold. A plain `onClick` is the whole
 * pointer story — it fires for touch, mouse and keyboard alike, exactly once.
 *
 * The reducer owns every DECISION and is unit-tested; this hook maps its
 * ordered `actions` onto effects:
 *   "start"   → useVoice.start()   begin recording
 *   "stop"    → useVoice.stop()    stop + transcribe
 *   "discard" → useVoice.cancel()  stop and throw the audio away (Esc)
 *
 * Returns handlers for the mic button plus the derived UI flags (recording /
 * transcribing / supported), `cancel` for Esc, and toggleFromShortcut for
 * ⌘. / Alt+. — which is now the same action as a tap.
 */
export function useVoiceGesture({ onTranscript, onError } = {}) {
  // useVoice's error callback is routed through a ref so we can (a) reset the
  // gesture machine to idle — a getUserMedia failure would otherwise leave the
  // button stuck visually in `recording` — and (b) surface the message via the
  // caller's onError. The ref is populated below, once dispatch exists.
  const voiceErrorRef = useRef(onError);
  const forwardVoiceError = useCallback((msg) => voiceErrorRef.current?.(msg), []);
  const {
    recording, transcribing, start: startVoice, stop: stopVoice,
    cancel: cancelVoice, completion, supported,
  } = useVoice(onTranscript, forwardVoiceError);

  // The reducer state lives in a ref (read fresh inside handlers without stale
  // closures) mirrored by a state value that forces re-renders so the button
  // face follows the phase.
  const stateRef = useRef(INITIAL);
  const [uiState, setUiState] = useState(INITIAL);

  // dispatch — the single choke point: fold an event through the pure reducer,
  // apply the resulting state (ref + mirror), then run the emitted actions.
  const dispatch = useCallback((event) => {
    const { state, actions } = reduce(stateRef.current, event);
    stateRef.current = state;
    setUiState(state);
    for (const a of actions) {
      switch (a.type) {
        case "start": startVoice(); break;
        case "stop": stopVoice(); break;
        case "discard": cancelVoice(); break;
        default: break;
      }
    }
  }, [startVoice, stopVoice, cancelVoice]);

  // Bind the voice-error handler now that dispatch exists: a recorder error
  // (mic denied, no device, failed getUserMedia) must return the machine to
  // idle so the button doesn't stay stuck, then surface the message to the
  // caller. Kept in a ref so useVoice's callback identity is stable.
  const handleVoiceError = useCallback((msg) => {
    if (stateRef.current.phase !== "idle") dispatch(ev.reset());
    onError?.(msg);
  }, [dispatch, onError]);
  voiceErrorRef.current = handleVoiceError;

  // One handler for every activation — touch, mouse and keyboard all arrive
  // here as a click, so tap-to-start / tap-to-stop needs nothing else.
  const onClick = useCallback(() => {
    dispatch(ev.toggle());
  }, [dispatch]);

  // toggleFromShortcut — ⌘. / Alt+. is the same decision as a tap.
  const toggleFromShortcut = onClick;

  // cancel — Esc while recording: stop the mic and throw the audio away, so an
  // abandoned recording never lands in the field.
  const cancel = useCallback(() => {
    dispatch(ev.cancel());
  }, [dispatch]);

  // Every capture attempt reports completion exactly once, including recordings
  // discarded for being short/empty. This is deterministic, unlike guessing
  // from a timeout whether a transcription ever started.
  const previousCompletionRef = useRef(completion);
  useEffect(() => {
    if (previousCompletionRef.current !== completion && isTranscribingPhase(stateRef.current)) {
      dispatch(ev.transcribeDone());
    }
    previousCompletionRef.current = completion;
  }, [completion, dispatch]);

  // Cleanup on unmount: discard any active/pending recording and reset.
  useEffect(() => () => {
    cancelVoice();
    stateRef.current = INITIAL;
  }, [cancelVoice]);

  return {
    handlers: { onClick },
    recording: isRecordingPhase(uiState) || recording,
    transcribing,
    supported,
    cancel,
    toggleFromShortcut,
  };
}
