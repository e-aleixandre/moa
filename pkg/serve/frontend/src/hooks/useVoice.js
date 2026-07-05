import { useState, useRef, useCallback } from 'preact/hooks';

/**
 * useVoice — MediaRecorder → backend transcription → insert text.
 *
 * Returns { recording, transcribing, start, stop, cancel, toggle, supported }
 * - start(): begin recording (no-op if already recording/transcribing)
 * - stop():  stop recording and transcribe
 * - cancel(): stop recording and DISCARD (no transcription) — used for
 *   accidental/too-short taps
 * - toggle(): start if idle, stop if recording (click-to-toggle fallback)
 *
 * After stop, audio is sent to POST /api/transcribe. On success onTranscript is
 * called with the text; on any failure onError is called with a human-readable
 * message (nothing is swallowed silently).
 *
 * Recordings shorter than MIN_RECORDING_MS are discarded so an accidental
 * press-and-release doesn't fire a pointless transcription request.
 */
const MIN_RECORDING_MS = 400;

export function useVoice(onTranscript, onError) {
  const [recording, setRecording] = useState(false);
  const [transcribing, setTranscribing] = useState(false);
  const recorderRef = useRef(null);
  const chunksRef = useRef([]);
  const startedAtRef = useRef(0);
  const discardRef = useRef(false);

  const supported = typeof MediaRecorder !== 'undefined' && !!navigator.mediaDevices?.getUserMedia;

  const reportError = useCallback((msg) => {
    if (onError) onError(msg);
    else console.error('Voice:', msg);
  }, [onError]);

  // stop finishes the recording and transcribes it.
  const stop = useCallback(() => {
    const rec = recorderRef.current;
    if (rec && rec.state !== 'inactive') {
      discardRef.current = false;
      rec.stop();
    }
    recorderRef.current = null;
    setRecording(false);
  }, []);

  // cancel finishes the recording and throws it away (no transcription).
  const cancel = useCallback(() => {
    const rec = recorderRef.current;
    if (rec && rec.state !== 'inactive') {
      discardRef.current = true;
      rec.stop();
    }
    recorderRef.current = null;
    setRecording(false);
  }, []);

  const start = useCallback(async () => {
    if (recorderRef.current) return; // already recording
    try {
      const stream = await navigator.mediaDevices.getUserMedia({ audio: true });
      chunksRef.current = [];
      discardRef.current = false;
      startedAtRef.current = Date.now();

      // Prefer webm/opus, fall back to whatever the browser supports.
      const mimeType = MediaRecorder.isTypeSupported('audio/webm;codecs=opus')
        ? 'audio/webm;codecs=opus'
        : MediaRecorder.isTypeSupported('audio/webm')
          ? 'audio/webm'
          : '';

      const recorder = new MediaRecorder(stream, mimeType ? { mimeType } : {});
      recorderRef.current = recorder;

      recorder.ondataavailable = (e) => {
        if (e.data.size > 0) chunksRef.current.push(e.data);
      };

      recorder.onerror = (e) => {
        reportError('Recording failed: ' + (e.error?.message || 'unknown error'));
      };

      recorder.onstop = async () => {
        // Stop all mic tracks so the browser indicator goes away.
        stream.getTracks().forEach(t => t.stop());

        const chunks = chunksRef.current;
        chunksRef.current = [];

        // Discarded (cancel) or accidental tap that was too short → drop it
        // silently, no transcription request.
        const durationMs = Date.now() - startedAtRef.current;
        if (discardRef.current || durationMs < MIN_RECORDING_MS || chunks.length === 0) {
          return;
        }

        const blob = new Blob(chunks, { type: recorder.mimeType || 'audio/webm' });

        // Determine file extension from mime type.
        const ext = (recorder.mimeType || '').includes('webm') ? 'webm'
          : (recorder.mimeType || '').includes('mp4') ? 'mp4'
          : (recorder.mimeType || '').includes('ogg') ? 'ogg'
          : 'webm';

        setTranscribing(true);
        try {
          const form = new FormData();
          form.append('audio', blob, `recording.${ext}`);

          const resp = await fetch('/api/transcribe', {
            method: 'POST',
            headers: { 'X-Moa-Request': '1' },
            body: form,
          });

          if (!resp.ok) {
            const errText = (await resp.text()).trim();
            reportError(errText || `Transcription failed (HTTP ${resp.status})`);
            return;
          }

          const data = await resp.json();
          const text = (data.text || '').trim();
          if (text) {
            onTranscript(text);
          } else {
            reportError('No speech detected. Try again a bit closer to the mic.');
          }
        } catch (e) {
          reportError('Transcription error: ' + (e.message || String(e)));
        } finally {
          setTranscribing(false);
        }
      };

      recorder.start();
      setRecording(true);
    } catch (e) {
      recorderRef.current = null;
      setRecording(false);
      const name = e?.name || '';
      if (name === 'NotAllowedError' || name === 'SecurityError') {
        reportError('Microphone access denied. Allow it in your browser settings.');
      } else if (name === 'NotFoundError') {
        reportError('No microphone found.');
      } else {
        reportError('Could not start recording: ' + (e.message || String(e)));
      }
    }
  }, [onTranscript, reportError]);

  const toggle = useCallback(() => {
    if (recording) stop();
    else start();
  }, [recording, start, stop]);

  return { recording, transcribing, start, stop, cancel, toggle, supported };
}
