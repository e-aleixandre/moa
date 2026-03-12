import { useState, useRef, useCallback } from 'preact/hooks';
import { api } from '../api.js';

/**
 * useVoice — MediaRecorder → backend transcription → insert text.
 *
 * Returns { recording, transcribing, toggle, supported }
 * - toggle(): start/stop recording
 * - After stop, audio is sent to POST /api/transcribe
 * - On success, calls onTranscript(text)
 */
export function useVoice(onTranscript) {
  const [recording, setRecording] = useState(false);
  const [transcribing, setTranscribing] = useState(false);
  const recorderRef = useRef(null);
  const chunksRef = useRef([]);

  const supported = typeof MediaRecorder !== 'undefined' && !!navigator.mediaDevices?.getUserMedia;

  const stop = useCallback(() => {
    const rec = recorderRef.current;
    if (rec && rec.state !== 'inactive') {
      rec.stop();
    }
    recorderRef.current = null;
    setRecording(false);
  }, []);

  const start = useCallback(async () => {
    try {
      const stream = await navigator.mediaDevices.getUserMedia({ audio: true });
      chunksRef.current = [];

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

      recorder.onstop = async () => {
        // Stop all mic tracks so the browser indicator goes away.
        stream.getTracks().forEach(t => t.stop());

        const chunks = chunksRef.current;
        if (chunks.length === 0) return;

        const blob = new Blob(chunks, { type: recorder.mimeType || 'audio/webm' });
        chunksRef.current = [];

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
            const errText = await resp.text();
            console.error('Transcription failed:', errText);
            return;
          }

          const data = await resp.json();
          if (data.text) {
            onTranscript(data.text);
          }
        } catch (e) {
          console.error('Transcription error:', e);
        } finally {
          setTranscribing(false);
        }
      };

      recorder.start();
      setRecording(true);
    } catch (e) {
      console.error('Mic access denied:', e);
      setRecording(false);
    }
  }, [onTranscript]);

  const toggle = useCallback(() => {
    if (recording) {
      stop();
    } else {
      start();
    }
  }, [recording, start, stop]);

  return { recording, transcribing, toggle, supported };
}
