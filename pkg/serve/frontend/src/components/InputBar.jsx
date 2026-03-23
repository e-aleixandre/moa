import { useRef, useCallback, useEffect, useState } from 'preact/hooks';
import { SendHorizonal, Mic, MicOff, Loader2 } from 'lucide-preact';
import { sendMessage, cancelRun, execCommand, execShell, resolvePermission, addPermissionRule } from '../session-actions.js';
import { useVoice } from '../hooks/useVoice.js';
import { formatShortcut } from '../hooks/useHotkeys.js';
import { addToast } from '../notifications.js';
import { store, updateSession } from '../store.js';
import { FileSuggestions } from './FileSuggestions.jsx';

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
  { name: 'thinking', desc: 'Set thinking level', args: '<off|low|medium|high|xhigh>' },
  { name: 'permissions', desc: 'Set permission mode', args: '<yolo|ask|auto>' },
  { name: 'plan', desc: 'Enter/exit plan mode', args: '[exit]' },
  { name: 'tasks', desc: 'View/manage tasks', args: '[done <id> | reset]' },
];

export function InputBar({ sessionId, session, tileId }) {
  const textareaRef = useRef(null);
  const sessionState = session?.state;
  const pendingSteers = session?.pendingSteers;
  const busy = sessionState === 'running';
  const [canTranscribe, setCanTranscribe] = useState(false);
  const [cmdSuggestions, setCmdSuggestions] = useState(null); // null = hidden
  const [cmdCursor, setCmdCursor] = useState(0);
  const [fileSuggestions, setFileSuggestions] = useState(null); // [{path, is_dir}] or null
  const [fileCursor, setFileCursor] = useState(0);
  const fileAbortRef = useRef(null);
  const fileDebounceRef = useRef(null);
  const feedbackRef = useRef(null);

  const permissionActive = sessionState === 'permission' && !!session?.pendingPerm;
  const [permFeedbackOpen, setPermFeedbackOpen] = useState(false);
  const [permFeedback, setPermFeedback] = useState('');
  const [permRuleOpen, setPermRuleOpen] = useState(false);
  const [permRule, setPermRule] = useState('');
  const [permBusy, setPermBusy] = useState(false);
  const [permError, setPermError] = useState('');

  // Check if transcription is available on mount.
  useEffect(() => {
    fetch('/api/capabilities', { headers: { 'X-Moa-Request': '1' } })
      .then(r => r.json())
      .then(caps => setCanTranscribe(!!caps.transcribe))
      .catch(() => {});
  }, []);

  const insertAtCursor = useCallback((text) => {
    const el = (permissionActive && permFeedbackOpen)
      ? feedbackRef.current
      : textareaRef.current;
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
  }, [permissionActive, permFeedbackOpen]);

  const { recording, transcribing, toggle: toggleVoice, supported: voiceSupported } = useVoice(insertAtCursor);

  useEffect(() => {
    if (!permissionActive) {
      setPermFeedbackOpen(false);
      setPermFeedback('');
      setPermRuleOpen(false);
      setPermRule('');
      setPermError('');
      setPermBusy(false);
      return;
    }
    setPermFeedback('');
    setPermRuleOpen(false);
    setPermRule('');
    setPermError('');
  }, [permissionActive, session?.pendingPerm?.id]);

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

  // --- File suggestions (@mention) ---
  const cancelFileRequest = useCallback(() => {
    if (fileAbortRef.current) {
      fileAbortRef.current.abort();
      fileAbortRef.current = null;
    }
  }, []);

  // Cleanup on unmount.
  useEffect(() => {
    return () => {
      cancelFileRequest();
      clearTimeout(fileDebounceRef.current);
    };
  }, [cancelFileRequest]);

  const updateFileSuggestions = useCallback(() => {
    const el = textareaRef.current;
    if (!el || !sessionId) return;
    const val = el.value;
    const cursor = el.selectionStart;

    // Walk backwards from cursor to find @.
    let atIdx = -1;
    for (let i = cursor - 1; i >= 0; i--) {
      if (val[i] === '@') { atIdx = i; break; }
      if (/\s/.test(val[i])) break;
    }

    if (atIdx < 0 || (atIdx > 0 && !/\s/.test(val[atIdx - 1]))) {
      cancelFileRequest();
      setFileSuggestions(null);
      return;
    }

    const filter = val.slice(atIdx + 1, cursor);
    if (/\s/.test(filter)) {
      cancelFileRequest();
      setFileSuggestions(null);
      return;
    }

    // Abort previous request.
    cancelFileRequest();
    const controller = new AbortController();
    fileAbortRef.current = controller;

    fetch(`/api/sessions/${sessionId}/files?q=${encodeURIComponent(filter)}&limit=50`, {
      signal: controller.signal,
      headers: { 'X-Moa-Request': '1' },
    })
    .then(r => r.json())
    .then(items => {
      if (!controller.signal.aborted) {
        setFileSuggestions(items.length > 0 ? items : null);
        setFileCursor(0);
      }
    })
    .catch(() => {}); // aborted or network error
  }, [sessionId, cancelFileRequest]);

  const acceptFileMention = useCallback((path, isDir) => {
    const el = textareaRef.current;
    if (!el) return;
    const val = el.value;
    const cursor = el.selectionStart;

    // Find the @ backwards.
    let atIdx = -1;
    for (let i = cursor - 1; i >= 0; i--) {
      if (val[i] === '@') { atIdx = i; break; }
      if (/\s/.test(val[i])) break;
    }
    const before = atIdx >= 0 ? val.slice(0, atIdx) : val.slice(0, cursor);
    const after = val.slice(cursor);

    if (isDir) {
      // Navigate into directory: keep @, update filter to dir/.
      const replacement = '@' + path + '/';
      el.value = before + replacement + after;
      el.selectionStart = el.selectionEnd = before.length + replacement.length;
      setFileSuggestions(null);
      // Re-trigger to show directory contents.
      setTimeout(updateFileSuggestions, 50);
    } else {
      // Accept file: remove @, insert path + space.
      el.value = before + path + ' ' + after;
      el.selectionStart = el.selectionEnd = before.length + path.length + 1;
      setFileSuggestions(null);
    }
    el.dispatchEvent(new Event('input', { bubbles: true }));
    el.focus();
  }, [updateFileSuggestions]);

  // --- Dequeue steers ---
  const handleDequeueSteers = useCallback(() => {
    const sess = store.get().sessions[sessionId];
    if (!sess?.pendingSteers?.length) return;

    const el = textareaRef.current;
    if (!el) return;

    const combined = sess.pendingSteers.join('\n');
    const current = el.value;
    el.value = current ? current + '\n' + combined : combined;

    updateSession(sessionId, { pendingSteers: null });

    autoResize();
    el.focus();
    el.selectionStart = el.selectionEnd = el.value.length;
  }, [sessionId, autoResize]);

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
    setFileSuggestions(null);
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
    // Alt+ArrowUp: dequeue pending steers to input (parity with TUI).
    if (e.key === 'ArrowUp' && e.altKey) {
      const sess = store.get().sessions[sessionId];
      if (sess?.pendingSteers?.length) {
        e.preventDefault();
        handleDequeueSteers();
        return;
      }
    }

    // File suggestion navigation (takes priority over cmd suggestions).
    if (fileSuggestions) {
      if (e.key === 'ArrowUp') {
        e.preventDefault();
        setFileCursor(i => Math.max(0, i - 1));
        return;
      }
      if (e.key === 'ArrowDown') {
        e.preventDefault();
        setFileCursor(i => Math.min(fileSuggestions.length - 1, i + 1));
        return;
      }
      if (e.key === 'Tab' || (e.key === 'Enter' && !e.shiftKey)) {
        e.preventDefault();
        const item = fileSuggestions[fileCursor];
        acceptFileMention(item.path, item.is_dir);
        return;
      }
      if (e.key === 'Escape') {
        e.preventDefault();
        setFileSuggestions(null);
        return;
      }
    }

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

    // Esc aborts running agent.
    if (e.key === 'Escape' && busy) {
      e.preventDefault();
      handleStop();
      return;
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
    // File suggestions with debounce.
    clearTimeout(fileDebounceRef.current);
    fileDebounceRef.current = setTimeout(updateFileSuggestions, 100);
  };

  const handleStop = async () => {
    if (!sessionId) return;
    try {
      await cancelRun(sessionId);
    } catch (e) {
      console.error('Cancel failed:', e);
    }
  };

  const handlePermissionResolve = async (approved, alwaysAllow = false) => {
    if (!sessionId || !session?.pendingPerm || permBusy) return;
    setPermBusy(true);
    setPermError('');
    try {
      await resolvePermission(sessionId, session.pendingPerm.id, approved, {
        feedback: permFeedback.trim(),
        allow: alwaysAllow ? (session.pendingPerm.allow_pattern || '') : '',
      });
      setPermFeedbackOpen(false);
      setPermFeedback('');
      setPermRuleOpen(false);
      setPermRule('');
    } catch (e) {
      console.error('Permission resolve failed:', e);
      setPermError(e.message || 'Permission resolve failed');
    } finally {
      setPermBusy(false);
    }
  };

  const handlePermissionRule = async () => {
    if (!sessionId || !session?.pendingPerm || permBusy) return;
    const rule = permRule.trim();
    if (!rule) return;
    setPermBusy(true);
    setPermError('');
    try {
      await addPermissionRule(sessionId, session.pendingPerm.id, rule);
      setPermRule('');
      setPermRuleOpen(false);
    } catch (e) {
      console.error('Add permission rule failed:', e);
      setPermError(e.message || 'Could not add rule');
    } finally {
      setPermBusy(false);
    }
  };

  // Derive activity label from session state.
  let activityLabel = null;
  if (session?.autoVerifying) {
    activityLabel = 'Running auto-verify…';
  } else if (busy) {
    if (session?.thinkingText) activityLabel = 'Thinking…';
    else if (session?.streamingText) activityLabel = 'Generating…';
    else if (session?.runningTool) activityLabel = `Running ${session.runningTool}…`;
    else activityLabel = 'Working…';
  }

  const permissionMode = session?.permissionMode || 'yolo';

  return (
    <div class={`input-bar ${busy ? 'busy' : ''} ${permissionActive ? 'permission-active' : ''}`}>
      {permissionActive ? (
        <div class="permission-prompt-bar">
          <div class="permission-prompt-head">
            <span class="permission-prompt-title">Permission required</span>
            {permError && <span class="permission-prompt-error">{permError}</span>}
          </div>

          <div class="permission-prompt-actions">
            <button class="btn-approve" disabled={permBusy} onClick={() => handlePermissionResolve(true)}>
              Approve
            </button>

            {permissionMode === 'ask' && (
              <button class="btn-approve permission-always" disabled={permBusy} onClick={() => handlePermissionResolve(true, true)}>
                Always allow
              </button>
            )}

            <button class="btn-deny" disabled={permBusy} onClick={() => handlePermissionResolve(false)}>
              Deny
            </button>

            {permissionMode === 'auto' && (
              <button class="permission-rule-toggle" disabled={permBusy} onClick={() => setPermRuleOpen(v => !v)}>
                Add rule
              </button>
            )}

            <button class="permission-feedback-toggle" disabled={permBusy} onClick={() => setPermFeedbackOpen(v => !v)}>
              + feedback
            </button>
          </div>

          {permRuleOpen && permissionMode === 'auto' && (
            <div class="permission-inline-editor">
              <input
                type="text"
                value={permRule}
                onInput={(e) => setPermRule(e.target.value)}
                placeholder="Type rule and press Save rule"
              />
              <button class="permission-rule-save" disabled={permBusy || !permRule.trim()} onClick={handlePermissionRule}>
                Save rule
              </button>
            </div>
          )}

          {permFeedbackOpen && (
            <div class="permission-inline-editor">
              <input
                ref={feedbackRef}
                type="text"
                value={permFeedback}
                onInput={(e) => setPermFeedback(e.target.value)}
                placeholder="Optional feedback"
              />
              {canTranscribe && (
                <button
                  class={`input-mic permission-mic ${recording ? 'recording' : ''} ${transcribing ? 'transcribing' : ''} ${!voiceSupported ? 'unavailable' : ''}`}
                  onClick={handleMicClick}
                  disabled={transcribing}
                  title={!voiceSupported ? 'Voice input (requires HTTPS)' : recording ? `Stop recording (${formatShortcut('.', { mod: true })})` : transcribing ? 'Transcribing…' : `Voice input (${formatShortcut('.', { mod: true })})`}
                >
                  {transcribing ? <Loader2 /> : recording ? <MicOff /> : <Mic />}
                </button>
              )}
            </div>
          )}
        </div>
      ) : (
        <>
          {(busy || session?.autoVerifying) && activityLabel && (
            <div class="input-activity">
              <Loader2 class="input-activity-spinner" />
              <span class="input-activity-label">{activityLabel}</span>
              {busy && (
                <button class="input-activity-abort" onClick={handleStop} title="Stop (Esc)">
                  Esc to abort
                </button>
              )}
            </div>
          )}
          {!busy && pendingSteers && pendingSteers.length > 0 && (
            <button class="input-steers" onClick={handleDequeueSteers} title="Click or Alt+↑ to edit queued messages">
              {pendingSteers.length === 1
                ? <span class="input-steer-text">{pendingSteers[0]}</span>
                : <span class="input-steer-text">{pendingSteers[pendingSteers.length - 1]} <span class="input-steer-count">+{pendingSteers.length - 1}</span></span>
              }
              <span class="input-steer-badge">queued · click to edit</span>
            </button>
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
            {fileSuggestions && !cmdSuggestions && (
              <FileSuggestions
                items={fileSuggestions}
                cursor={fileCursor}
                onSelect={acceptFileMention}
                onHover={setFileCursor}
              />
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
          <button
            class={`input-send ${busy ? 'steer' : ''}`}
            onClick={handleSend}
            disabled={!sessionId}
            title={busy ? 'Steer' : 'Send'}
          >
            <SendHorizonal />
          </button>
        </>
      )}
    </div>
  );
}
