import { useState, useRef, useCallback, useEffect } from "preact/hooks";
import { useVoice } from "./useVoice.js";
import {
  reduce, ev, INITIAL, HOLD_MS,
  isRecordingPhase, isTranscribingPhase, isLockedPhase, showSlideHint as showSlideHintPred,
} from "../data/voice-gestures.js";

/**
 * useVoiceGesture — the thin EFFECT wrapper around the pure push-to-talk state
 * machine (data/voice-gestures.js) and useVoice (MediaRecorder → transcription).
 *
 * The reducer owns every DECISION (tap vs hold vs slide-lock, plus the iOS
 * pointercancel promotion) and is exhaustively unit-tested. This hook owns only
 * the EFFECTS the reducer deliberately doesn't touch: pointer capture, window
 * fallback listeners, preventDefault, the HOLD_MS timer, and running the actual
 * recorder. It maps the reducer's ordered `actions` onto those effects:
 *   "start"  → useVoice.start()   begin recording
 *   "stop"   → useVoice.stop()    stop + transcribe
 *   "send"   → onSend()           submit the composer (a plain tap / keyboard)
 *   "lock"   → visual lock on     (recorder keeps running hands-free)
 *   "unlock" → visual lock off
 *
 * Returns handlers ready to spread onto the send/record button plus the derived
 * UI flags (recording / transcribing / locked / showSlideHint / supported) and
 * toggleFromShortcut for the ⌘. / Alt+. keyboard shortcut.
 */
export function useVoiceGesture({ onTranscript, onError, onSend, sendOnPointerCancel = false } = {}) {
  // useVoice's error callback is routed through a ref so we can (a) reset the
  // gesture machine to idle — a getUserMedia failure from the shortcut/iOS path
  // would otherwise leave the button stuck visually in `locked` — and (b)
  // surface the message via the caller's onError. The ref is populated below,
  // once the reset helper exists.
  const voiceErrorRef = useRef(onError);
  const forwardVoiceError = useCallback((msg) => voiceErrorRef.current?.(msg), []);
  const {
    recording, transcribing, start: startVoice, stop: stopVoice,
    cancel: cancelVoice, completion, supported,
  } = useVoice(onTranscript, forwardVoiceError);

  // The reducer state lives in a ref (read fresh inside pointer handlers without
  // stale closures) mirrored by a state value that forces re-renders so the
  // button icon/classes follow the phase. Same ref+setState-mirror pattern the
  // old InputBar used for `recording`.
  const stateRef = useRef(INITIAL);
  const [uiState, setUiState] = useState(INITIAL);

  // Latest onSend, so dispatch can call it without being re-created (and without
  // closing over a stale sessionId/attachments closure at first render).
  const onSendRef = useRef(onSend);
  onSendRef.current = onSend;
  const sendOnPointerCancelRef = useRef(sendOnPointerCancel);
  sendOnPointerCancelRef.current = sendOnPointerCancel;

  // Active-hold bookkeeping (mirrors InputBar.holdRef): the HOLD_MS timer, the
  // captured pointerId + element, and the window fallback listeners we register
  // in case pointer capture isn't honored and the up/cancel lands off-button.
  const holdRef = useRef(null); // { pointerId, el, timer, onWinUp, onWinCancel } | null
  // True between a pointerdown and the synthetic click it triggers (mouse/touch),
  // so onClick can swallow that duplicate instead of treating it as a keyboard
  // activation (a bug the old InputBar had).
  const pointerDrivenRef = useRef(false);
  const clickSuppressTimer = useRef(null);
  const clearClickSuppressTimer = useCallback(() => {
    if (clickSuppressTimer.current != null) {
      clearTimeout(clickSuppressTimer.current);
      clickSuppressTimer.current = null;
    }
  }, []);

  // endHold tears down the active hold gesture (timer + window fallback
  // listeners + pointer capture). Safe to call multiple times.
  const endHold = useCallback(() => {
    const h = holdRef.current;
    if (!h) return;
    holdRef.current = null;
    clearTimeout(h.timer);
    if (h.onWinUp) {
      window.removeEventListener("pointerup", h.onWinUp);
      window.removeEventListener("pointercancel", h.onWinCancel);
    }
    try { h.el?.releasePointerCapture?.(h.pointerId); } catch (_) { /* ignore */ }
  }, []);

  // dispatch — the single choke point: fold an event through the pure reducer,
  // apply the resulting state (ref + mirror), then run the emitted actions.
  const dispatch = useCallback((event) => {
    const { state, actions } = reduce(stateRef.current, event);
    stateRef.current = state;
    setUiState(state);
    for (const a of actions) {
      switch (a.type) {
        case "start": startVoice(); break;
        case "stop":
          stopVoice();
          break;
        case "send": onSendRef.current?.(); break;
        case "lock": /* visual only — derived from state */ break;
        case "unlock": /* visual only — derived from state */ break;
        default: break;
      }
    }
  }, [startVoice, stopVoice]);

  // Bind the voice-error handler now that dispatch exists: a recorder error
  // (mic denied, no device, failed getUserMedia — reachable from the shortcut
  // and iOS-cancel paths that leave the machine in `locked`) must return the
  // gesture machine to idle so the button doesn't stay stuck, then surface the
  // message to the caller. Kept in a ref so useVoice's callback identity is
  // stable.
  const handleVoiceError = useCallback((msg) => {
    if (stateRef.current.phase !== "idle") dispatch(ev.reset());
    onError?.(msg);
  }, [dispatch, onError]);
  voiceErrorRef.current = handleVoiceError;

  const onPointerDown = useCallback((e) => {
    if (e.button != null && e.button !== 0) return; // primary / touch only
    const el = e.currentTarget;

    // A previous hold is still active (a stray second pointerdown, e.g. a second
    // finger, before the first released): tear it down first so its timer and
    // window listeners can't leak. We only track one gesture at a time.
    endHold();

    // Kill WebKit's long-press callout/selection before it can start (it later
    // fires pointercancel and used to drop the recording).
    if (e.pointerType === "touch" && e.cancelable) e.preventDefault();

    clearClickSuppressTimer();
    pointerDrivenRef.current = true;

    try { el.setPointerCapture?.(e.pointerId); } catch (_) { /* ignore */ }

    // Window fallback: if pointer capture isn't honored and the up/cancel lands
    // off the button, still route the event so the gesture completes. Filter by
    // pointerId so an unrelated pointer's up/cancel can't finish this gesture.
    const pid = e.pointerId;
    const onWinUp = (we) => {
      if (we.pointerId !== pid) return;
      pointerDrivenRef.current = false; endHold(); dispatch(ev.pointerUp());
    };
    const onWinCancel = (we) => {
      if (we.pointerId !== pid) return;
      pointerDrivenRef.current = false; endHold(); dispatch(ev.pointerCancel());
    };
    window.addEventListener("pointerup", onWinUp);
    window.addEventListener("pointercancel", onWinCancel);

    const h = { pointerId: pid, el, timer: null, onWinUp, onWinCancel };
    holdRef.current = h;
    // Arm the intentional-hold timer; firing it promotes holding → recording.
    h.timer = setTimeout(() => {
      if (holdRef.current !== h) return;
      dispatch(ev.holdTimer());
    }, HOLD_MS);

    dispatch(ev.pointerDown(e.clientY ?? 0));
  }, [dispatch, endHold, clearClickSuppressTimer]);

  const onPointerMove = useCallback((e) => {
    dispatch(ev.pointerMove(e.clientY ?? 0));
  }, [dispatch]);

  const onPointerUp = useCallback(() => {
    // A pointerup that lands on the button synthesizes a click afterwards; keep
    // pointerDrivenRef set so onClick swallows it instead of double-firing.
    // Self-clear after a beat in case no click arrives.
    clearClickSuppressTimer();
    clickSuppressTimer.current = setTimeout(() => {
      pointerDrivenRef.current = false;
      clickSuppressTimer.current = null;
    }, 700);
    endHold();
    dispatch(ev.pointerUp());
  }, [dispatch, endHold, clearClickSuppressTimer]);

  const onPointerCancel = useCallback(() => {
    // Usually pointercancel has no click, but WebKit may still emit one after
    // the fallback send below. Treat it like pointerup: consume that trailing
    // click while a fresh pointerdown clears this guard for the next gesture.
    clearClickSuppressTimer();
    pointerDrivenRef.current = true;
    clickSuppressTimer.current = setTimeout(() => {
      pointerDrivenRef.current = false;
      clickSuppressTimer.current = null;
    }, 700);
    endHold();
    dispatch(ev.pointerCancel(sendOnPointerCancelRef.current));
  }, [dispatch, endHold, clearClickSuppressTimer]);

  const onClick = useCallback(() => {
    // Swallow the synthetic click that follows a pointer sequence (mouse/touch);
    // the pointerup already handled it.
    if (pointerDrivenRef.current) {
      pointerDrivenRef.current = false;
      clearClickSuppressTimer();
      return;
    }
    // Genuine keyboard activation (Enter/Space) with no pointer sequence.
    dispatch(ev.keyActivate());
  }, [dispatch, clearClickSuppressTimer]);

  const onContextMenu = useCallback((e) => e.preventDefault(), []);

  // toggleFromShortcut — the ⌘. / Alt+. keyboard shortcut. Routes a single
  // pointer-free SHORTCUT_TOGGLE through the reducer: from idle it starts a
  // hands-free recording (parked in `locked`), and while recording it stops +
  // transcribes. All decisions and effects go through the normal dispatch path
  // — no out-of-band state.
  const toggleFromShortcut = useCallback(() => {
    dispatch(ev.shortcutToggle());
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

  // Cleanup on unmount: tear down the timer + window listeners and discard any
  // active/pending recording, then reset the reducer.
  useEffect(() => () => {
    endHold();
    clearClickSuppressTimer();
    cancelVoice();
    stateRef.current = INITIAL;
  }, [endHold, clearClickSuppressTimer, cancelVoice]);

  return {
    handlers: { onPointerDown, onPointerMove, onPointerUp, onPointerCancel, onClick, onContextMenu },
    recording: isRecordingPhase(uiState) || recording,
    transcribing,
    locked: isLockedPhase(uiState),
    showSlideHint: showSlideHintPred(uiState),
    supported,
    toggleFromShortcut,
  };
}
