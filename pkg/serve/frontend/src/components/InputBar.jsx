import { useRef, useCallback, useEffect, useState } from 'preact/hooks';
import { SendHorizonal, Square, Zap, Mic, MicOff, Loader2 } from 'lucide-preact';
import { sendMessage, cancelRun, execCommand, execShell } from '../session-actions.js';
import { useVoice } from '../hooks/useVoice.js';
import { formatShortcut } from '../hooks/useHotkeys.js';
import { addToast } from '../notifications.js';

// Global registry: tileId → { toggleVoice }. Used by keyboard shortcuts.
export const inputBarRegistry = new Map();

// Per-session input history (survives component re-renders, not page reload).
const sessionHistories = new Map();
function getHistory(id) {
  if (!sessionHistories.has(id)) sessionHistories.set(id, { entries: [], idx: -1, draft: '' });
  return sessionHistories.get(id);
}
const MAX_HISTORY = 100;

// Available commands for the suggestion popup.
const COMMANDS = [
  { name: 'clear', desc: 'Clear conversation history' },
  { name: 'compact', desc: 'Compact conversation context' },
  { name: 'model', desc: 'Switch model', args: '<model>' },
  { name: 'thinking', desc: 'Set thinking level', args: '<off|low|medium|high>' },
  { name: 'permissions', desc: 'Set permission mode', args: '<yolo|ask|auto>' },
  { name: 'plan', desc: 'Enter/exit plan mode', args: '[exit]' },
  { name: 'tasks', desc: 'View/manage tasks', args: '[done <id> | reset]' },
];

export function InputBar({ sessionId, sessionState, tileId, pendingSteers }) {
  const textareaRef = useRef(null);
  const busy = sessionState === 'running' || sessionState === 'permission';
  const [canTranscribe, setCanTranscribe] = useState(false);
  const [cmdSuggestions, setCmdSuggestions] = useState(null); // null = hidden
  const [cmdCursor, setCmdCursor] = useState(0);

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
    const sep = before.length > 0 && !/\s$/.test(before) ? ' ' : '';
    el.value = before + sep + text + after;
    const newPos = start + sep.length + text.length;
    el.selectionStart = el.selectionEnd = newPos;
    el.focus();
    el.dispatchEvent(new Event('input', { bubbles: true }));
  }, []);

  const { recording, transcribing, toggle: toggleVoice, supported: voiceSupported } = useVoice(insertAtCursor);

  const handleMicClick = useCallback(() => {
    if (voiceSupported) {
      toggleVoice();
    } else {
      addToast({
        title: 'Voice input requires HTTPS',
        detail: 'Serve moa behind HTTPS (e.g. Tailscale, Caddy, or mkcert) to enable microphone access.',
        type: 'attention',
      });
    }
  }, [voiceSupported, toggleVoice]);

  // Register in global map so keyboard shortcuts can trigger voice toggle
  useEffect(() => {
    if (tileId != null && canTranscribe) {
      inputBarRegistry.set(tileId, { toggleVoice: handleMicClick });
      return () => inputBarRegistry.delete(tileId);
    }
  }, [tileId, canTranscribe, handleMicClick]);

  const autoResize = useCallback(() => {
    const el = textareaRef.current;
    if (!el) return;
    el.style.height = 'auto';
    el.style.height = Math.min(el.scrollHeight, 120) + 'px';
  }, []);

  const pushHistory = useCallback((text) => {
    if (!sessionId) return;
    const h = getHistory(sessionId);
    if (h.entries.length === 0 || h.entries[h.entries.length - 1] !== text) {
      h.entries.push(text);
      if (h.entries.length > MAX_HISTORY) h.entries.splice(0, h.entries.length - MAX_HISTORY);
    }
    h.idx = -1;
    h.draft = '';
  }, [sessionId]);

  // Update command suggestions on input change.
  // Only show suggestions while the user is still typing the command name
  // (before the first space). Once they've moved on to arguments, hide them.
  const updateSuggestions = useCallback(() => {
    const el = textareaRef.current;
    if (!el) return;
    const val = el.value;
    if (val.startsWith('/') && !val.includes('\n')) {
      const afterSlash = val.slice(1);
      // Once there's a space, the user is typing arguments — stop suggesting.
      if (afterSlash.includes(' ')) {
        setCmdSuggestions(null);
        return;
      }
      const filter = afterSlash.toLowerCase();
      const matches = COMMANDS.filter(c => c.name.startsWith(filter));
      if (matches.length > 0) {
        setCmdSuggestions(matches);
        setCmdCursor(0);
        return;
      }
    }
    setCmdSuggestions(null);
  }, []);

  const acceptSuggestion = useCallback((cmd) => {
    const el = textareaRef.current;
    if (!el) return;
    if (cmd.args) {
      el.value = '/' + cmd.name + ' ';
      setCmdSuggestions(null);
      el.focus();
    } else {
      el.value = '/' + cmd.name;
      setCmdSuggestions(null);
      handleSendInner(el);
    }
  }, [sessionId]); // eslint-disable-line react-hooks/exhaustive-deps

  const handleSendInner = async (el) => {
    if (!el || !sessionId) return;
    const text = el.value.trim();
    if (!text) return;
    pushHistory(text);
    el.value = '';
    setCmdSuggestions(null);
    autoResize();

    // Detect slash commands.
    if (text.startsWith('/')) {
      try {
        const result = await execCommand(sessionId, text);
        if (result && !result.ok) {
          addToast({ title: 'Command failed', detail: result.message, type: 'error' });
        }
      } catch (e) {
        addToast({ title: 'Command error', detail: e.message, type: 'error' });
      }
      return;
    }

    // Shell escape: !! = silent (user-only), ! = context (sent with next message)
    if (text.startsWith('!')) {
      const silent = text.startsWith('!!');
      const command = (silent ? text.slice(2) : text.slice(1)).trim();
      if (!command) return;
      try {
        const result = await execShell(sessionId, command, silent);
        if (!result) return;
        // Show output as a tool-call-like block via sendMessage won't work here —
        // we add it to the session's pending shell context on the server side.
        // For now, just show the output inline. The state.execShell handles
        // adding the tool block to the message list and storing context.
      } catch (e) {
        addToast({ title: 'Shell error', detail: e.message, type: 'error' });
      }
      return;
    }

    try {
      await sendMessage(sessionId, text);
    } catch (e) {
      console.error('Send failed:', e);
    }
  };

  const handleSend = () => handleSendInner(textareaRef.current);

  // Returns the row the cursor is on (0-indexed).
  const cursorRow = useCallback(() => {
    const el = textareaRef.current;
    if (!el) return 0;
    const before = el.value.substring(0, el.selectionStart);
    return (before.match(/\n/g) || []).length;
  }, []);

  const totalRows = useCallback(() => {
    const el = textareaRef.current;
    if (!el) return 1;
    return (el.value.match(/\n/g) || []).length + 1;
  }, []);

  const handleKey = (e) => {
    // Command suggestion navigation.
    if (cmdSuggestions) {
      if (e.key === 'ArrowUp') {
        e.preventDefault();
        setCmdCursor(i => Math.max(0, i - 1));
        return;
      }
      if (e.key === 'ArrowDown') {
        e.preventDefault();
        setCmdCursor(i => Math.min(cmdSuggestions.length - 1, i + 1));
        return;
      }
      if (e.key === 'Tab' || (e.key === 'Enter' && !e.shiftKey)) {
        e.preventDefault();
        acceptSuggestion(cmdSuggestions[cmdCursor]);
        return;
      }
      if (e.key === 'Escape') {
        e.preventDefault();
        setCmdSuggestions(null);
        return;
      }
    }

    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      handleSend();
      return;
    }

    if (!sessionId) return;
    const h = getHistory(sessionId);

    if (e.key === 'ArrowUp' && cursorRow() === 0 && h.entries.length > 0) {
      e.preventDefault();
      const el = textareaRef.current;
      if (h.idx === -1) {
        h.draft = el.value;
        h.idx = h.entries.length - 1;
      } else if (h.idx > 0) {
        h.idx--;
      }
      el.value = h.entries[h.idx];
      autoResize();
      el.selectionStart = el.selectionEnd = el.value.length;
      updateSuggestions();
      return;
    }

    if (e.key === 'ArrowDown' && h.idx !== -1 && cursorRow() === totalRows() - 1) {
      e.preventDefault();
      const el = textareaRef.current;
      h.idx++;
      if (h.idx >= h.entries.length) {
        h.idx = -1;
        el.value = h.draft;
        h.draft = '';
      } else {
        el.value = h.entries[h.idx];
      }
      autoResize();
      el.selectionStart = el.selectionEnd = el.value.length;
      updateSuggestions();
      return;
    }
  };

  const handleInput = (e) => {
    autoResize();
    updateSuggestions();
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
      {pendingSteers && pendingSteers.length > 0 && (
        <div class="input-steers">
          {pendingSteers.length === 1
            ? <span class="input-steer-text">{pendingSteers[0]}</span>
            : <span class="input-steer-text">{pendingSteers[pendingSteers.length - 1]} <span class="input-steer-count">+{pendingSteers.length - 1}</span></span>
          }
          <span class="input-steer-badge">queued</span>
        </div>
      )}
      <div class="input-wrap">
        {cmdSuggestions && (
          <div class="cmd-suggestions">
            {cmdSuggestions.map((cmd, i) => (
              <div
                key={cmd.name}
                class={`cmd-suggestion-item ${i === cmdCursor ? 'selected' : ''}`}
                onMouseDown={(e) => { e.preventDefault(); acceptSuggestion(cmd); }}
                onMouseEnter={() => setCmdCursor(i)}
              >
                <span class="cmd-suggestion-name">/{cmd.name}</span>
                {cmd.args && <span class="cmd-suggestion-args">{cmd.args}</span>}
                <span class="cmd-suggestion-desc">{cmd.desc}</span>
              </div>
            ))}
          </div>
        )}
        <textarea
          ref={textareaRef}
          placeholder={busy ? 'Steer the agent…' : 'Send a message…'}
          rows="1"
          onInput={handleInput}
          onKeyDown={handleKey}
        />
        {canTranscribe && (
          <button
            class={`input-mic ${recording ? 'recording' : ''} ${transcribing ? 'transcribing' : ''} ${!voiceSupported ? 'unavailable' : ''}`}
            onClick={handleMicClick}
            disabled={transcribing}
            title={!voiceSupported ? 'Voice input (requires HTTPS)' : recording ? `Stop recording (${formatShortcut('.', { mod: true })})` : transcribing ? 'Transcribing…' : `Voice input (${formatShortcut('.', { mod: true })})`}
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
