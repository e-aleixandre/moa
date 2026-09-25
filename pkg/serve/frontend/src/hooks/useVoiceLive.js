import { useState, useRef, useCallback, useEffect } from 'preact/hooks';
import { VoiceLiveController } from '../data/voice-live.js';

const IDLE_STATE = {
  phase: 'idle',
  error: '',
  questionsUsed: 0,
  maxQuestions: 5,
  pendingAsks: 0,
  micState: 'unknown',
  voiceSeconds: 0,
  startedAt: 0,
  endedReason: '',
  active: false,
};

/**
 * useVoiceLive is the Preact adapter for VoiceLiveController. The transport,
 * the tool loop and the rescue rules live in the controller; this only mirrors
 * its state into a render and ticks the elapsed clock the panel shows.
 */
export function useVoiceLive(sessionId, { onResult, onError } = {}) {
  const [state, setState] = useState(IDLE_STATE);
  const [elapsed, setElapsed] = useState(0);
  const callbacksRef = useRef({});
  callbacksRef.current = { onResult, onError };

  const controllerRef = useRef(null);
  if (!controllerRef.current) {
    controllerRef.current = new VoiceLiveController({
      sessionId,
      onState: setState,
      onResult: (text, meta) => {
        if (text) callbacksRef.current.onResult?.(text, meta);
      },
      onError: (message) => callbacksRef.current.onError?.(message),
    });
  }
  // The composer is keyed by session upstream, but keep the id honest anyway:
  // a call must never POST against a session it was not opened from.
  controllerRef.current.sessionId = sessionId;

  useEffect(() => {
    if (state.phase !== 'live' || !state.startedAt) {
      setElapsed(0);
      return undefined;
    }
    const tick = () => setElapsed(Math.max(0, Math.floor((Date.now() - state.startedAt) / 1000)));
    tick();
    const timer = setInterval(tick, 1000);
    return () => clearInterval(timer);
  }, [state.phase, state.startedAt]);

  const start = useCallback(() => controllerRef.current.start(), []);
  const hangup = useCallback(() => controllerRef.current.hangup(), []);
  // The call's own microphone, for a level meter. Read-only: the call owns it.
  const getStream = useCallback(() => controllerRef.current.stream || null, []);

  useEffect(() => () => controllerRef.current.dispose(), []);

  const supported = typeof RTCPeerConnection !== 'undefined'
    && !!globalThis.navigator?.mediaDevices?.getUserMedia;

  return { ...state, elapsed, start, hangup, supported, getStream };
}
