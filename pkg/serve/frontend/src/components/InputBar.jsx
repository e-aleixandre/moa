import { useRef, useCallback, useEffect, useState } from 'preact/hooks';
import { SendHorizonal, Mic, MicOff, Square, Loader2, Paperclip, X, ChevronUp } from 'lucide-preact';
import { sendMessage, cancelRun, cancelSteers, execCommand, execShell, resolvePermission, addPermissionRule, steerSubagent, newSteerId } from '../session-actions.js';
import { useVoice } from '../hooks/useVoice.js';
import { formatShortcut } from '../hooks/useHotkeys.js';
import { addToast } from '../notifications.js';
import { store, updateSession } from '../store.js';
import { FileSuggestions } from './FileSuggestions.jsx';
import { processFile } from '../util/attachments.js';
import { classifyCommand, POLICY_QUEUE, POLICY_REJECT } from '../util/command-policy.js';
import { activityPhase, activityLabel as buildActivityLabel, formatElapsed } from '../util/activity.js';

const MAX_ATTACHMENTS = 8;

// Global registry: tileId → { toggleVoice }. Used by keyboard shortcuts.
export const inputBarRegistry = new Map();

// Per-session input history (survives component re-renders, not page reload).
const sessionHistories = new Map();
function getHistory(id) {
  if (!sessionHistories.has(id)) sessionHistories.set(id, { entries: [], idx: -1, draft: '' });
  return sessionHistories.get(id);
}
const MAX_HISTORY = 100;

// Per-session unsent draft, persisted to localStorage so a page reload (iOS
// evicts backgrounded PWAs freely) doesn't lose what you were typing.
const DRAFT_PREFIX = 'moa-draft-';
function loadDraft(id) {
  if (!id) return '';
  try { return localStorage.getItem(DRAFT_PREFIX + id) || ''; } catch (_) { return ''; }
}
function saveDraft(id, text) {
  if (!id) return;
  try {
    if (text) localStorage.setItem(DRAFT_PREFIX + id, text);
    else localStorage.removeItem(DRAFT_PREFIX + id);
  } catch (_) { /* ignore */ }
}

// Available commands for the suggestion popup.
const COMMANDS = [
  { name: 'clear', desc: 'Clear conversation history' },
  { name: 'compact', desc: 'Compact conversation context' },
  { name: 'model', desc: 'Switch model', args: '<model>' },
  { name: 'thinking', desc: 'Set thinking level', args: '<off|low|medium|high|xhigh>' },
  { name: 'permissions', desc: 'Set permission mode', args: '<yolo|ask|auto>' },
  { name: 'plan', desc: 'Enter/exit plan mode', args: '[exit]' },
  { name: 'goal', desc: 'Autonomous maker→verifier loop', args: '<objective> [flags]|stop|status' },
  { name: 'tasks', desc: 'View/manage tasks', args: '[done <id> | reset]' },
  { name: 'verify', desc: 'Run verification checks' },
  { name: 'undo', desc: 'Undo last file change' },
  { name: 'path', desc: 'Manage path access scope', args: '[list|add <dir>|rm <dir>|scope workspace|unrestricted]' },
  { name: 'rename', desc: 'Rename this conversation', args: '<title>' },
  { name: 'schedule', desc: 'Schedule a prompt in this conversation', args: 'at <date> <time> [zone] -- <task> | in <duration> -- <task> | list | cancel <id>' },
];

export function InputBar({ sessionId, session, tileId }) {
  const textareaRef = useRef(null);
  const sessionState = session?.state;
  const pendingSteers = session?.pendingSteers;
  const busy = sessionState === 'running';
  // When viewing a subagent, the input targets that subagent instead of the
  // main agent. `steerJobId` is set iff a live subagent view is open.
  const steerJobId = session?.viewingSubagent || null;
  const steerSub = steerJobId ? (session?.subagents || {})[steerJobId] : null;
  const subagentMode = !!steerSub && steerSub.kind !== 'bash';
  // Optimistic list of messages sent to the current subagent (no WS echo).
  const [subagentPending, setSubagentPending] = useState([]);
  // Reset the optimistic queue whenever the targeted subagent changes / closes.
  useEffect(() => {
    setSubagentPending([]);
  }, [steerJobId]);
  const [canTranscribe, setCanTranscribe] = useState(false);
  const [goalFlags, setGoalFlags] = useState([]);
  const [cmdSuggestions, setCmdSuggestions] = useState(null); // null = hidden
  const [cmdCursor, setCmdCursor] = useState(0);
  const [fileSuggestions, setFileSuggestions] = useState(null); // [{path, is_dir}] or null
  const [fileCursor, setFileCursor] = useState(0);
  const fileAbortRef = useRef(null);
  const fileDebounceRef = useRef(null);
  const feedbackRef = useRef(null);
  const attachInputRef = useRef(null);
  const [attachments, setAttachments] = useState([]);
  // Whether the textarea currently has content — drives Send vs Mic icon.
  const [hasText, setHasText] = useState(false);

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
      .then(caps => {
        setCanTranscribe(!!caps.transcribe);
        setGoalFlags(Array.isArray(caps.goal_flags) ? caps.goal_flags : []);
      })
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

  const onVoiceError = useCallback((msg) => {
    addToast({ title: 'Voice input', detail: msg, type: 'error' });
  }, []);

  const { recording, transcribing, start: startVoice, stop: stopVoice, supported: voiceSupported } = useVoice(insertAtCursor, onVoiceError);

  // Push-to-talk gesture state. `voiceLocked` means the user slid up while
  // holding to lock recording hands-free (Telegram-style); then a tap stops.
  const [voiceLocked, setVoiceLocked] = useState(false);
  const holdRef = useRef(null); // { startY, longPress, locked, pointerId, el, timer, onWinUp, onWinCancel } while pressing
  const pointerDrivenRef = useRef(false); // true between pointerdown and the click it synthesizes (mouse), to suppress duplicate send

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

  // --- Push-to-talk gesture ---
  // Tap the main button = send. Press-and-hold = record while held; release to
  // stop+transcribe. Slide up past a threshold while holding = lock recording
  // (hands-free); then a tap stops. Recordings shorter than ~400ms are dropped
  // by useVoice so an accidental tap doesn't fire a transcription.
  const LOCK_SLIDE_PX = 48;
  const HOLD_MS = 180; // press longer than this = intentional hold (record)

  const notifyNoVoice = useCallback(() => {
    addToast({
      title: 'Voice input requires HTTPS',
      detail: 'Serve moa behind HTTPS (e.g. Tailscale, Caddy, or mkcert) to enable microphone access.',
      type: 'attention',
    });
  }, []);

  // Keyboard shortcut / external toggle (Cmd+.). Uses simple toggle semantics.
  const handleMicToggle = useCallback(() => {
    if (!voiceSupported) { notifyNoVoice(); return; }
    if (recording) { setVoiceLocked(false); stopVoice(); }
    else { startVoice(); }
  }, [voiceSupported, recording, startVoice, stopVoice, notifyNoVoice]);

  // recordingRef mirrors `recording` so the pointer handlers read a fresh value
  // without needing it in their dependency arrays (and without stale closures).
  const recordingRef = useRef(recording);
  recordingRef.current = recording;

  // clickSuppressTimer holds the pending self-clear of pointerDrivenRef (see the
  // locked-stop path). Cancelling it when the click is consumed or a new pointer
  // sequence starts prevents a stale timeout from clearing a later interaction's
  // flag (bug #1 review).
  const clickSuppressTimer = useRef(null);
  const clearClickSuppressTimer = useCallback(() => {
    if (clickSuppressTimer.current != null) {
      clearTimeout(clickSuppressTimer.current);
      clickSuppressTimer.current = null;
    }
  }, []);

  // endHold tears down the active hold gesture (timer, pointer capture, window
  // fallback listeners). Safe to call multiple times.
  const endHold = useCallback((e) => {
    const h = holdRef.current;
    if (!h) return;
    holdRef.current = null;
    clearTimeout(h.timer);
    if (h.onWinUp) {
      window.removeEventListener('pointerup', h.onWinUp);
      window.removeEventListener('pointercancel', h.onWinCancel);
    }
    const el = e?.currentTarget || h.el;
    try { el?.releasePointerCapture?.(h.pointerId); } catch (_) { /* ignore */ }
    return h;
  }, []);

  const finishHold = useCallback((h) => {
    if (!h) {
      // No active hold — this is a tap while locked-recording → stop & transcribe.
      if (recordingRef.current) { setVoiceLocked(false); stopVoice(); }
      return;
    }
    // h.longPress: recording was (or is being) started by this hold.
    if (h.longPress) {
      if (h.locked) return; // locked: keep recording; a later tap stops.
      stopVoice();          // plain hold-and-release → stop + transcribe.
      return;
    }
    // Quick tap (released before HOLD_MS).
    if (recordingRef.current) {
      setVoiceLocked(false);
      stopVoice();          // tap while (locked-)recording → stop + transcribe.
      return;
    }
    handleSendRef.current(); // idle tap → send.
  }, [stopVoice]);

  // handleGestureCancel deals with a pointercancel. On iOS Safari, the system
  // fires pointercancel while the user is STILL holding — it hijacks the
  // long-press for Haptic Touch / the callout menu, typically ~1-2s in. Treating
  // that as "abort and discard" was killing the MediaRecorder (and the mic
  // track) mid-recording. Instead, if recording already started, promote the
  // gesture to locked (hands-free) mode and keep the audio flowing; a later tap
  // stops and transcribes. Only a cancel *before* recording began is discarded.
  const handleGestureCancel = useCallback((e) => {
    pointerDrivenRef.current = false;
    const h = endHold(e);
    if (h && h.longPress) {
      if (!h.locked) setVoiceLocked(true); // visual; recorder keeps running
      return;
    }
    if (h && h.longPress === false) {
      // Cancelled before HOLD_MS elapsed: nothing was recorded, nothing to do.
      return;
    }
    // No active hold: a stray cancel (e.g. while locked-recording). Leave the
    // recording alone — the user stops it with a tap.
  }, [endHold]);

  const onSendPointerDown = useCallback((e) => {
    if (e.button != null && e.button !== 0) return; // primary/touch only
    if (!voiceSupported) return;      // no mic → send-only (onClick)

    const el = e.currentTarget;

    // Already recording (locked): this press is a tap to stop. Register global
    // pointerup/cancel so we stop even if the finger drifts off the button
    // before release (the element's own pointerup might not fire then).
    if (recordingRef.current) {
      const pid = e.pointerId;
      try { el.setPointerCapture?.(pid); } catch (_) { /* ignore */ }
      const done = () => {
        window.removeEventListener('pointerup', onUp);
        window.removeEventListener('pointercancel', onUp);
        try { el.releasePointerCapture?.(pid); } catch (_) { /* ignore */ }
      };
      const onUp = (ev) => {
        done();
        setVoiceLocked(false);
        stopVoice();
        // A tap on the button to stop a locked recording. If this pointerup
        // lands on the button (capture honored), the browser synthesizes a
        // click afterwards; keep pointerDrivenRef true so onClick swallows it
        // instead of falling through to handleSend and sending stale text
        // (bug #1). A pointercancel — or a pointerup that lands off the button —
        // synthesizes no click, so clear the flag immediately to avoid
        // swallowing a later legitimate activation.
        const onButton = ev?.type === 'pointerup' && el.contains?.(ev.target);
        if (onButton) {
          pointerDrivenRef.current = true;
          clearClickSuppressTimer();
          clickSuppressTimer.current = setTimeout(() => {
            pointerDrivenRef.current = false;
            clickSuppressTimer.current = null;
          }, 700);
        } else {
          pointerDrivenRef.current = false;
        }
      };
      window.addEventListener('pointerup', onUp);
      window.addEventListener('pointercancel', onUp);
      return;
    }

    const h = { startY: e.clientY ?? 0, longPress: false, locked: false, pointerId: e.pointerId, el };
    holdRef.current = h;
    try { el.setPointerCapture?.(e.pointerId); } catch (_) { /* ignore */ }

    // Fallback: if pointer capture isn't honored and the up/cancel lands off
    // the button, still finish the gesture from the window. No click is
    // synthesized in that path, so clear the suppression flag ourselves.
    h.onWinUp = () => { pointerDrivenRef.current = false; const hh = endHold(); finishHold(hh); };
    h.onWinCancel = (ev) => handleGestureCancel(ev);
    window.addEventListener('pointerup', h.onWinUp);
    window.addEventListener('pointercancel', h.onWinCancel);

    h.timer = setTimeout(() => {
      if (holdRef.current !== h) return;
      h.longPress = true;
      startVoice();
    }, HOLD_MS);
  }, [voiceSupported, startVoice, stopVoice, endHold, finishHold, handleGestureCancel]);

  const onSendPointerMove = useCallback((e) => {
    const h = holdRef.current;
    if (!h || !h.longPress || h.locked) return;
    const dy = h.startY - (e.clientY ?? 0);
    if (dy >= LOCK_SLIDE_PX) {
      h.locked = true;      // gesture source of truth (not React state)
      setVoiceLocked(true); // visual only
    }
  }, []);

  const onSendPointerUp = useCallback((e) => {
    // Pass through even when there was no active hold: that case is a tap on the
    // button while a locked recording is in progress → finishHold stops it.
    finishHold(endHold(e));
  }, [endHold, finishHold]);

  const onSendPointerCancel = useCallback((e) => {
    handleGestureCancel(e);
  }, [handleGestureCancel]);

  // Tear down any dangling gesture on unmount.
  useEffect(() => () => { endHold(); clearClickSuppressTimer(); }, [endHold, clearClickSuppressTimer]);

  // Register in global map so keyboard shortcuts can trigger voice toggle
  useEffect(() => {
    if (tileId != null && canTranscribe) {
      inputBarRegistry.set(tileId, { toggleVoice: handleMicToggle });
      return () => inputBarRegistry.delete(tileId);
    }
  }, [tileId, canTranscribe, handleMicToggle]);

  const autoResize = useCallback(() => {
    const el = textareaRef.current;
    if (!el) return;
    el.style.height = 'auto';
    el.style.height = Math.min(el.scrollHeight, 120) + 'px';
  }, []);

  // Restore the persisted draft when this input binds to a session (mount or
  // session switch). The previous session's draft was already saved on input.
  useEffect(() => {
    const el = textareaRef.current;
    if (!el) return;
    el.value = loadDraft(sessionId);
    setHasText(!!el.value.trim());
    autoResize();
  }, [sessionId, autoResize]);

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
  // For "/goal ..." lines, also offer flag autocompletion: if the token under
  // the cursor starts with "-", suggest matching flags from goalFlags.
  const updateSuggestions = useCallback(() => {
    const el = textareaRef.current;
    if (!el) return;
    const val = el.value;
    if (val.startsWith('/') && !val.includes('\n')) {
      const afterSlash = val.slice(1);
      // Once there's a space, the user is typing arguments — stop suggesting
      // the command name itself, but /goal gets flag autocompletion instead.
      if (afterSlash.includes(' ')) {
        if (val.startsWith('/goal ')) {
          const cursor = el.selectionStart;
          let tokenStart = cursor;
          while (tokenStart > 0 && val[tokenStart - 1] !== ' ') tokenStart--;
          const token = val.slice(tokenStart, cursor);
          if (token.startsWith('-')) {
            const filter = token.toLowerCase();
            const isBare = filter === '-' || filter === '--';
            const matches = goalFlags.filter(f => {
              if (!isBare && !f.name.toLowerCase().startsWith(filter)) return false;
              // Exclude flags already present as a token in the text.
              const re = new RegExp(`(^|\\s)${f.name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}(\\s|$)`);
              return !re.test(val);
            }).map(f => ({ name: f.name, desc: f.desc, args: f.placeholder, __flag: true }));
            if (matches.length > 0) {
              setCmdSuggestions(matches);
              setCmdCursor(0);
              return;
            }
          }
        }
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
  }, [goalFlags]);

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

    // Every chip has a client-minted ID and is queued (or in flight) on the
    // agent, so pull them all into the textarea for editing. Command chips carry
    // their full "/command" text; message chips carry their text. Queued images
    // can't be pulled back — their base64 payload was never tracked client-side
    // (only the count), so warn that they're dropped and must be re-attached.
    const combined = sess.pendingSteers.map((s) => s.text).join('\n');
    const droppedImages = sess.pendingSteers.reduce((n, s) => n + (s.images || 0), 0);
    const current = el.value;
    el.value = current ? current + '\n' + combined : combined;

    if (droppedImages > 0) {
      addToast({ sessionId, title: 'Queued images dropped', detail: `${droppedImages} attached image${droppedImages > 1 ? 's were' : ' was'} not restored — re-attach if still needed.`, type: 'attention' });
    }

    // Cancel the not-yet-delivered steers on the server so re-submitting the
    // edited text doesn't deliver both the originals and the edit. The server
    // broadcasts steers_canceled to every client (shared queue), which clears
    // the chips. On failure the chips stay, reflecting the still-queued truth.
    // Mirrors the TUI's Alt+Up dequeue.
    cancelSteers(sessionId).catch((e) => {
      console.error('cancelSteers failed:', e);
      addToast({ sessionId, title: 'Could not cancel queued messages', detail: e.message, type: 'error' });
    });

    autoResize();
    el.focus();
    el.selectionStart = el.selectionEnd = el.value.length;
  }, [sessionId, autoResize]);

  // --- Attachments ---
  const addFiles = useCallback(async (fileList) => {
    const files = Array.from(fileList || []);
    if (files.length === 0) return;

    const room = MAX_ATTACHMENTS - attachments.length;
    if (room <= 0) {
      addToast({ title: 'Too many attachments', detail: `Max ${MAX_ATTACHMENTS} per message`, type: 'attention' });
      return;
    }
    const toProcess = files.slice(0, room);
    if (files.length > toProcess.length) {
      addToast({ title: 'Too many attachments', detail: `Max ${MAX_ATTACHMENTS} per message`, type: 'attention' });
    }

    const results = [];
    for (const file of toProcess) {
      try {
        results.push(await processFile(file));
      } catch (e) {
        addToast({ title: 'Attachment error', detail: e.message, type: 'error' });
      }
    }
    if (results.length > 0) setAttachments((prev) => [...prev, ...results]);
  }, [attachments.length]);

  const removeAttachment = useCallback((idx) => {
    setAttachments((prev) => prev.filter((_, i) => i !== idx));
  }, []);

  const handleAttachClick = () => {
    attachInputRef.current?.click();
  };

  const handleAttachChange = (e) => {
    addFiles(e.target.files);
    e.target.value = ''; // allow re-selecting the same file
  };

  const handlePaste = (e) => {
    const files = Array.from(e.clipboardData?.files || []).filter((f) => f.type.startsWith('image/'));
    if (files.length === 0) return;
    e.preventDefault();
    addFiles(files);
  };

  const acceptSuggestion = useCallback((cmd) => {
    const el = textareaRef.current;
    if (!el) return;
    if (cmd.__flag) {
      const val = el.value;
      const cursor = el.selectionStart;
      let tokenStart = cursor;
      while (tokenStart > 0 && val[tokenStart - 1] !== ' ') tokenStart--;
      const before = val.slice(0, tokenStart);
      const after = val.slice(cursor);
      el.value = before + cmd.name + ' ' + after;
      const newPos = before.length + cmd.name.length + 1;
      el.selectionStart = el.selectionEnd = newPos;
      el.focus();
      el.dispatchEvent(new Event('input', { bubbles: true }));
      setCmdSuggestions(null);
      return;
    }
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
    const atts = attachments;
    if (!text && atts.length === 0) return;

    // Commands and shell escapes are text-only; attaching files to them
    // doesn't make sense (the server would reject/ignore them anyway).
    if ((text.startsWith('/') || text.startsWith('!')) && atts.length > 0) {
      addToast({ title: 'Cannot attach files here', detail: 'Remove the attachments first, or send them in a separate message', type: 'attention' });
      return;
    }

    if (text) pushHistory(text);
    el.value = '';
    saveDraft(sessionId, ''); // sent — drop the persisted draft
    setHasText(false);
    setCmdSuggestions(null);
    setFileSuggestions(null);
    setAttachments([]);
    autoResize();

    // Subagent mode: steer the subagent instead of the main agent. Slash
    // commands / shell escapes / attachments don't apply here — send raw text.
    if (subagentMode) {
      if (!text) return;
      setSubagentPending((prev) => [...prev, text]);
      try {
        const res = await steerSubagent(sessionId, steerJobId, text);
        if (res && res.queued === false) {
          addToast({ title: 'Message not delivered', detail: 'The subagent is not accepting messages right now (still starting or already finished).', type: 'attention' });
        }
      } catch (e) {
        setSubagentPending((prev) => {
          const idx = prev.lastIndexOf(text);
          return idx === -1 ? prev : prev.filter((_, i) => i !== idx);
        });
        addToast({ title: 'Message not sent', detail: String(e.message || e), type: 'error' });
      }
      return;
    }

    // Detect slash commands.
    if (text.startsWith('/')) {
      // Mobile keyboards autocorrect a typed "--" into an em/en-dash ("—"/"–"),
      // which breaks flag parsing (/goal … --max 3). Normalize a dash that
      // starts a token (preceded by whitespace, followed by a letter) back into
      // "--". A real em-dash inside prose (word—word) is left untouched.
      const normalized = text.replace(/(^|\s)[\u2013\u2014](?=[A-Za-z])/g, '$1--');

      // While the session is occupied (running / permission) OR the queue rail
      // is non-empty, a command is classified by policy (mirrors the server's
      // requireIdle + ClassifyCommand gate): reject commands that can't run
      // mid-run, and enqueue "queue" commands as a barrier with an optimistic
      // command chip so they run in strict send order at the next idle point.
      // An idle session with an empty queue runs everything immediately.
      const sessNow = store.get().sessions[sessionId];
      const queueNonEmpty = !!sessNow?.pendingSteers?.length;
      const occupied = sessionState === 'running' || sessionState === 'permission';
      let optimisticCmd = null;
      let cmdId = '';
      if (occupied || queueNonEmpty) {
        const policy = classifyCommand(normalized);
        if (policy === POLICY_REJECT) {
          addToast({ title: 'Cannot run this now', detail: `${normalized.split(/\s+/)[0]} can't run while the agent is working — stop it first.`, type: 'attention' });
          return;
        }
        if (policy === POLICY_QUEUE) {
          // Optimistic command chip: minted client-side so it has an
          // authoritative identity before the POST returns (the server echoes
          // the same ID on command_queued). Reconciled by ID like a steer chip.
          cmdId = newSteerId();
          optimisticCmd = { id: cmdId, text: normalized, command: true };
          const steers = sessNow?.pendingSteers || [];
          updateSession(sessionId, { pendingSteers: [...steers, optimisticCmd] });
        }
      }

      try {
        const result = await execCommand(sessionId, normalized, cmdId);
        if (optimisticCmd) {
          if (result && result.queued) {
            // Confirm the chip if it's still there (a concurrent
            // command_dequeued may already have removed it); never resurrect.
            const cur = store.get().sessions[sessionId];
            const list = cur?.pendingSteers;
            if (list && list.some((s) => s.id === cmdId)) {
              updateSession(sessionId, {
                pendingSteers: list.map((s) => (s.id === cmdId ? { ...s, confirmed: true } : s)),
              });
            }
            return; // queued — no immediate outcome to surface
          }
          // The run ended before the POST landed: the server found the session
          // idle and ran the command immediately (queued:false), so no
          // command_dequeued will retire the optimistic chip. Remove it now and
          // fall through to surface the immediate outcome (verify/rename/error).
          const cur = store.get().sessions[sessionId];
          if (cur?.pendingSteers) {
            const kept = cur.pendingSteers.filter((s) => s !== optimisticCmd);
            updateSession(sessionId, { pendingSteers: kept.length > 0 ? kept : null });
          }
        } else if (result && result.queued) {
          return; // enqueued server-side without an optimistic chip (e.g. idle→queue race)
        }
        if (text.startsWith('/verify') && result) {
          // Verify ran — surface the pass/fail outcome (the spinner is driven
          // by the AutoVerify WS events).
          addToast({
            title: result.ok ? 'Verify passed' : 'Verify failed',
            detail: result.message,
            type: result.ok ? 'done' : 'attention',
          });
        } else if (text.startsWith('/rename') && result && result.ok) {
          // Reflect the new title immediately; the poll would otherwise lag
          // (up to 15s on mobile). The server has already persisted it.
          const title = result.message.replace(/^renamed to:\s*/, '');
          updateSession(sessionId, { title });
        } else if (result && !result.ok) {
          addToast({ title: 'Command failed', detail: result.message, type: 'error' });
        }
      } catch (e) {
        // Roll back the optimistic command chip: a rejected enqueue (e.g. 503
        // queue full, or a network error) must not leave a phantom chip.
        if (optimisticCmd) {
          const cur = store.get().sessions[sessionId];
          if (cur?.pendingSteers) {
            const kept = cur.pendingSteers.filter((s) => s !== optimisticCmd);
            updateSession(sessionId, { pendingSteers: kept.length > 0 ? kept : null });
          }
        }
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
      await sendMessage(sessionId, text, atts);
    } catch (e) {
      console.error('Send failed:', e);
      // The optimistic echo was already rolled back in sendMessage; surface
      // the reason (e.g. a 400 for a rejected attachment) so it's not silent.
      addToast({ title: 'Message not sent', detail: String(e.message || e), type: 'error' });
    }
  };

  const handleSend = () => handleSendInner(textareaRef.current);
  // Keep a ref to the latest handleSend so the push-to-talk pointer handlers
  // (memoized useCallbacks) always invoke the current version, not a stale
  // closure missing the latest attachments/subagent state.
  const handleSendRef = useRef(handleSend);
  handleSendRef.current = handleSend;

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
    saveDraft(sessionId, e.target.value);
    setHasText(!!e.target.value.trim());
    // File suggestions with debounce.
    clearTimeout(fileDebounceRef.current);
    fileDebounceRef.current = setTimeout(updateFileSuggestions, 100);
  };

  const handleStop = async () => {
    if (!sessionId) return;
    // On abort the agent discards its queued steers/commands (they belonged to
    // the now-dead run) without emitting an event, so the user's intent would be
    // lost. Preserve it: move the locally-tracked queued chips back into the
    // input before cancelling, and clear them (mirrors the TUI's abort-dumps-
    // queue-to-input). Images can't be restored (base64 not tracked) — warn.
    const sess = store.get().sessions[sessionId];
    const steers = sess?.pendingSteers;
    if (steers && steers.length > 0) {
      const el = textareaRef.current;
      if (el) {
        const combined = steers.map((s) => s.text).join('\n');
        el.value = el.value ? el.value + '\n' + combined : combined;
        setHasText(!!el.value.trim());
        autoResize();
        el.focus();
        el.selectionStart = el.selectionEnd = el.value.length;
      }
      const droppedImages = steers.reduce((n, s) => n + (s.images || 0), 0);
      if (droppedImages > 0) {
        addToast({ sessionId, title: 'Queued images dropped', detail: `${droppedImages} attached image${droppedImages > 1 ? 's were' : ' was'} not restored — re-attach if still needed.`, type: 'attention' });
      }
      updateSession(sessionId, { pendingSteers: null });
    }
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

  // Derive activity phase + label from session state. Three coarse phases
  // (thinking / working / waiting) plus compacting/auto-verify specials — never
  // the specific tool, which is already visible in the chat. The "working"
  // phase rotates playful gerunds by elapsed time.
  const phase = activityPhase(session);
  const activityActive = phase !== null;
  const runStartedAtMs = session?.runStartedAtMs || 0;

  // Tick once a second while a run is in flight so both the elapsed counter and
  // the rotating gerund advance on their own.
  const [activityTick, setActivityTick] = useState(() => Date.now());
  useEffect(() => {
    if (!activityActive) return;
    setActivityTick(Date.now());
    const t = setInterval(() => setActivityTick(Date.now()), 1000);
    return () => clearInterval(t);
  }, [activityActive]);

  const elapsedMs = runStartedAtMs ? Math.max(0, activityTick - runStartedAtMs) : 0;
  const activityLabel = buildActivityLabel(phase, elapsedMs);
  // Show the timer only for the running phases, not for the momentary
  // compacting/verifying/waiting states where an age counter reads oddly.
  const showTimer = runStartedAtMs > 0 && (phase === 'thinking' || phase === 'working');
  const elapsedText = showTimer ? formatElapsed(elapsedMs) : '';

  const permissionMode = session?.permissionMode || 'yolo';

  // Cache-expiry warning: the prompt cache goes cold `cacheExpiresAt` ms after
  // the last run. We tick a clock while idle so the warning appears on its own
  // once the cache has expired (writing then pays a fresh cache-write). Only
  // relevant when the backend reported an expiry (Anthropic models).
  const cacheExpiresAt = session?.cacheExpiresAt || 0;
  const [nowTick, setNowTick] = useState(() => Date.now());
  useEffect(() => {
    if (!cacheExpiresAt || busy) return;
    setNowTick(Date.now());
    const t = setInterval(() => setNowTick(Date.now()), 15000);
    return () => clearInterval(t);
  }, [cacheExpiresAt, busy]);
  const cacheExpired = cacheExpiresAt > 0 && !busy && nowTick >= cacheExpiresAt;

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
                  onClick={handleMicToggle}
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
          {cacheExpired && !subagentMode && (
            <div class="input-cache-warn" title="El caché de prompt de esta conversación ha caducado. El siguiente mensaje se cobra como escritura de caché (más caro).">
              <span class="input-cache-dot" />
              Caché caducada · el próximo mensaje paga escritura
            </div>
          )}
          {activityActive && activityLabel && (
            <div class={`input-activity phase-${phase}`}>
              <span class="input-activity-dots" aria-hidden="true"><i></i><i></i><i></i></span>
              <span class="input-activity-label">{activityLabel}</span>
              {elapsedText && <span class="input-activity-timer">· {elapsedText}</span>}
              {busy && (
                <button class="input-activity-abort" onClick={handleStop} title="Stop (Esc)">
                  Esc to abort
                </button>
              )}
            </div>
          )}
          {!subagentMode && pendingSteers && pendingSteers.length > 0 && (() => {
            const last = pendingSteers[pendingSteers.length - 1];
            const extra = pendingSteers.length - 1;
            const badge = last.command ? 'command · queued' : 'queued · click to edit';
            return (
              <button class="input-steers" onClick={handleDequeueSteers} title="Click or Alt+↑ to edit queued messages">
                {last.command && <span class="input-steer-cmd" aria-hidden="true">/</span>}
                {!last.command && last.images > 0 && <span class="input-steer-img" aria-hidden="true">🖼</span>}
                <span class="input-steer-text">
                  {last.command ? last.text.replace(/^\//, '') : last.text}
                  {extra > 0 && <span class="input-steer-count"> +{extra}</span>}
                </span>
                <span class="input-steer-badge">{badge}</span>
              </button>
            );
          })()}
          {subagentMode && subagentPending.length > 0 && (
            <div class="input-steers-list">
              {subagentPending.map((msg, i) => (
                <button
                  key={i}
                  class="input-steers"
                  onClick={() => setSubagentPending((prev) => prev.filter((_, j) => j !== i))}
                  title="Sent to subagent · click to dismiss"
                >
                  <span class="input-steer-text">{msg}</span>
                  <span class="input-steer-badge">sent to subagent</span>
                </button>
              ))}
            </div>
          )}
          {attachments.length > 0 && (
            <div class="attachment-preview-strip">
              {attachments.map((a, i) => (
                <div class="attachment-chip" key={i}>
                  {a.isImage
                    ? <img src={`data:${a.mime};base64,${a.data}`} alt={a.name} />
                    : <span class="attachment-chip-name">📎 {a.name} <span class="attachment-chip-size">({Math.max(1, Math.round(a.size / 1024))} kB)</span></span>
                  }
                  <button class="attachment-chip-remove" onClick={() => removeAttachment(i)} title="Remove">
                    <X />
                  </button>
                </div>
              ))}
            </div>
          )}
          <input
            ref={attachInputRef}
            type="file"
            multiple
            hidden
            accept="image/*,application/pdf,.pdf,.txt,.md,.csv,.json,.log,.yaml,.yml,.xml,.html,.css,.js,.ts,.jsx,.tsx,.go,.py,.sh,.sql,.toml,.xlsx,.xls,.docx,.doc,.pptx,.ppt,.zip,.tar,.gz"
            onChange={handleAttachChange}
          />
          <button
            class="input-attach"
            onClick={handleAttachClick}
            title="Attach files"
          >
            <Paperclip />
          </button>
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
                    <span class="cmd-suggestion-name">{cmd.__flag ? cmd.name : '/' + cmd.name}</span>
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
              placeholder={subagentMode ? 'Message subagent…' : busy ? 'Steer the agent…' : 'Send a message…'}
              rows="1"
              onInput={handleInput}
              onKeyDown={handleKey}
              onPaste={handlePaste}
            />
          </div>
          {(() => {
            // The main button is send + push-to-talk. Tap = send; hold =
            // record; slide up while holding = lock. When there's text to send
            // (or voice isn't available), it's a plain send button. When idle
            // and empty, it shows a mic and records on hold.
            const canVoice = canTranscribe && voiceSupported;
            const micMode = canVoice && !hasText && !busy;
            const gesture = canVoice; // attach pointer gesture when voice is usable

            let icon = <SendHorizonal />;
            if (transcribing) icon = <Loader2 />;
            else if (recording) icon = voiceLocked ? <Square /> : <Mic />;
            else if (micMode) icon = <Mic />;

            const cls = [
              'input-send',
              busy ? 'steer' : '',
              gesture ? 'gesture' : '',
              recording ? 'recording' : '',
              voiceLocked ? 'locked' : '',
              transcribing ? 'transcribing' : '',
              micMode ? 'mic-mode' : '',
            ].filter(Boolean).join(' ');

            const title = transcribing ? 'Transcribing…'
              : recording ? (voiceLocked ? 'Tap to stop & transcribe' : 'Release to transcribe · slide up to lock')
              : micMode ? `Hold to talk · tap to send (${formatShortcut('.', { mod: true })} for mic)`
              : busy ? 'Steer' : 'Send';

            const gestureProps = gesture ? {
              onPointerDown: (e) => {
                // Suppress WebKit's long-press callout/selection before it can
                // start (it later fires pointercancel and kills recording).
                if (e.pointerType === 'touch' && e.cancelable) e.preventDefault();
                clearClickSuppressTimer();
                pointerDrivenRef.current = true;
                onSendPointerDown(e);
              },
              onPointerMove: onSendPointerMove,
              onPointerUp: onSendPointerUp,
              onPointerCancel: onSendPointerCancel,
              onContextMenu: (e) => e.preventDefault(),
              // Keyboard activation (Enter/Space) fires click with no preceding
              // pointer sequence — send in that case. Mouse taps also fire click
              // after pointerup, which already handled them; the ref suppresses
              // that duplicate.
              onClick: () => {
                if (pointerDrivenRef.current) { pointerDrivenRef.current = false; clearClickSuppressTimer(); return; }
                // Keyboard activation (no pointer sequence): stop if recording,
                // otherwise send. Use the ref (not the `recording` state, which
                // may have already re-rendered to false) and never send while a
                // transcription is still in flight — that click must not submit
                // the stale textarea contents (bug #1).
                if (recordingRef.current) { setVoiceLocked(false); stopVoice(); return; }
                if (transcribing) return;
                handleSendRef.current();
              },
            } : {
              onClick: handleSend,
            };

            return (
              <div class="input-send-wrap">
                {recording && !voiceLocked && (
                  <div class="voice-lock-hint">
                    <ChevronUp />
                    <span>Slide up to lock</span>
                  </div>
                )}
                <button
                  class={cls}
                  disabled={!sessionId || transcribing}
                  title={title}
                  {...gestureProps}
                >
                  {icon}
                </button>
              </div>
            );
          })()}
        </>
      )}
    </div>
  );
}
