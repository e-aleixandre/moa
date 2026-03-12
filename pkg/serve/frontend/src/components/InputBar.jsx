import { useRef, useCallback } from 'preact/hooks';
import { SendHorizonal, Square, Zap } from 'lucide-preact';
import { sendMessage, cancelRun } from '../state.js';

export function InputBar({ sessionId, sessionState }) {
  const textareaRef = useRef(null);
  const busy = sessionState === 'running' || sessionState === 'permission';

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
      <textarea
        ref={textareaRef}
        placeholder={busy ? 'Steer the agent…' : 'Send a message…'}
        rows="1"
        onInput={autoResize}
        onKeyDown={handleKey}
      />
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
