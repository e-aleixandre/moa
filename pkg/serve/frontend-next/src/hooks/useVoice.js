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

// Minimum plausible size of a real recording. An empty container (webm/mp4
// header with no audio frames) is only a handful of bytes; Whisper rejects it
// with a 400. Anything below this is treated as "no audio captured". Kept small
// so genuinely short commands ("sí", "no") still go through.
const MIN_BLOB_BYTES = 256;

// Apple's WebKit (Safari, and every iOS browser / WKWebView, which are all
// WebKit under the hood) advertises audio/webm support but its MediaRecorder
// actually produces MP4/AAC — and sometimes an empty webm container. Detect it
// so we can request MP4 explicitly instead of trusting a webm that comes back
// as a 5-byte header.
const isAppleWebKit = typeof navigator !== 'undefined'
  && (/iP(hone|ad|od)/.test(navigator.userAgent || '')
    || /^((?!chrome|android|crios|fxios).)*safari/i.test(navigator.userAgent || ''));

// isUsableMime rejects empty or malformed container hints like
// "audio/webm; codecs=" (trailing empty codecs) that some engines report — we
// don't want to label an upload with those or fall back to them.
function isUsableMime(t) {
  if (!t) return false;
  const s = t.toLowerCase();
  if (/codecs=\s*$/.test(s)) return false; // "audio/webm; codecs="
  return /^(audio|video)\//.test(s);
}

// extForType maps a container MIME to a filename extension Whisper accepts.
// Returns '' when the type is unknown so callers can bail instead of guessing.
function extForType(t) {
  const s = (t || '').toLowerCase();
  if (s.includes('webm')) return 'webm';
  if (s.includes('ogg')) return 'ogg';
  if (s.includes('wav')) return 'wav';
  if (s.includes('mp4') || s.includes('m4a') || s.includes('aac') || s.includes('mpeg') || s.includes('mpga')) return 'mp4';
  return '';
}

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

    // Pick a container the browser can actually encode. On Apple WebKit, webm
    // is falsely advertised but produces empty/garbage output, so request
    // MP4/AAC first. Everywhere else, prefer webm/opus. Asking for a concrete
    // type keeps recorder.mimeType populated and our extension honest.
    const preferredTypes = isAppleWebKit
      ? [
          'audio/mp4;codecs=mp4a.40.2',
          'audio/mp4',
          'audio/aac',
          'audio/webm;codecs=opus',
          'audio/webm',
        ]
      : [
          'audio/webm;codecs=opus',
          'audio/webm',
          'audio/ogg;codecs=opus',
          'audio/mp4;codecs=mp4a.40.2',
          'audio/mp4',
        ];
    const mimeType = preferredTypes.find(t => MediaRecorder.isTypeSupported(t)) || '';

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
    // The requested mimeType is our most reliable signal for the container:
    // recorder.mimeType can come back empty on some browsers (notably iOS
    // Safari), which previously made us mislabel an mp4 recording as .webm and
    // earned an "invalid file format" 400 from Whisper. Capture it now and
    // reconcile with recorder.mimeType (once it's populated) below.
    const requestedMime = mimeType;

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

      // Determine the real container. Prefer, in order: the actual type of the
      // recorded chunks, then a valid recorder.mimeType, then what we asked for.
      // Reject empty/malformed hints like "audio/webm; codecs=". If we still
      // can't tell, bail rather than mislabel the upload and earn a 400.
      const chunkType = chunks.find(c => isUsableMime(c.type))?.type || '';
      const reportedType = isUsableMime(recorder.mimeType) ? recorder.mimeType : '';
      const effectiveType = chunkType || reportedType || (isUsableMime(requestedMime) ? requestedMime : '');

      const blob = new Blob(chunks, effectiveType ? { type: effectiveType } : {});

      // A container header with no audio frames is only a few dozen bytes. We've
      // seen tiny uploads (e.g. 5-byte empty webm) when the mic never delivered
      // samples — another app holding it, or a WebKit encoder producing nothing.
      // Sending that earns an "invalid file format" 400 from Whisper.
      if (blob.size < MIN_BLOB_BYTES) {
        reportError('No audio captured. Make sure the mic isn\u0027t in use by another app, then try again.');
        return;
      }

      const ext = extForType(effectiveType || blob.type);
      if (!ext) {
        reportError('This browser produced an audio format we can\u0027t transcribe. Try a different browser.');
        return;
      }

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
      // Pass a timeslice so the recorder emits dataavailable periodically while
      // recording, instead of only once on stop(). Some engines (notably iOS
      // Safari / WKWebView) deliver an empty final chunk when started without a
      // timeslice — which produced 5-byte "empty webm" uploads that Whisper
      // rejects with a 400. Chunking guarantees we accumulate real audio.
      recorder.start(1000);
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
