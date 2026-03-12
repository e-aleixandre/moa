import { useRef, useCallback } from 'preact/hooks';
import { SendHorizonal, Square } from 'lucide-preact';
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
        placeholder="Send a message…"
        rows="1"
        onInput={autoResize}
        onKeyDown={handleKey}
        disabled={busy}
      />
      {busy ? (
        <button class="input-stop" onClick={handleStop}><Square /> Stop</button>
      ) : (
        <button class="input-send" onClick={handleSend} disabled={!sessionId}>
          <SendHorizonal /> Send
        </button>
      )}
    </div>
  );
}
