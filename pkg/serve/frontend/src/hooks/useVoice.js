import { useState, useRef, useCallback, useEffect } from 'preact/hooks';
import { VoiceCaptureController } from '../data/voice-capture.js';

/**
 * useVoice is the Preact adapter for VoiceCaptureController. Capture ownership,
 * microphone arbitration, recorder callbacks, and transcription live in the
 * controller so they can be tested without rendering a component.
 */
export function useVoice(onTranscript, onError) {
  const [recording, setRecording] = useState(false);
  const [transcribing, setTranscribing] = useState(false);
  const [completion, setCompletion] = useState(0);
  const callbacksRef = useRef({});
  callbacksRef.current = { onTranscript, onError };

  const controllerRef = useRef(null);
  if (!controllerRef.current) {
    controllerRef.current = new VoiceCaptureController({
      onState: ({ recording: nextRecording, transcribing: nextTranscribing }) => {
        setRecording(nextRecording);
        setTranscribing(nextTranscribing);
      },
      onTranscript: (text) => callbacksRef.current.onTranscript?.(text),
      onError: (message) => {
        if (callbacksRef.current.onError) callbacksRef.current.onError(message);
        else console.error('Voice:', message);
      },
      onComplete: () => setCompletion((value) => value + 1),
    });
  }

  const start = useCallback(() => controllerRef.current.start(), []);
  const stop = useCallback(() => controllerRef.current.stop(), []);
  const cancel = useCallback(() => controllerRef.current.cancel(), []);
  const toggle = useCallback(() => {
    if (recording) stop();
    else start();
  }, [recording, start, stop]);

  useEffect(() => () => {
    // Do not let an unmounted composer receive a late recorder/fetch callback.
    callbacksRef.current = {};
    controllerRef.current.dispose();
  }, []);

  const supported = typeof MediaRecorder !== 'undefined'
    && !!globalThis.navigator?.mediaDevices?.getUserMedia;
  return { recording, transcribing, completion, start, stop, cancel, toggle, supported };
}
