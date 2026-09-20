import { useRef, useCallback, useEffect, useState } from "preact/hooks";
import { Paperclip, X, Mic, Loader2, Image as ImageIcon, PhoneCall } from "lucide-preact";
import { Chip } from "../../primitives/index.js";
import { FileSuggestions } from "../../components/FileSuggestions/FileSuggestions.jsx";
import { ActionMenu } from "../../components/ActionMenu/ActionMenu.jsx";
import { useVoiceGesture } from "../../hooks/useVoiceGesture.js";
import { useVoiceLive } from "../../hooks/useVoiceLive.js";
import { appendCallResult } from "../../data/voice-live.js";
import { VoiceLivePanel } from "../../components/VoiceLivePanel/VoiceLivePanel.jsx";
import { useStore } from "../../hooks/useStore.js";
import {
  sendMessage, stopRun, cancelSteers, execCommand, execShell, newSteerId,
  steerSubagent,
} from "../../data/session-actions.js";
import { store, updateSession } from "../../data/store.js";
import { consumeComposerDrop } from "../../data/share.js";
import { appendSharedText } from "../../data/share-target.js";
import { addToast } from "../../data/notifications.js";
import { combineQueueText, droppedImageCount, queueSummary, recallActivates, sendMayClear } from "../../data/composer-queue.js";
import {
  slashSuggestions, findMentionToken, computeMentionInsertion, normalizeDashes,
} from "../../data/composer-suggest.js";
import { useSessionSkills } from '../../hooks/useSessionSkills.js';
import { interceptSecretCommand } from "../../data/secrets.js";
import { loadDraft, saveDraft } from "../../data/composer-draft.js";
import { classifyCommand, POLICY_QUEUE, POLICY_REJECT } from "../../data/util/command-policy.js";
import { processFile } from "../../data/util/attachments.js";
import { formatShortcut } from "../../data/util/shortcut.js";
import {
  SEND_BUTTON_INITIAL, sendButtonEvent, reduceContentSendActivation,
} from "../../data/composer-send-button.js";
import {
  compositionEnded, compositionInputDiscarded, compositionStarted,
  compositionSubmitted, newCompositionState, shouldDiscardLateCompositionInput,
  valueBeforeLateCompositionInput,
} from "../../data/composer-composition.js";
import "./Composer.css";

// Composer — the conversation input. Markup and CSS are the catalogue's
// (catalog/zones-lab.jsx `Composer`, zones-lab.css the `.zl-composer` /
// `.zl-dock` / `.zl-ta` / `.zl-attach` / `.zl-send` block), MOVED here rather
// than imitated: the classes travelled with the rules, so the slab IS the
// accepted design instead of a translation of it. The catalogue imports this
// component now, which is what makes one definition rather than two.
//
// What is NOT the catalogue's is everything the prototype never had, grafted
// on top: send / queue / slash / @-mention / attachments / dictation / the
// draft that survives a reload / the anti-double-send barrier.
//
// TWO ROWS. The text takes a row of its own and the controls live under it,
// always, in both densities. The pill used to have room for exactly one
// control at its end, so on the phone that button was the mic OR Send — and
// with tap-to-record, the moment there was a draft the mic was gone.
// Dictating, fixing a word and dictating some more is the natural way to use
// it, so the mic and Send each have a permanent seat now and a tap always
// means one thing. Send is never peach — peach is the message the text
// becomes after this button.
//
// Stop is NOT here. This slab holds what the owner is about to say; stopping
// the agent is a verb of the row above it, the LiveBar, which is the one that
// says the agent is working. Esc still stops from the keyboard (below). The
// two used to share this row and, with the mic recording, put two red squares
// side by side that meant opposite things (stop the AGENT / stop MY mic).
//
// Subagent steering: when `steer` is set ({ jobId, name, onRebound }) the
// composer becomes a STEER box for a live subagent. It stays visually IDENTICAL
// to the normal composer — the subagent view's header identifies who you're
// writing to, and its Stop lives there too. Enter routes the text through
// steerSubagent instead of sendMessage, and there is no queue/slash/shell
// semantics (those belong to the parent run).
//
// `plusActions` turns the `+` into a menu instead of a direct file picker: the
// surface that hosts the composer contributes the extra entries that belong
// next to Attach files there. Only the phone passes any (Live preview, which
// has no room of its own since the mobile header is gone); on the desktop the
// prop stays empty and `+` opens the picker with a single tap, as before.

function AttachIcon({ size = 18 }) {
  return (
    <svg viewBox="0 0 16 16" width={size} height={size} aria-hidden="true">
      <path d="M8 3.5v9M3.5 8h9" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" />
    </svg>
  );
}

function SendIcon() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true">
      <path d="M8 13V3.5M8 3.5L3.8 7.7M8 3.5l4.2 4.2" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" />
    </svg>
  );
}

const MAX_ATTACHMENTS = 8;

// No allow-list. The old one was a list of extensions, and it never defended
// anything: `accept` only filters what the file picker offers you, and the
// server validates size and count but no format at all -- anything that
// arrives is stored and the agent can read it. All it did was hide a .key, an
// .odp or whatever else the phone reported with a MIME type nobody enumerated,
// and send the owner to scp a file he could have attached.
const ATTACH_ACCEPT = '*/*';

// Per-session input history (survives re-renders, not a page reload). Ported
// from InputBar: getHistory/pushHistory + cursorRow drive the ↑/↓ recall.
const sessionHistories = new Map();
function getHistory(id) {
  if (!sessionHistories.has(id)) sessionHistories.set(id, { entries: [], idx: -1, draft: "" });
  return sessionHistories.get(id);
}
const MAX_HISTORY = 100;

export function keepCommandSuggestionVisible(list, index) {
  const item = list?.children?.[index];
  if (!item) return;
  const top = item.offsetTop;
  const bottom = top + item.offsetHeight;
  if (top < list.scrollTop) {
    list.scrollTop = top;
  } else if (bottom > list.scrollTop + list.clientHeight) {
    list.scrollTop = bottom - list.clientHeight;
  }
}

// Per-session unsent draft, persisted to localStorage so a reload (iOS evicts
// backgrounded PWAs freely) doesn't lose what you were typing. The prefix is
// deliberately DISTINCT from the old SPA's `moa-draft-` so the two frontends
// don't clobber each other's drafts while they coexist under /next.
export function Composer({ sessionId, session, shortPlaceholder = false, compact = false, steer = null, onSecret, transformMessage, onSent, plusActions = [], onFocusChange }) {
  const { skills, refreshSkills } = useSessionSkills(sessionId);
  const textareaRef = useRef(null);
  const attachInputRef = useRef(null);
  const restoreVoiceFocusRef = useRef(false);
  const sessionState = session?.state;
  const pendingSteers = session?.pendingSteers;
  // In steer mode the box targets a subagent, not the parent run — so it
  // must never enter the parent's "busy" affordances (Esc-aborts, queue note).
  // It always shows a Send button that fires a steer.
  const busy = sessionState === "running" && !steer;
  const [hasText, setHasText] = useState(false);
  // Safari may emit pointercancel instead of the click that completes a touch
  // activation. The reducer records pointer identity and latches the terminal
  // event before invoking send, so stale attachments cannot be submitted twice.
  const contentSendActivationRef = useRef(SEND_BUTTON_INITIAL);
  const dispatchContentSendRef = useRef(null);
  const [contentSendPending, setContentSendPending] = useState(false);
  const compositionRef = useRef(newCompositionState());
  // The last normal DOM value lets us remove precisely one stale IME insertion
  // without erasing text typed for the next message after a successful send.
  const inputValueRef = useRef("");
  // Guards a recall (chip click / Alt+↑) against double-activation before the
  // WS steers_canceled round-trip clears the chips: without it, a second click
  // (or click + Alt+↑) would see the same pendingSteers and combine the texts
  // twice into the textarea. Released once cancelSteers settles.
  const recallInFlight = useRef(false);
  // A click only counts as a recall when this chip also received its
  // pointerdown. The chip is born under the finger: it appears in the composer
  // the instant a message is queued, which is exactly where the send button was
  // just tapped, so the click that follows that tap lands on a control that did
  // not exist when the gesture started. Production traces caught it firing the
  // recall 11ms after a send (a real tap on it measured ~1500ms), cancelling
  // the message server-side while the send was still in flight — the text was
  // destroyed on both sides. Requiring the whole gesture to happen on the chip
  // rejects an inherited click by construction, with no timing heuristics.
  const recallPointerDown = useRef(null);
  // Counts every write to the textarea that a send did not make itself: a queue
  // recall or abort restoring messages, a voice transcript, history recall, an
  // accepted suggestion. A send captures the count before awaiting the server
  // and only clears the box if it has not moved.
  //
  // Comparing the text instead is not enough: a recall restores the very
  // message that was just sent, so the two are equal by value.
  //
  // Writes go through writeComposer so a new one cannot forget to bump it —
  // the previous fix listed the routes by hand and missed four.
  const composerEpoch = useRef(0);
  const writeComposer = useCallback((el, value) => {
    if (!el) return;
    el.value = value;
    composerEpoch.current += 1;
  }, []);


  // --- Slash command + @-mention suggestion state ---
  const [goalFlags, setGoalFlags] = useState([]);
  const [canTranscribe, setCanTranscribe] = useState(false);
  const [canVoiceLive, setCanVoiceLive] = useState(false);
  const [cmdSuggestions, setCmdSuggestions] = useState(null); // null = hidden
  const [cmdCursor, setCmdCursor] = useState(0);
  const [fileSuggestions, setFileSuggestions] = useState(null); // [{path, is_dir}] or null
  const [fileCursor, setFileCursor] = useState(0);
  const cmdSuggestionsRef = useRef(null);
  const fileAbortRef = useRef(null);
  const fileDebounceRef = useRef(null);

  useEffect(() => {
    if (!cmdSuggestions) return;
    keepCommandSuggestionVisible(cmdSuggestionsRef.current, cmdCursor);
  }, [cmdSuggestions, cmdCursor]);

  // --- Attachments ---
  const [attachments, setAttachments] = useState([]);
  // Slots reserved by in-flight addFiles calls, so two concurrent loads (e.g.
  // paste + picker) can't each independently reserve up to MAX and overshoot.
  const attachInFlightRef = useRef(0);

  // Fetch /goal flag metadata + transcription capability once on mount (mirrors
  // InputBar's capabilities check). `transcribe` drives whether the send button
  // doubles as a push-to-talk mic.
  useEffect(() => {
    if (!sessionId) return;
    fetch('/api/capabilities', { headers: { 'X-Moa-Request': '1' } })
      .then(r => r.json())
      .then(caps => {
        setGoalFlags(Array.isArray(caps.goal_flags) ? caps.goal_flags : []);
        setCanTranscribe(!!caps.transcribe);
        // A live call needs the same OpenAI key slot the transcriber uses, so
        // `transcribe` is the honest fallback until the server publishes a
        // capability of its own. Being wrong here costs a toast (the session
        // endpoint answers 503), never a silent failure.
        setCanVoiceLive(caps.voice_live === undefined ? !!caps.transcribe : !!caps.voice_live);
      })
      .catch(() => {});
  }, [sessionId]);

  const autoResize = useCallback(() => {
    const el = textareaRef.current;
    if (!el) return;
    el.style.height = "0";
    el.style.height = Math.min(el.scrollHeight, 132) + "px";
  }, []);

  // Restore the persisted draft on mount. The composer is keyed by session in
  // the container, so a session switch remounts this component (tearing down
  // in-flight file requests / attachment processing and clearing state) rather
  // than mutating sessionId in place.
  useEffect(() => {
    const el = textareaRef.current;
    if (!el) return;
    el.value = loadDraft(sessionId);
    inputValueRef.current = el.value;
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
  const handleDequeueSteers = useCallback((opts) => {
    const armedPointerId = recallPointerDown.current;
    recallPointerDown.current = null;
    if (!recallActivates({
      armedPointerId,
      pointerId: opts?.pointerId,
      detail: opts?.detail,
      fromKeyboard: opts?.fromKeyboard === true,
    })) return;
    if (recallInFlight.current) return; // a recall is already in flight
    const sess = store.get().sessions[sessionId];
    if (!sess?.pendingSteers?.length) return;

    const el = textareaRef.current;
    if (!el) return;

    recallInFlight.current = true;
    writeComposer(el, combineQueueText(el.value, sess.pendingSteers));
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
    // Typing "/" is the moment the list matters: re-read it (throttled) so a
    // skill created while this session was open can appear without a restart.
    if (el.value.startsWith('/')) refreshSkills();
    setCmdSuggestions(slashSuggestions(el.value, el.selectionStart, goalFlags, skills));
    setCmdCursor(0);
  }, [goalFlags, skills, refreshSkills]);

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
    writeComposer(el, value);
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
      writeComposer(el, before + cmd.name + ' ' + after);
      const newPos = before.length + cmd.name.length + 1;
      el.selectionStart = el.selectionEnd = newPos;
      el.focus();
      el.dispatchEvent(new Event('input', { bubbles: true }));
      setCmdSuggestions(null);
      return;
    }
    if (cmd.args) {
      writeComposer(el, '/' + cmd.name + ' ');
      setCmdSuggestions(null);
      el.focus();
      // Mirror the flag branch: fire input so the draft/hasText/autoResize
      // stay in sync (a reload before the next keystroke would otherwise lose
      // the just-picked command).
      el.dispatchEvent(new Event('input', { bubbles: true }));
    } else {
      writeComposer(el, '/' + cmd.name);
      setCmdSuggestions(null);
      dispatchContentSendRef.current?.(sendButtonEvent.keyActivate());
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

  const [plusMenuOpen, setPlusMenuOpen] = useState(false);
  // Never leave the menu hanging over another screen: a session switch remounts
  // this composer, and a send closes it below.
  useEffect(() => { setPlusMenuOpen(false); }, [sessionId]);

  const handleAttachClick = useCallback(() => {
    attachInputRef.current?.click();
  }, []);

  const handleAttachChange = useCallback((e) => {
    addFiles(e.target.files);
    e.target.value = ''; // allow re-selecting the same file
  }, [addFiles]);

  // Something shared into moa from another app, routed to THIS session by the
  // share picker (data/share.js). It lands exactly where a picked file lands —
  // an attachment chip, plus the shared text at the caret — and nothing is
  // sent: the owner writes what to do with it and presses send himself.
  //
  // Not a prop: the choice is made in an overlay that has no path to this
  // component, and the chosen session's composer may not be mounted yet (a
  // saved session is resumed first). The store is the handoff; the drop is
  // consumed on arrival so a re-render cannot place it twice.
  const drop = useStore((s) => (sessionId ? s.composerDrops[sessionId] : null));
  useEffect(() => {
    if (!drop || !sessionId || steer) return;
    consumeComposerDrop(sessionId);
    if (drop.text) {
      const el = textareaRef.current;
      if (el) {
        writeComposer(el, appendSharedText(el.value, drop.text));
        setHasText(!!el.value.trim());
        saveDraft(sessionId, el.value);
        autoResize();
        // A recalled queue (Stop) puts the owner back in the box to edit it;
        // a share does not steal focus, the owner is still choosing.
        if (drop.focus) {
          el.focus();
          el.selectionStart = el.selectionEnd = el.value.length;
        }
      }
    }
    if (drop.files?.length) addFiles(drop.files);
  }, [drop, sessionId, steer, addFiles, autoResize, writeComposer]);

  const handlePaste = useCallback((e) => {
    const files = Array.from(e.clipboardData?.files || []).filter((f) => f.type.startsWith('image/'));
    if (files.length > 0) {
      e.preventDefault();
      addFiles(files);
      return;
    }

    // Native text pastes subsequently emit input, which performs the same
    // check. Remove an old draft now as well, before the browser inserts a
    // recognized /secret command into the textarea.
    const pasted = e.clipboardData?.getData("text/plain");
    const el = textareaRef.current;
    if (pasted && el) {
      const start = el.selectionStart ?? el.value.length;
      const end = el.selectionEnd ?? start;
      const nextValue = el.value.slice(0, start) + pasted + el.value.slice(end);
      if (interceptSecretCommand(nextValue.trim()) !== null) saveDraft(sessionId, "");
    }
  }, [addFiles, sessionId]);

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

    // This client-only command is deliberately intercepted before generic
    // slash handling. It carries aliases only; values are typed exclusively in
    // SecretBatch's local password inputs and never enter this textarea's
    // draft/history lifecycle.
    const secretCommand = interceptSecretCommand(text);
    if (secretCommand !== null) {
      // Clear this before validating: every /secret invocation may contain an
      // accidentally typed value and must never enter composer history or its
      // persisted localStorage draft.
      el.value = secretCommand.composerDraft;
      saveDraft(sessionId, secretCommand.composerDraft);
      setHasText(false);
      setCmdSuggestions(null);
      setFileSuggestions(null);
      autoResize();
      if (atts.length > 0) {
        addToast({ title: 'Cannot attach files here', detail: 'Remove the attachments before storing secrets', type: 'attention' });
        return;
      }
      if (steer) {
        addToast({ title: 'Refused /secret command', detail: 'Secrets can only be staged from the main conversation composer. The command was discarded; rotate a value if you typed one.', type: 'error' });
        return;
      }
      if (secretCommand.error) {
        addToast({ title: 'Refused /secret command', detail: `${secretCommand.error} The command was discarded; rotate a value if you typed one.`, type: 'error' });
        return;
      }
      onSecret?.(secretCommand.aliases);
      return;
    }

    // Steer mode: everything the user types goes to the live subagent as a
    // steer. No slash/shell/queue semantics, no attachments (the subagent steer
    // endpoint is text-only). The message shows up in the child's transcript
    // when the child takes it into its context and the server echoes the steer
    // back over the socket; if the subagent already finished, we rebound to the
    // parent.
    if (steer && steer.jobId) {
      if (!text) return;
      const steerEpoch = composerEpoch.current;
      try {
        const res = await steerSubagent(sessionId, steer.jobId, text);
        // The server answers whether the child actually took the message; a
        // 200 alone does not mean it was queued. A refused steer must leave the
        // text where the user can resend it rather than silently empty the box.
        if (res && res.queued === false) throw new Error('the subagent did not accept the message');
        pushHistory(text);
        // Same ownership rule as an ordinary send: history recall or an
        // accepted suggestion during the round-trip means the box is no longer
        // this steer's to empty.
        if (!sendMayClear(steerEpoch, composerEpoch.current)) return;
        el.value = '';
        saveDraft(sessionId, '');
        setHasText(false);
        setCmdSuggestions(null);
        setFileSuggestions(null);
        setAttachments([]);
        autoResize();
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

    const sendEpoch = composerEpoch.current;
    const clearSentComposer = () => {
      // Only clear what this send is entitled to clear. An ordinary message
      // waits for the server response before emptying the box, and in that gap
      // the textarea can legitimately hold something else: a queue recall
      // restoring its messages, or an abort dumping them back. Wiping it
      // blindly destroyed text the user never sent — that is how a queued
      // message could vanish from both the server and the screen at once.
      if (!sendMayClear(sendEpoch, composerEpoch.current)) return;
      // Safari can deliver a composition input after the Enter that submitted
      // it. Its epoch identifies it as stale without rejecting a later, new
      // composition.
      compositionRef.current = compositionSubmitted(compositionRef.current);
      el.value = '';
      inputValueRef.current = '';
      saveDraft(sessionId, '');
      setHasText(false);
      setCmdSuggestions(null);
      setFileSuggestions(null);
      setAttachments([]);
      autoResize();
    };

    // Commands and shell escapes retain their established immediate-clear
    // semantics. Ordinary content waits for the server response below, so a
    // rejected attachment send remains a real retry rather than empty text.
    if (text.startsWith('/') || text.startsWith('!')) {
      if (text) pushHistory(text);
      clearSentComposer();
    }

    // Detect slash commands.
    if (text.startsWith('/')) {
      // Mobile keyboards autocorrect a typed "--" into an em/en-dash ("—"/"–"),
      // which breaks flag parsing (/goal … --max 3). Normalize a dash that
      // starts a token back into "--"; a real em-dash inside prose is left
      // untouched.
      const normalized = normalizeDashes(text);
      const commandName = normalized.trim().split(/\s+/, 1)[0].replace(/^\//, '').toLowerCase();
      const hasDeferredOutcome = commandName === 'compact' || commandName === 'prepare-compact';

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
        const policy = classifyCommand(normalized, skills);
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
          if (result && result.queued && result.id) {
            // Enqueued as a barrier under the ID the server echoes: confirm the
            // chip if it's still there (a concurrent command_dequeued may
            // already have removed it); never resurrect.
            const cur = store.get().sessions[sessionId];
            const list = cur?.pendingSteers;
            if (list && list.some((s) => s.id === cmdId)) {
              updateSession(sessionId, {
                pendingSteers: list.map((s) => (s.id === cmdId ? { ...s, confirmed: true } : s)),
              });
            }
            return; // queued — no immediate outcome to surface
          }
          // Either the command was ACCEPTED AND STARTED now (queued without an
          // ID: /compact, whose outcome arrives as WS events), or it ran
          // immediately because the run ended before the POST landed
          // (queued:false). Neither will produce a command_dequeued, so the
          // optimistic chip must be retired here or it stays forever.
          const cur = store.get().sessions[sessionId];
          if (cur?.pendingSteers) {
            const kept = cur.pendingSteers.filter((s) => s !== optimisticCmd);
            updateSession(sessionId, { pendingSteers: kept.length > 0 ? kept : null });
          }
          if (result && result.queued) return; // started — the outcome is not in this response
        } else if (result && result.queued) {
          return; // enqueued or started server-side without an optimistic chip
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
        // Only deferred compaction commands can still report their outcome over
        // the socket after a dead request. Other commands have no such terminal
        // event, so hiding their transport error would lose the only feedback.
        if (hasDeferredOutcome && e?.status == null) return;
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
      await sendMessage(sessionId, transformMessage ? transformMessage(text) : text, atts);
      if (text) pushHistory(text);
      clearSentComposer();
      onSent?.();
    } catch (e) {
      console.error('Send failed:', e);
      // sendMessage already rolled back the optimistic echo/chip; surface the
      // reason (e.g. a 400) so it's not silent.
      addToast({ sessionId, title: 'Message not sent', detail: `${String(e.message || e)} Your text and attachments are still here; you can retry.`, type: 'error' });
    }
  }, [sessionId, sessionState, attachments, pushHistory, autoResize, steer, onSecret, transformMessage, onSent]);

  const handleSend = useCallback(() => {
    setPlusMenuOpen(false);
    return handleSendInner(textareaRef.current);
  }, [handleSendInner]);
  const finishContentSend = useCallback(() => {
    const { state } = reduceContentSendActivation(
      contentSendActivationRef.current,
      sendButtonEvent.sendFinished(),
    );
    contentSendActivationRef.current = state;
    setContentSendPending(false);
  }, []);
  const dispatchContentSendActivation = useCallback((event) => {
    const { state, actions } = reduceContentSendActivation(contentSendActivationRef.current, event);
    contentSendActivationRef.current = state;
    if (actions.some((action) => action.type === "send")) {
      // The reducer sets sendInFlight synchronously above, before this async
      // handler can observe the current attachments. Every later DOM event is
      // inert until this exact invocation has completed.
      setContentSendPending(true);
      void Promise.resolve(handleSend()).finally(finishContentSend);
    } else if (contentSendActivationRef.current.sendInFlight) {
      addToast({ sessionId, title: 'Message is still sending', detail: 'Wait for the request to finish before sending again.', type: 'attention' });
    }
  }, [handleSend, finishContentSend, sessionId]);
  dispatchContentSendRef.current = dispatchContentSendActivation;
  const handleContentSendPointerDown = useCallback((e) => {
    if (e.button != null && e.button !== 0) return;
    dispatchContentSendActivation(sendButtonEvent.pointerDown(e.pointerId));
  }, [dispatchContentSendActivation]);
  const handleContentSendPointerUp = useCallback((e) => {
    dispatchContentSendActivation(sendButtonEvent.pointerUp(e.pointerId));
  }, [dispatchContentSendActivation]);
  const handleContentSendPointerCancel = useCallback((e) => {
    dispatchContentSendActivation(sendButtonEvent.pointerCancel(e.pointerId));
  }, [dispatchContentSendActivation]);
  const handleContentSendClick = useCallback(() => {
    dispatchContentSendActivation(sendButtonEvent.click());
  }, [dispatchContentSendActivation]);
  const handleContentSendKeyDown = useCallback((e) => {
    if (e.repeat || (e.key !== "Enter" && e.key !== " ")) return;
    e.preventDefault();
    dispatchContentSendActivation(sendButtonEvent.keyActivate());
  }, [dispatchContentSendActivation]);
  // --- Voice / push-to-talk ---
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
    writeComposer(el, before + sep + text + after);
    const newPos = start + sep.length + text.length;
    el.selectionStart = el.selectionEnd = newPos;
    if (restoreVoiceFocusRef.current) el.focus();
    restoreVoiceFocusRef.current = false;
    el.dispatchEvent(new Event('input', { bubbles: true }));
  }, []);

  const captureVoiceFocus = useCallback((activeElement) => {
    restoreVoiceFocusRef.current = activeElement === textareaRef.current;
  }, []);

  const onVoiceError = useCallback((msg) => {
    addToast({ title: 'Voice input', detail: msg, type: 'error' });
  }, []);

  const {
    handlers: voiceHandlers, recording, transcribing,
    supported: voiceSupported, toggleFromShortcut, cancel: cancelVoice,
  } = useVoiceGesture({
    onTranscript: insertAtCursor,
    onError: onVoiceError,
    onRecordingStart: captureVoiceFocus,
  });

  // Voice is usable only when the backend can transcribe AND the browser has a
  // MediaRecorder + mic (needs a secure context). Nothing else gates it: a
  // steer is typed in this same box, by the same thumb, and often while
  // walking — the one situation dictation exists for. The transcript lands in
  // the composer that asked for it (insertAtCursor writes THIS instance's
  // textarea, and useVoiceGesture holds one recorder per composer), so a
  // subagent's steer box cannot spill into the parent's.
  const canVoice = canTranscribe && voiceSupported;

  // --- Voice delegate (live call) ---
  // The minutes land in the composer as a draft, and they are NEVER sent
  // automatically: the owner reads, corrects or deletes them and sends them
  // himself. They are APPENDED as their own block rather than inserted at the
  // caret — a call runs for minutes, and during it the owner may have typed,
  // selected or moved the caret, so an insertion would replace his text with
  // the delegate's. The input event is dispatched the same way insertAtCursor
  // does it, so the draft, hasText and the auto-resize stay correct.
  const onVoiceLiveResult = useCallback((text) => {
    const el = textareaRef.current;
    if (!el) return;
    const next = appendCallResult(el.value, text);
    if (next === el.value) return;
    writeComposer(el, next);
    el.selectionStart = el.selectionEnd = next.length;
    el.dispatchEvent(new Event('input', { bubbles: true }));
  }, [writeComposer]);

  const onVoiceLiveError = useCallback((msg) => {
    addToast({ sessionId, title: 'Voice call', detail: msg, type: 'error' });
  }, [sessionId]);

  const voiceLive = useVoiceLive(sessionId, {
    onResult: onVoiceLiveResult,
    onError: onVoiceLiveError,
  });

  // Never in steer mode: that box writes to a subagent, and a call is a
  // conversation with THIS session.
  const canCall = canVoiceLive && voiceLive.supported && !steer && !!sessionId;

  const handleCallToggle = useCallback(() => {
    if (voiceLive.active) voiceLive.hangup();
    else voiceLive.start();
  }, [voiceLive.active, voiceLive.hangup, voiceLive.start]);

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
  // Esc is the keyboard's Stop. The work (abort, restore only the steers the
  // server confirms it discarded) is stopRun's, shared with the LiveBar's
  // button; the restored text comes back through the composerDrops handoff
  // below, the same way a share lands, so both entry points fill the box the
  // same way.
  const handleStop = useCallback(() => {
    if (!sessionId) return;
    stopRun(sessionId).catch((e) => console.error("Cancel failed:", e));
  }, [sessionId]);

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
        handleDequeueSteers({ fromKeyboard: true });
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

    // Esc discards a live recording. This is the cancel route the slide-away
    // gesture used to provide: the mic stops and the audio is thrown away, so
    // nothing you decided not to say reaches the field. It is checked before
    // the agent-stop below because while YOUR mic is live it is the nearer
    // thing to back out of.
    if (e.key === "Escape" && recording) {
      e.preventDefault();
      cancelVoice();
      return;
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
      if (e.isComposing || compositionRef.current.composing) return;
      e.preventDefault();
      dispatchContentSendActivation(sendButtonEvent.keyActivate());
      return;
    }

    if (e.key === "Enter" && !e.shiftKey) {
      if (e.isComposing || compositionRef.current.composing) return;
      e.preventDefault();
      dispatchContentSendActivation(sendButtonEvent.keyActivate());
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
      writeComposer(el, h.entries[h.idx]);
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
        writeComposer(el, h.draft);
        h.draft = "";
      } else {
        writeComposer(el, h.entries[h.idx]);
      }
      autoResize();
      saveDraft(sessionId, el.value); // keep the persisted draft in sync
      el.selectionStart = el.selectionEnd = el.value.length;
      updateSuggestions();
      return;
    }
  }, [
    sessionId, busy, fileSuggestions, fileCursor, cmdSuggestions, cmdCursor,
    handleDequeueSteers, handleStop, dispatchContentSendActivation, acceptFileMention, acceptSuggestion,
    cursorRow, totalRows, autoResize, updateSuggestions, recording, cancelVoice,
  ]);

  const handleInput = useCallback((e) => {
    const inputType = e.inputType || e.nativeEvent?.inputType;
    if (shouldDiscardLateCompositionInput(compositionRef.current, {
      inputType, isComposing: e.isComposing || e.nativeEvent?.isComposing,
    })) {
      compositionRef.current = compositionInputDiscarded(compositionRef.current);
      const restored = valueBeforeLateCompositionInput(inputValueRef.current, e.target.value, e.data || e.nativeEvent?.data);
      if (restored != null) {
        e.target.value = restored;
        inputValueRef.current = restored;
        saveDraft(sessionId, restored);
        setHasText(!!restored.trim());
      }
      autoResize();
      return;
    }
    inputValueRef.current = e.target.value;
    autoResize();
    updateSuggestions();
    // saveDraft recognizes /secret from its first line and removes any prior
    // draft instead of retaining a command that may have pasted a value below.
    saveDraft(sessionId, e.target.value);
    setHasText(!!e.target.value.trim());
    // File suggestions with debounce.
    clearTimeout(fileDebounceRef.current);
    fileDebounceRef.current = setTimeout(updateFileSuggestions, 100);
  }, [sessionId, autoResize, updateSuggestions, updateFileSuggestions]);

  const handleCompositionStart = useCallback(() => {
    compositionRef.current = compositionStarted(compositionRef.current);
  }, []);
  const handleCompositionEnd = useCallback(() => {
    compositionRef.current = compositionEnded(compositionRef.current);
  }, []);

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
  const short = compact || shortPlaceholder;
  // "Message moa" everywhere. The keyboard hints used to be printed here — 71
  // characters of instructions inside the field, which is the noisiest place in
  // the product to teach a shortcut and the one the eye returns to most. The
  // shortcuts themselves are unchanged; only their advertisement is gone.
  const idlePlaceholder = "Message moa";
  // Steer mode uses the standard busy placeholder because its header identifies
  // the subagent. Busy (parent run) copy states that Enter STEERS without stopping —
  // the persistent Send button already signals "you can always talk to it", so
  // the copy names the consequence. Mobile's pill has no room for the long form.
  const busyPlaceholder = short
    ? "Steer — it keeps working…"
    : "Steer the agent — ⏎ sends while it works, it won't stop it…";
  const placeholder = (steer || busy) ? busyPlaceholder : idlePlaceholder;

  /* `is-armed` — there is something to send. The send button is a quiet
      control until then, so the accent marks a real action rather than
      decorating an empty box. Attachments count: a photo with no caption is
      as sendable as a sentence. */
  const armed = hasText || attachments.length > 0;

  return (
    <div class={`zl-composer${busy ? " is-busy" : ""}${armed ? " is-armed" : ""}`}>
      {cacheExpired && (
        <div class="cache-warn" title="The prompt cache for this conversation has expired. Your next message will pay for a fresh cache write (more expensive).">
          <span class="cache-warn-dot" />
          Prompt cache expired · your next message pays a cache write
        </div>
      )}
      {voiceLive.active && (
        <VoiceLivePanel
          phase={voiceLive.phase}
          endedReason={voiceLive.endedReason}
          micState={voiceLive.micState}
          questionsUsed={voiceLive.questionsUsed}
          maxQuestions={voiceLive.maxQuestions}
          pendingAsks={voiceLive.pendingAsks}
          elapsed={voiceLive.elapsed}
          onHangup={voiceLive.hangup}
        />
      )}
      {attachments.length > 0 && (
        <div class="attach-preview-strip">
          {attachments.map((a, i) => (
            <div class="attach-chip" key={i}>
              {a.isImage
                ? <img src={`data:${a.mime};base64,${a.data}`} alt={a.name} />
                : <span class="attach-chip-name">📎 {a.name} <span class="attach-chip-size">({Math.max(1, Math.round(a.size / 1024))} kB)</span></span>
              }
              <button type="button" class="attach-chip-remove" onClick={() => removeAttachment(i)} title="Remove" disabled={contentSendPending}>
                <X size={12} />
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
        <div class="cmd-suggestions" ref={cmdSuggestionsRef}>
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
        class="zl-ta"
        aria-label="Message moa"
        placeholder={placeholder}
        onInput={handleInput}
        onKeyDown={handleKey}
        onCompositionStart={handleCompositionStart}
        onCompositionEnd={handleCompositionEnd}
        onPaste={handlePaste}
        onFocus={onFocusChange ? () => onFocusChange(true) : undefined}
        onBlur={onFocusChange ? () => onFocusChange(false) : undefined}
        readOnly={contentSendPending}
      />
      {summary && (
        <button
          type="button"
          class="queue-note"
          title="Click or Alt+↑ to edit queued messages"
          onPointerDown={(e) => { recallPointerDown.current = e.pointerId ?? true; }}
          onPointerCancel={() => { recallPointerDown.current = null; }}
          onClick={(e) => handleDequeueSteers({ pointerId: e.pointerId, detail: e.detail })}
          onKeyDown={(e) => {
            if (e.key !== "Enter" && e.key !== " ") return;
            e.preventDefault();
            handleDequeueSteers({ fromKeyboard: true });
          }}
        >
          <Chip size="sm" mono>{summary.count} queued</Chip>
          <span>
            {summary.lastImages > 0 && <ImageIcon size={13} aria-hidden="true" />}
            {summary.lastIsCommand && <span aria-hidden="true">/</span>}
            “{summary.lastText}”
          </span>
        </button>
      )}
      {busy && hasText && !summary && (
        <span class="steer-hint" aria-hidden="true">⏎ steers — won't interrupt</span>
      )}
      <div class="zl-controls">
        {plusActions.length > 0 ? (
          <ActionMenu
            open={plusMenuOpen}
            onOpenChange={setPlusMenuOpen}
            icon={AttachIcon}
            label="More"
            triggerClass="zl-attach"
            triggerSize={18}
            placement="up"
            disabled={contentSendPending}
            actions={[
              { id: "attach", icon: Paperclip, label: "Attach files", onClick: handleAttachClick },
              ...plusActions,
            ]}
          />
        ) : (
          <button type="button" class="zl-attach" title="Attach files" aria-label="Attach" onClick={handleAttachClick} disabled={contentSendPending}>
            <AttachIcon />
          </button>
        )}
        <span class="zl-controls-spring" />
        {canVoice && (
          /* Dictation, with a permanent seat of its own in both densities.
             It drives the voice machine through its pointer-free toggle: tap
             to start dictating, tap again to stop and transcribe (⌘./Alt+.
             does the same), and the transcript lands at the caret.

             It used to take the send button over on the phone, because the
             pill had room for exactly one control at its end. That made the
             mic disappear the moment there was a draft — so dictating,
             fixing a word and dictating some more, the natural way to use
             it, was impossible. The second row is what buys both a seat. */
          <button
            type="button"
            class={`zl-attach zl-mic${recording ? " recording" : ""}${transcribing ? " transcribing" : ""}`}
            aria-label={transcribing ? "Transcribing" : recording ? "Stop recording" : "Dictate"}
            title={
              transcribing ? "Transcribing…"
                : recording ? "Tap to stop & transcribe · Esc discards"
                  : `Dictate (${formatShortcut(".", { mod: true })})`
            }
            disabled={transcribing || contentSendPending}
            {...voiceHandlers}
          >
            {/* Recording keeps the mic glyph: a live microphone is a state of
                MY input, not a stop control, and a square here read as the
                same thing as the agent's Stop. The ring says "live". */}
            {transcribing ? <Loader2 size={15} class="spin" /> : <Mic size={15} />}
          </button>
        )}
        {canCall && (
          /* Talk live — turns this conversation into a voice call. It sits
             next to dictation because both are "speak instead of type", but
             they are different verbs: the mic writes what YOU said, this one
             hands the conversation to a delegate that talks back and returns
             minutes. Hence a phone glyph, never a second microphone. */
          <button
            type="button"
            class={`zl-attach zl-call${voiceLive.active ? " in-call" : ""}`}
            aria-label={voiceLive.active ? "End call" : "Talk live"}
            title={
              voiceLive.active
                ? "End call — the minutes land here as a draft"
                : "Talk live — a voice delegate takes this conversation"
            }
            disabled={contentSendPending || voiceLive.phase === 'closing'}
            onClick={handleCallToggle}
          >
            {voiceLive.phase === 'connecting' || voiceLive.phase === 'closing'
              ? <Loader2 size={15} class="spin" />
              : <PhoneCall size={15} />}
          </button>
        )}
        {/* Send is always Send now. It no longer has to ask whose turn it is:
            the mic is a button beside it, so one tap has exactly one meaning
            in every state and density. */}
        <button
          type="button"
          class="zl-send"
          aria-label={contentSendPending ? "Sending" : busy ? "Send steer" : "Send"}
          title={
            contentSendPending ? "Sending…"
              : busy ? "Send — steers the agent, doesn't stop it"
                : "Send"
          }
          disabled={!armed}
          onPointerDown={handleContentSendPointerDown}
          onPointerUp={handleContentSendPointerUp}
          onPointerCancel={handleContentSendPointerCancel}
          onClick={handleContentSendClick}
          onKeyDown={handleContentSendKeyDown}
        >
          {contentSendPending ? <Loader2 size={16} class="spin" /> : <SendIcon />}
        </button>
      </div>
    </div>
  );
}
