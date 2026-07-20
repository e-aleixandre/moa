import { useRef, useCallback, useEffect, useState } from "preact/hooks";
import { Plus, Slash, ArrowUp, Square, X, Mic, Loader2, ChevronUp } from "lucide-preact";
import { Chip } from "../../primitives/index.js";
import { FileSuggestions } from "../../components/FileSuggestions/FileSuggestions.jsx";
import { useVoiceGesture } from "../../hooks/useVoiceGesture.js";
import {
  sendMessage, cancelRun, cancelSteers, execCommand, execShell, newSteerId,
  steerSubagent,
} from "../../data/session-actions.js";
import { store, updateSession } from "../../data/store.js";
import { addToast } from "../../data/notifications.js";
import { combineQueueText, droppedImageCount, queueSummary } from "../../data/composer-queue.js";
import {
  slashSuggestions, findMentionToken, computeMentionInsertion, normalizeDashes,
} from "../../data/composer-suggest.js";
import { classifyCommand, POLICY_QUEUE, POLICY_REJECT } from "../../data/util/command-policy.js";
import { processFile } from "../../data/util/attachments.js";
import { formatShortcut } from "../../data/util/shortcut.js";
import "./Composer.css";

// Composer — the conversation input: send a message, steer/queue while the
// agent works, recall the queue to edit, and stop the run. This is a STATEFUL
// container (unlike the presentational Spine/ChatHead/etc.) because it owns a
// lot of DOM-bound state — the textarea ref, per-session drafts, and input
// history — that can't be lifted without churn. It receives `sessionId` and
// `session` by props (the ConversationScreen container reads them from the
// store), exactly like the old SPA's InputBar. The queue/recall/abort/history/
// draft logic (5D) plus slash commands/@-mentions/attachments/shell escapes/
// cache warning (5E) are ported faithfully from pkg/serve/frontend/src/
// components/InputBar.jsx, recording the pieces that belong to later
// subphases as stubs.
//
// Deferred to later subphases (NOT wired here):
//   5F — permission / ask_user prompts.
//
// 5J — subagent steering: when `steer` is set ({ jobId, accent, name }) the
// composer becomes a STEER box for a live subagent. The STEER tag + accented
// focus border make it unmistakable who you're writing to; Enter routes the
// text through steerSubagent(sessionId, jobId, text) instead of sendMessage,
// and there is no queue/slash/shell semantics (those belong to the parent run).

const MAX_ATTACHMENTS = 8;

// Same broad allow-list as the old SPA's attach <input accept="…">: images
// plus the file kinds the agent can read off disk (server saves non-image/PDF
// attachments to disk; images/PDFs go to the model natively).
const ATTACH_ACCEPT = 'image/*,application/pdf,.pdf,.txt,.md,.csv,.json,.log,.yaml,.yml,.xml,.html,.css,.js,.ts,.jsx,.tsx,.go,.py,.sh,.sql,.toml,.xlsx,.xls,.docx,.doc,.pptx,.ppt,.zip,.tar,.gz';

// Per-session input history (survives re-renders, not a page reload). Ported
// from InputBar: getHistory/pushHistory + cursorRow drive the ↑/↓ recall.
const sessionHistories = new Map();
function getHistory(id) {
  if (!sessionHistories.has(id)) sessionHistories.set(id, { entries: [], idx: -1, draft: "" });
  return sessionHistories.get(id);
}
const MAX_HISTORY = 100;

// Per-session unsent draft, persisted to localStorage so a reload (iOS evicts
// backgrounded PWAs freely) doesn't lose what you were typing. The prefix is
// deliberately DISTINCT from the old SPA's `moa-draft-` so the two frontends
// don't clobber each other's drafts while they coexist under /next.
const DRAFT_PREFIX = "moa-next-draft-";
function loadDraft(id) {
  if (!id) return "";
  try { return localStorage.getItem(DRAFT_PREFIX + id) || ""; } catch (_) { return ""; }
}
function saveDraft(id, text) {
  if (!id) return;
  try {
    if (text) localStorage.setItem(DRAFT_PREFIX + id, text);
    else localStorage.removeItem(DRAFT_PREFIX + id);
  } catch (_) { /* ignore */ }
}

export function Composer({ sessionId, session, shortPlaceholder = false, steer = null }) {
  const textareaRef = useRef(null);
  const attachInputRef = useRef(null);
  const sessionState = session?.state;
  const pendingSteers = session?.pendingSteers;
  // In 5J steer mode the box targets a subagent, not the parent run — so it
  // must never enter the parent's "busy" affordances (Stop button, Esc-aborts,
  // queue note). It always shows a Send button that fires a steer.
  const busy = sessionState === "running" && !steer;
  const [hasText, setHasText] = useState(false);
  // Guards a recall (chip click / Alt+↑) against double-activation before the
  // WS steers_canceled round-trip clears the chips: without it, a second click
  // (or click + Alt+↑) would see the same pendingSteers and combine the texts
  // twice into the textarea. Released once cancelSteers settles.
  const recallInFlight = useRef(false);

  // --- Slash command + @-mention suggestion state ---
  const [goalFlags, setGoalFlags] = useState([]);
  const [canTranscribe, setCanTranscribe] = useState(false);
  const [cmdSuggestions, setCmdSuggestions] = useState(null); // null = hidden
  const [cmdCursor, setCmdCursor] = useState(0);
  const [fileSuggestions, setFileSuggestions] = useState(null); // [{path, is_dir}] or null
  const [fileCursor, setFileCursor] = useState(0);
  const fileAbortRef = useRef(null);
  const fileDebounceRef = useRef(null);

  // --- Attachments ---
  const [attachments, setAttachments] = useState([]);
  // Slots reserved by in-flight addFiles calls, so two concurrent loads (e.g.
  // paste + picker) can't each independently reserve up to MAX and overshoot.
  const attachInFlightRef = useRef(0);

  // Fetch /goal flag metadata + transcription capability once on mount (mirrors
  // InputBar's capabilities check). `transcribe` drives whether the send button
  // doubles as a push-to-talk mic (5M).
  useEffect(() => {
    fetch('/api/capabilities', { headers: { 'X-Moa-Request': '1' } })
      .then(r => r.json())
      .then(caps => {
        setGoalFlags(Array.isArray(caps.goal_flags) ? caps.goal_flags : []);
        setCanTranscribe(!!caps.transcribe);
      })
      .catch(() => {});
  }, []);

  const autoResize = useCallback(() => {
    const el = textareaRef.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = Math.min(el.scrollHeight, 160) + "px";
  }, []);

  // Restore the persisted draft on mount. The composer is keyed by session in
  // the container, so a session switch remounts this component (tearing down
  // in-flight file requests / attachment processing and clearing state) rather
  // than mutating sessionId in place.
  useEffect(() => {
    const el = textareaRef.current;
    if (!el) return;
    el.value = loadDraft(sessionId);
    setHasText(!!el.value.trim());
    setCmdSuggestions(null);
    setFileSuggestions(null);
    setAttachments([]);
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
    h.draft = "";
  }, [sessionId]);

  // --- Dequeue steers (recall to input for editing) ---
  // Ported from InputBar.handleDequeueSteers: pull every queued chip's text
  // into the textarea, warn about queued images that can't be restored, and
  // cancel the not-yet-delivered steers server-side so re-submitting the edited
  // text doesn't deliver both the originals and the edit. The server broadcasts
  // steers_canceled to every client (shared queue), which clears the chips.
  const handleDequeueSteers = useCallback(() => {
    if (recallInFlight.current) return; // a recall is already in flight
    const sess = store.get().sessions[sessionId];
    if (!sess?.pendingSteers?.length) return;

    const el = textareaRef.current;
    if (!el) return;

    recallInFlight.current = true;
    el.value = combineQueueText(el.value, sess.pendingSteers);
    setHasText(!!el.value.trim());
    saveDraft(sessionId, el.value); // persist the recalled text (no input event)

    const dropped = droppedImageCount(sess.pendingSteers);
    if (dropped > 0) {
      addToast({ sessionId, title: "Queued images dropped", detail: `${dropped} attached image${dropped > 1 ? "s were" : " was"} not restored — re-attach if still needed.`, type: "attention" });
    }

    cancelSteers(sessionId)
      .catch((e) => {
        console.error("cancelSteers failed:", e);
        addToast({ sessionId, title: "Could not cancel queued messages", detail: e.message, type: "error" });
      })
      .finally(() => { recallInFlight.current = false; });

    autoResize();
    el.focus();
    el.selectionStart = el.selectionEnd = el.value.length;
  }, [sessionId, autoResize]);

  // --- Slash command suggestions ---
  // Recomputes the popup from the textarea's current value/cursor. Ported from
  // InputBar.updateSuggestions, with the filtering/matching logic factored out
  // to data/composer-suggest.js (slashSuggestions).
  const updateSuggestions = useCallback(() => {
    const el = textareaRef.current;
    if (!el) return;
    setCmdSuggestions(slashSuggestions(el.value, el.selectionStart, goalFlags));
    setCmdCursor(0);
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
    const mention = findMentionToken(el.value, el.selectionStart);
    if (!mention) {
      cancelFileRequest();
      setFileSuggestions(null);
      return;
    }

    // Abort previous request.
    cancelFileRequest();
    const controller = new AbortController();
    fileAbortRef.current = controller;

    fetch(`/api/sessions/${sessionId}/files?q=${encodeURIComponent(mention.filter)}&limit=50`, {
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
    const { value, cursor, retrigger } = computeMentionInsertion(el.value, el.selectionStart, path, isDir);
    el.value = value;
    el.selectionStart = el.selectionEnd = cursor;
    setFileSuggestions(null);
    if (retrigger) setTimeout(updateFileSuggestions, 50); // navigate into directory
    el.dispatchEvent(new Event('input', { bubbles: true }));
    el.focus();
  }, [updateFileSuggestions]);

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
      // Mirror the flag branch: fire input so 5D's draft/hasText/autoResize
      // stay in sync (a reload before the next keystroke would otherwise lose
      // the just-picked command).
      el.dispatchEvent(new Event('input', { bubbles: true }));
    } else {
      el.value = '/' + cmd.name;
      setCmdSuggestions(null);
      handleSendInnerRef.current(el);
    }
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  // --- Attachments ---
  const addFiles = useCallback(async (fileList) => {
    const files = Array.from(fileList || []);
    if (files.length === 0) return;

    // Reserve slots atomically against both the committed attachments and any
    // still-processing ones, so concurrent addFiles calls can't jointly exceed
    // MAX_ATTACHMENTS.
    const room = MAX_ATTACHMENTS - attachments.length - attachInFlightRef.current;
    if (room <= 0) {
      addToast({ title: 'Too many attachments', detail: `Max ${MAX_ATTACHMENTS} per message`, type: 'attention' });
      return;
    }
    const toProcess = files.slice(0, room);
    if (files.length > toProcess.length) {
      addToast({ title: 'Too many attachments', detail: `Max ${MAX_ATTACHMENTS} per message`, type: 'attention' });
    }

    attachInFlightRef.current += toProcess.length;
    const results = [];
    try {
      for (const file of toProcess) {
        try {
          results.push(await processFile(file));
        } catch (e) {
          addToast({ title: 'Attachment error', detail: e.message, type: 'error' });
        }
      }
    } finally {
      attachInFlightRef.current -= toProcess.length;
    }
    if (results.length > 0) setAttachments((prev) => [...prev, ...results]);
  }, [attachments.length]);

  const removeAttachment = useCallback((idx) => {
    setAttachments((prev) => prev.filter((_, i) => i !== idx));
  }, []);

  const handleAttachClick = useCallback(() => {
    attachInputRef.current?.click();
  }, []);

  const handleAttachChange = useCallback((e) => {
    addFiles(e.target.files);
    e.target.value = ''; // allow re-selecting the same file
  }, [addFiles]);

  const handlePaste = useCallback((e) => {
    const files = Array.from(e.clipboardData?.files || []).filter((f) => f.type.startsWith('image/'));
    if (files.length === 0) return;
    e.preventDefault();
    addFiles(files);
  }, [addFiles]);

  // The composer bar has no dedicated "/" affordance in the old SPA (the popup
  // only appears once you type "/" yourself). We give the button a concrete
  // job instead of leaving it a no-op: insert "/" at the cursor and focus, which
  // opens the same popup as typing it manually.
  const handleSlashButton = useCallback(() => {
    const el = textareaRef.current;
    if (!el) return;
    if (el.value === '') {
      el.value = '/';
      el.selectionStart = el.selectionEnd = 1;
      el.dispatchEvent(new Event('input', { bubbles: true }));
    }
    el.focus();
  }, []);

  // --- Send / enqueue ---
  // Ported from InputBar.handleSendInner. Slash commands and shell escapes are
  // text-only (attaching files to them doesn't make sense — the server would
  // reject/ignore them anyway); everything else goes through sendMessage, which
  // is the single source of truth for the send-vs-enqueue decision (an idle/
  // errored session runs the message and starts a run; a busy session mints a
  // steer chip, optimistic and reconciled by id).
  const handleSendInner = useCallback(async (el) => {
    if (!el || !sessionId) return;
    const text = el.value.trim();
    const atts = attachments;
    if (!text && atts.length === 0) return;

    // 5J steer mode: everything the user types goes to the live subagent as a
    // steer. No slash/shell/queue semantics, no attachments (the subagent steer
    // endpoint is text-only). Clear the box and fire; the caller shows optimistic
    // feedback / rebounds to the parent if the subagent already finished.
    if (steer && steer.jobId) {
      if (!text) return;
      pushHistory(text);
      el.value = '';
      saveDraft(sessionId, '');
      setHasText(false);
      setCmdSuggestions(null);
      setFileSuggestions(null);
      setAttachments([]);
      autoResize();
      try {
        await steerSubagent(sessionId, steer.jobId, text);
      } catch (e) {
        console.error('Steer failed:', e);
        if (steer.onRebound) steer.onRebound();
        addToast({ sessionId, title: 'Steer not delivered', detail: String(e.message || e), type: 'error' });
      }
      return;
    }

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

    // Detect slash commands.
    if (text.startsWith('/')) {
      // Mobile keyboards autocorrect a typed "--" into an em/en-dash ("—"/"–"),
      // which breaks flag parsing (/goal … --max 3). Normalize a dash that
      // starts a token back into "--"; a real em-dash inside prose is left
      // untouched.
      const normalized = normalizeDashes(text);

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
        await execShell(sessionId, command, silent);
      } catch (e) {
        addToast({ title: 'Shell error', detail: e.message, type: 'error' });
      }
      return;
    }

    try {
      await sendMessage(sessionId, text, atts);
    } catch (e) {
      console.error('Send failed:', e);
      // sendMessage already rolled back the optimistic echo/chip; surface the
      // reason (e.g. a 400) so it's not silent.
      addToast({ sessionId, title: 'Message not sent', detail: String(e.message || e), type: 'error' });
    }
  }, [sessionId, sessionState, attachments, pushHistory, autoResize, steer]);

  const handleSend = useCallback(() => handleSendInner(textareaRef.current), [handleSendInner]);
  // acceptSuggestion (below) is defined before handleSendInner and has an
  // intentionally-empty dep array (it's only invoked from event handlers, well
  // after mount) — this ref keeps it calling the latest handleSendInner
  // (fresh sessionId/attachments) instead of one closed over at first render.
  const handleSendInnerRef = useRef(handleSendInner);
  handleSendInnerRef.current = handleSendInner;

  // --- Voice / push-to-talk (5M) ---
  // insertAtCursor drops transcribed text at the textarea caret (with a space
  // separator when needed), then fires input so drafts/hasText/autoResize/
  // suggestions stay in sync. Ported from InputBar.insertAtCursor.
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

  const onVoiceError = useCallback((msg) => {
    addToast({ title: 'Voice input', detail: msg, type: 'error' });
  }, []);

  const {
    handlers: voiceHandlers, recording, transcribing, locked: voiceLocked,
    showSlideHint, supported: voiceSupported, toggleFromShortcut,
  } = useVoiceGesture({ onTranscript: insertAtCursor, onError: onVoiceError, onSend: handleSend });

  // Voice is usable only when the backend can transcribe AND the browser has a
  // MediaRecorder + mic (needs a secure context). Steer mode never records — it
  // targets a subagent, not the parent run.
  const canVoice = canTranscribe && voiceSupported && !steer;

  // ⌘. (Mac) / Alt+. (elsewhere) toggles push-to-talk for the FOCUSED composer.
  // Ctrl is deliberately excluded (project rule: ⌘ on Mac / Alt elsewhere,
  // never Ctrl). Gated to this composer having focus so multi-pane layouts only
  // toggle the one you're typing in.
  useEffect(() => {
    if (!canVoice) return;
    const onKey = (e) => {
      if (!((e.metaKey || e.altKey) && !e.ctrlKey && e.key === '.')) return;
      const el = textareaRef.current;
      if (!el || document.activeElement !== el) return;
      e.preventDefault();
      toggleFromShortcut();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [canVoice, toggleFromShortcut]);

  // --- Stop / abort ---
  // Ported from InputBar.handleStop: on abort the agent discards its queued
  // steers (they belonged to the now-dead run) without emitting an event, so
  // the user's intent would be lost. Preserve it — dump the locally-tracked
  // queued chips back into the input before cancelling, clear them, and warn
  // about queued images that can't be restored (mirror of the TUI's
  // abort-dumps-queue-to-input).
  const handleStop = useCallback(async () => {
    if (!sessionId) return;
    const sess = store.get().sessions[sessionId];
    const steers = sess?.pendingSteers;
    if (steers && steers.length > 0) {
      const el = textareaRef.current;
      if (el) {
        el.value = combineQueueText(el.value, steers);
        setHasText(!!el.value.trim());
        autoResize();
        el.focus();
        el.selectionStart = el.selectionEnd = el.value.length;
      }
      saveDraft(sessionId, el.value); // persist the dumped queue (no input event)
      const dropped = droppedImageCount(steers);
      if (dropped > 0) {
        addToast({ sessionId, title: "Queued images dropped", detail: `${dropped} attached image${dropped > 1 ? "s were" : " was"} not restored — re-attach if still needed.`, type: "attention" });
      }
      updateSession(sessionId, { pendingSteers: null });
    }
    try {
      await cancelRun(sessionId);
    } catch (e) {
      console.error("Cancel failed:", e);
    }
  }, [sessionId, autoResize]);

  // Returns the row the cursor is on (0-indexed) / total rows — ported from
  // InputBar to gate ↑/↓ history recall to the first/last line.
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

  const handleKey = useCallback((e) => {
    // Alt+ArrowUp: dequeue pending steers to input (parity with TUI).
    if (e.key === "ArrowUp" && e.altKey) {
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

    // Esc aborts the running agent.
    if (e.key === "Escape" && busy) {
      e.preventDefault();
      handleStop();
      return;
    }

    // Alt/⌥+Enter enqueues explicitly (parity with the placeholder hint);
    // sendMessage still routes to a steer whenever the session is busy, so this
    // is only meaningful for an idle session the user wants to queue against.
    if (e.key === "Enter" && (e.altKey || e.metaKey)) {
      e.preventDefault();
      handleSend();
      return;
    }

    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      handleSend();
      return;
    }

    if (!sessionId) return;
    const h = getHistory(sessionId);

    if (e.key === "ArrowUp" && cursorRow() === 0 && h.entries.length > 0) {
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
      saveDraft(sessionId, el.value); // keep the persisted draft in sync
      el.selectionStart = el.selectionEnd = el.value.length;
      updateSuggestions();
      return;
    }

    if (e.key === "ArrowDown" && h.idx !== -1 && cursorRow() === totalRows() - 1) {
      e.preventDefault();
      const el = textareaRef.current;
      h.idx++;
      if (h.idx >= h.entries.length) {
        h.idx = -1;
        el.value = h.draft;
        h.draft = "";
      } else {
        el.value = h.entries[h.idx];
      }
      autoResize();
      saveDraft(sessionId, el.value); // keep the persisted draft in sync
      el.selectionStart = el.selectionEnd = el.value.length;
      updateSuggestions();
      return;
    }
  }, [
    sessionId, busy, fileSuggestions, fileCursor, cmdSuggestions, cmdCursor,
    handleDequeueSteers, handleStop, handleSend, acceptFileMention, acceptSuggestion,
    cursorRow, totalRows, autoResize, updateSuggestions,
  ]);

  const handleInput = useCallback((e) => {
    autoResize();
    updateSuggestions();
    saveDraft(sessionId, e.target.value);
    setHasText(!!e.target.value.trim());
    // File suggestions with debounce.
    clearTimeout(fileDebounceRef.current);
    fileDebounceRef.current = setTimeout(updateFileSuggestions, 100);
  }, [sessionId, autoResize, updateSuggestions, updateFileSuggestions]);

  // Cache-expiry warning: the prompt cache goes cold `cacheExpiresAt` ms after
  // the last run. We tick a clock while idle so the warning appears on its own
  // once the cache has expired (writing then pays a fresh cache-write). Only
  // relevant when the backend reported an expiry (Anthropic models). Ported
  // from InputBar; the original SPA's copy is in Spanish — this one is in
  // English per the project's UI-text convention.
  const cacheExpiresAt = session?.cacheExpiresAt || 0;
  const [nowTick, setNowTick] = useState(() => Date.now());
  useEffect(() => {
    if (!cacheExpiresAt || busy) return;
    setNowTick(Date.now());
    const t = setInterval(() => setNowTick(Date.now()), 15000);
    return () => clearInterval(t);
  }, [cacheExpiresAt, busy]);
  const cacheExpired = cacheExpiresAt > 0 && !busy && nowTick >= cacheExpiresAt;

  const summary = steer ? null : queueSummary(pendingSteers);
  // shortPlaceholder (mobile): the multi-line keyboard hint doesn't fit the
  // single-line pill and reads noisy on a phone — use the short prompt.
  const idlePlaceholder = shortPlaceholder
    ? "Message moa…"
    : `Message moa — Enter to send, ⇧Enter for a new line, ${formatShortcut("Enter", { mod: true })} to queue…`;
  // 5J steer mode overrides everything: this box talks to the subagent.
  // Busy (parent run) placeholder: state that Enter STEERS without stopping —
  // the persistent Send button already signals "you can always talk to it", so
  // the copy names the consequence. Mobile's pill has no room for the long form.
  const busyPlaceholder = shortPlaceholder
    ? "Steer — it keeps working…"
    : "Steer the agent — ⏎ sends while it works, it won't stop it…";
  const placeholder = steer
    ? `Steer ${steer.name || "subagent"} — it reads this before its next step…`
    : (busy ? busyPlaceholder : idlePlaceholder);

  return (
    <div class={`composer-wrap${steer ? " composer-steer" : ""}`}>
      {steer && (
        <div class="composer-steer-tag" style={{ "--steer-accent": `var(--${steer.accent || "peach"})` }}>
          <span class="composer-steer-label">STEER</span>
          <span class="composer-steer-name" style={{ color: `var(--${steer.accent || "peach"})` }}>
            {steer.name || "subagent"}
          </span>
        </div>
      )}
      {cacheExpired && (
        <div class="cache-warn" title="The prompt cache for this conversation has expired. Your next message will pay for a fresh cache write (more expensive).">
          <span class="cache-warn-dot" />
          Prompt cache expired · your next message pays a cache write
        </div>
      )}
      {attachments.length > 0 && (
        <div class="attach-preview-strip">
          {attachments.map((a, i) => (
            <div class="attach-chip" key={i}>
              {a.isImage
                ? <img src={`data:${a.mime};base64,${a.data}`} alt={a.name} />
                : <span class="attach-chip-name">📎 {a.name} <span class="attach-chip-size">({Math.max(1, Math.round(a.size / 1024))} kB)</span></span>
              }
              <button type="button" class="attach-chip-remove" onClick={() => removeAttachment(i)} title="Remove">
                <X size={12} />
              </button>
            </div>
          ))}
        </div>
      )}
      <div class={`composer${busy ? " is-busy" : ""}`}>
        <input
          ref={attachInputRef}
          type="file"
          multiple
          hidden
          accept={ATTACH_ACCEPT}
          onChange={handleAttachChange}
        />
        {fileSuggestions && !cmdSuggestions && (
          <FileSuggestions
            items={fileSuggestions}
            cursor={fileCursor}
            onSelect={acceptFileMention}
            onHover={setFileCursor}
          />
        )}
        {cmdSuggestions && (
          <div class="cmd-suggestions">
            {cmdSuggestions.map((cmd, i) => (
              <div
                key={cmd.__flag ? cmd.name : '/' + cmd.name}
                class={`cmd-suggestion-item ${i === cmdCursor ? "selected" : ""}`}
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
        <textarea
          ref={textareaRef}
          rows={1}
          class="composer-textarea"
          aria-label="Message moa"
          placeholder={placeholder}
          onInput={handleInput}
          onKeyDown={handleKey}
          onPaste={handlePaste}
        />
        <div class="composer-bar">
          <button type="button" class="composer-btn composer-btn-attach" title="Attach files" aria-label="Attach" onClick={handleAttachClick}>
            <Plus size={15} />
          </button>
          <button type="button" class="composer-btn composer-btn-slash" title="Slash commands" aria-label="Slash commands" onClick={handleSlashButton}>
            <Slash size={14} />
          </button>
          {summary && (
            <button
              type="button"
              class="queue-note"
              title="Click or Alt+↑ to edit queued messages"
              onClick={handleDequeueSteers}
            >
              <Chip size="sm" mono>{summary.count} queued</Chip>
              <span>
                {summary.lastImages > 0 && <span aria-hidden="true">🖼 </span>}
                {summary.lastIsCommand && <span aria-hidden="true">/</span>}
                “{summary.lastText}”
              </span>
            </button>
          )}
          {busy && hasText && !summary && (
            <span class="steer-hint" aria-hidden="true">⏎ steers — won't interrupt</span>
          )}
          {busy && (
            /* Stop no longer REPLACES Send while busy — it joins it as a
               secondary (ghost) action. Send keeps its identity in every state
               so "you can always talk to it" (steer) reads at a glance; on
               mobile this ghost is also the only Stop (no Esc). */
            <button
              type="button"
              class="composer-stop-ghost"
              aria-label="Stop the run (Esc)"
              title="Stop — ends the run (Esc)"
              onClick={handleStop}
            >
              <Square size={13} />
            </button>
          )}
          {(() => {
            // The send button doubles as push-to-talk (5M): tap = send/steer,
            // hold = record, slide up while holding = lock hands-free. The
            // pointer GESTURE is attached whenever voice is usable — in every
            // state, including with text to send and while the agent is busy
            // (parity with the old InputBar, where gesture = canVoice). The
            // input being empty only decides the ICON (mic vs arrow): a hold
            // still records even when there's text to send, and the transcript
            // is inserted at the caret. Steer-to-subagent mode disables voice
            // (canVoice already excludes it) so it stays a plain send.
            const micMode = canVoice && !hasText;

            let icon = <ArrowUp size={16} />;
            if (transcribing) icon = <Loader2 size={16} class="spin" />;
            else if (recording && voiceLocked) icon = <Square size={14} />;
            else if (recording) icon = <Mic size={16} />;
            else if (micMode) icon = <Mic size={16} />;

            const cls = [
              "composer-send",
              canVoice ? "gesture" : "",
              recording ? "recording" : "",
              voiceLocked ? "locked" : "",
              transcribing ? "transcribing" : "",
              micMode ? "mic-mode" : "",
            ].filter(Boolean).join(" ");

            const sendTitle = busy ? "Send — steers the agent, doesn't stop it" : "Send";
            const title = transcribing ? "Transcribing…"
              : recording ? (voiceLocked ? "Tap to stop & transcribe" : "Release to transcribe · slide up to lock")
              : micMode ? `Hold to talk · tap to send (${formatShortcut(".", { mod: true })} for mic)`
              : sendTitle;

            // Attach the push-to-talk pointer gesture whenever voice is usable;
            // the reducer routes a quick tap to send and a hold to record, so a
            // plain send still works. Without voice it's a plain send button.
            const gestureProps = canVoice ? voiceHandlers : { onClick: handleSend };

            const sendLabel = micMode ? "Record" : busy ? "Send steer" : "Send";

            return (
              <div class="composer-send-wrap">
                {showSlideHint && (
                  <div class="voice-lock-hint">
                    <ChevronUp size={14} />
                    <span>Slide up to lock</span>
                  </div>
                )}
                <button
                  type="button"
                  class={cls}
                  aria-label={sendLabel}
                  title={title}
                  disabled={transcribing}
                  {...gestureProps}
                >
                  {icon}
                </button>
              </div>
            );
          })()}
        </div>
      </div>
    </div>
  );
}
