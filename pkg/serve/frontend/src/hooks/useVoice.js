import { useState, useRef, useCallback, useEffect } from 'preact/hooks';

/**
 * useVoice — MediaRecorder → backend transcription → insert text.
 *
 * Returns { recording, transcribing, start, stop, cancel, toggle, supported }
 * - start(): begin recording (no-op if already recording/starting/transcribing)
 * - stop():  stop recording and transcribe
 * - cancel(): stop recording and DISCARD (no transcription) — used for
 *   accidental/too-short taps and gesture aborts
 * - toggle(): start if idle, stop if recording (click-to-toggle fallback)
 *
 * After stop, audio is sent to POST /api/transcribe. On success onTranscript is
 * called with the text; on any failure onError is called with a human-readable
 * message (nothing is swallowed silently).
 *
 * Recordings shorter than MIN_RECORDING_MS are discarded so an accidental
 * press-and-release doesn't fire a pointless transcription request.
 *
 * Concurrency: each recording session captures its own chunks/startedAt/discard
 * in closures (never shared refs), and a monotonic startToken lets stop()/
 * cancel() invalidate an async start() whose getUserMedia() hasn't resolved yet
 * — so releasing before the mic opens can't leave a recorder running.
 */
const MIN_RECORDING_MS = 400;

export function useVoice(onTranscript, onError) {
  const [recording, setRecording] = useState(false);
  const [transcribing, setTranscribing] = useState(false);

  const recorderRef = useRef(null);
  const startingRef = useRef(false);
  const transcribingRef = useRef(false);
  // Bumped by every start/stop/cancel; an in-flight async start() aborts if its
  // token is stale by the time getUserMedia() resolves.
  const startTokenRef = useRef(0);

  const supported = typeof MediaRecorder !== 'undefined' && !!navigator.mediaDevices?.getUserMedia;

  const reportError = useCallback((msg) => {
    if (onError) onError(msg);
    else console.error('Voice:', msg);
  }, [onError]);

  // finish stops the active recorder (if any). discard=true throws the audio
  // away; discard=false transcribes it (decision read by the recorder's own
  // onstop closure via the discard flag it captured).
  const finish = useCallback((discard) => {
    startTokenRef.current++; // invalidate any pending async start()
    startingRef.current = false;
    const rec = recorderRef.current;
    if (rec) {
      rec._discard = discard;
      if (rec.state !== 'inactive') rec.stop();
      recorderRef.current = null;
    }
    setRecording(false);
  }, []);

  const stop = useCallback(() => finish(false), [finish]);
  const cancel = useCallback(() => finish(true), [finish]);

  const start = useCallback(async () => {
    if (recorderRef.current || startingRef.current || transcribingRef.current) return;

    const token = ++startTokenRef.current;
    startingRef.current = true;

    let stream;
    try {
      stream = await navigator.mediaDevices.getUserMedia({ audio: true });
    } catch (e) {
      startingRef.current = false;
      setRecording(false);
      const name = e?.name || '';
      if (name === 'NotAllowedError' || name === 'SecurityError') {
        reportError('Microphone access denied. Allow it in your browser settings.');
      } else if (name === 'NotFoundError') {
        reportError('No microphone found.');
      } else {
        reportError('Could not start recording: ' + (e.message || String(e)));
      }
      return;
    }

    startingRef.current = false;

    // The user released / cancelled before the mic opened → don't record.
    if (token !== startTokenRef.current) {
      stream.getTracks().forEach(t => t.stop());
      return;
    }

    // Prefer webm/opus, fall back to whatever the browser supports.
    const mimeType = MediaRecorder.isTypeSupported('audio/webm;codecs=opus')
      ? 'audio/webm;codecs=opus'
      : MediaRecorder.isTypeSupported('audio/webm')
        ? 'audio/webm'
        : '';

    let recorder;
    try {
      recorder = new MediaRecorder(stream, mimeType ? { mimeType } : {});
    } catch (e) {
      stream.getTracks().forEach(t => t.stop());
      reportError('Could not start recording: ' + (e.message || String(e)));
      return;
    }

    // Per-recording state — captured in this closure, never shared, so a new
    // recording can't clobber a previous one's chunks/timing while its onstop
    // is still running.
    const chunks = [];
    const startedAt = Date.now();
    recorder._discard = false;

    recorder.ondataavailable = (e) => {
      if (e.data.size > 0) chunks.push(e.data);
    };

    recorder.onerror = (e) => {
      reportError('Recording failed: ' + (e.error?.message || 'unknown error'));
    };

    recorder.onstop = async () => {
      // Stop mic tracks so the browser indicator goes away.
      stream.getTracks().forEach(t => t.stop());

      const durationMs = Date.now() - startedAt;
      if (recorder._discard || durationMs < MIN_RECORDING_MS || chunks.length === 0) {
        return;
      }

      const blob = new Blob(chunks, { type: recorder.mimeType || 'audio/webm' });
      const ext = (recorder.mimeType || '').includes('webm') ? 'webm'
        : (recorder.mimeType || '').includes('mp4') ? 'mp4'
        : (recorder.mimeType || '').includes('ogg') ? 'ogg'
        : 'webm';

      transcribingRef.current = true;
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
        if (text) onTranscript(text);
        else reportError('No speech detected. Try again a bit closer to the mic.');
      } catch (e) {
        reportError('Transcription error: ' + (e.message || String(e)));
      } finally {
        transcribingRef.current = false;
        setTranscribing(false);
      }
    };

    // Only publish the recorder (and flip the UI) once it has actually started,
    // so a throwing start() can't leave a non-null recorderRef with a live mic
    // that blocks all future recordings.
    try {
      recorder.start();
    } catch (e) {
      stream.getTracks().forEach(t => t.stop());
      reportError('Could not start recording: ' + (e.message || String(e)));
      return;
    }
    recorderRef.current = recorder;
    setRecording(true);
  }, [onTranscript, reportError]);

  const toggle = useCallback(() => {
    if (recording) stop();
    else start();
  }, [recording, start, stop]);

  // Cleanup on unmount: discard any active/pending recording so a late timer or
  // getUserMedia resolution can't leave the mic open.
  useEffect(() => () => finish(true), [finish]);

  return { recording, transcribing, start, stop, cancel, toggle, supported };
}
