import { useRef, useCallback, useEffect, useState } from 'preact/hooks';
import { SendHorizonal, Square, Zap, Mic, MicOff, Loader2 } from 'lucide-preact';
import { sendMessage, cancelRun } from '../state.js';
import { useVoice } from '../hooks/useVoice.js';

export function InputBar({ sessionId, sessionState }) {
  const textareaRef = useRef(null);
  const busy = sessionState === 'running' || sessionState === 'permission';
  const [canTranscribe, setCanTranscribe] = useState(false);

  // Check if transcription is available on mount.
  useEffect(() => {
    fetch('/api/capabilities', { headers: { 'X-Moa-Request': '1' } })
      .then(r => r.json())
      .then(caps => setCanTranscribe(!!caps.transcribe))
      .catch(() => {});
  }, []);

  const insertAtCursor = useCallback((text) => {
    const el = textareaRef.current;
    if (!el) return;
    const start = el.selectionStart;
    const end = el.selectionEnd;
    const before = el.value.substring(0, start);
    const after = el.value.substring(end);
    // Add a space before if there's already text and it doesn't end with whitespace.
    const sep = before.length > 0 && !/\s$/.test(before) ? ' ' : '';
    el.value = before + sep + text + after;
    const newPos = start + sep.length + text.length;
    el.selectionStart = el.selectionEnd = newPos;
    el.focus();
    // Trigger resize.
    el.dispatchEvent(new Event('input', { bubbles: true }));
  }, []);

  const { recording, transcribing, toggle: toggleVoice, supported: voiceSupported } = useVoice(insertAtCursor);
  const showMic = canTranscribe && voiceSupported;

  const autoResize = useCallback(() => {
    const el = textareaRef.current;
    if (!el) return;
    el.style.height = 'auto';
    el.style.height = Math.min(el.scrollHeight, 120) + 'px';
  }, []);

  const handleSend = async () => {
    const el = textareaRef.current;
    if (!el || !sessionId) return;
    const text = el.value.trim();
    if (!text) return;
    el.value = '';
    autoResize();
    try {
      await sendMessage(sessionId, text);
    } catch (e) {
      console.error('Send failed:', e);
    }
  };

  const handleKey = (e) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      handleSend();
    }
  };

  const handleStop = async () => {
    if (!sessionId) return;
    try {
      await cancelRun(sessionId);
    } catch (e) {
      console.error('Cancel failed:', e);
    }
  };

  return (
    <div class="input-bar">
      <div class="input-wrap">
        <textarea
          ref={textareaRef}
          placeholder={busy ? 'Steer the agent…' : 'Send a message…'}
          rows="1"
          onInput={autoResize}
          onKeyDown={handleKey}
        />
        {showMic && (
          <button
            class={`input-mic ${recording ? 'recording' : ''} ${transcribing ? 'transcribing' : ''}`}
            onClick={toggleVoice}
            disabled={transcribing}
            title={recording ? 'Stop recording' : transcribing ? 'Transcribing…' : 'Voice input'}
          >
            {transcribing ? <Loader2 /> : recording ? <MicOff /> : <Mic />}
          </button>
        )}
      </div>
      {busy && (
        <button class="input-stop" onClick={handleStop} title="Stop"><Square /></button>
      )}
      <button
        class={`input-send ${busy ? 'steer' : ''}`}
        onClick={handleSend}
        disabled={!sessionId}
        title={busy ? 'Steer' : 'Send'}
      >
        {busy ? <Zap /> : <SendHorizonal />}
      </button>
    </div>
  );
}
