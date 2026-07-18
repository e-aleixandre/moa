import { useRef, useCallback, useEffect, useState } from "preact/hooks";
import { Plus, Slash, ArrowUp, Square } from "lucide-preact";
import { Chip } from "../../primitives/index.js";
import {
  sendMessage, cancelRun, cancelSteers,
} from "../../data/session-actions.js";
import { store, updateSession } from "../../data/store.js";
import { addToast } from "../../data/notifications.js";
import { combineQueueText, droppedImageCount, queueSummary } from "../../data/composer-queue.js";
import "./Composer.css";

// Composer — the conversation input: send a message, steer/queue while the
// agent works, recall the queue to edit, and stop the run. This is a STATEFUL
// container (unlike the presentational Spine/ChatHead/etc.) because it owns a
// lot of DOM-bound state — the textarea ref, per-session drafts, and input
// history — that can't be lifted without churn. It receives `sessionId` and
// `session` by props (the ConversationScreen container reads them from the
// store), exactly like the old SPA's InputBar. The queue/recall/abort/history/
// draft logic is ported faithfully from pkg/serve/frontend/src/components/
// InputBar.jsx, recording the pieces that belong to later subphases as stubs.
//
// Deferred to later subphases (NOT wired here):
//   5E — slash commands (/…), shell escapes (!…), @-mentions, command-policy.
//   5E — attachments (the "+" button is a no-op).
//   5F — permission / ask_user prompts.
//   5J — subagent steering (viewingSubagent).
//   5M — voice / push-to-talk on the send button (plain click here).

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

export function Composer({ sessionId, session }) {
  const textareaRef = useRef(null);
  const sessionState = session?.state;
  const pendingSteers = session?.pendingSteers;
  const busy = sessionState === "running";
  const [hasText, setHasText] = useState(false);
  // Guards a recall (chip click / Alt+↑) against double-activation before the
  // WS steers_canceled round-trip clears the chips: without it, a second click
  // (or click + Alt+↑) would see the same pendingSteers and combine the texts
  // twice into the textarea. Released once cancelSteers settles.
  const recallInFlight = useRef(false);

  const autoResize = useCallback(() => {
    const el = textareaRef.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = Math.min(el.scrollHeight, 160) + "px";
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

  // --- Send / enqueue ---
  // Ported from InputBar.handleSendInner, trimmed to the 5D scope: normal text
  // messages only. sendMessage(id, text, []) is the single source of truth for
  // the send-vs-enqueue decision — an idle/errored session runs the message and
  // starts a run; a busy session mints a steer chip (optimistic, reconciled by
  // id) — so the composer just calls it and surfaces failures via a toast
  // (sendMessage already rolled back its optimistic echo/chip). Slash (/) and
  // shell (!) prefixes are 5E: for now they go through as plain text.
  const handleSendInner = useCallback(async (el) => {
    if (!el || !sessionId) return;
    const text = el.value.trim();
    if (!text) return;

    // 5E: if (text.startsWith('/') || text.startsWith('!')) classify + route
    // through command-policy / execShell. Until then, send as normal text.

    pushHistory(text);
    el.value = "";
    saveDraft(sessionId, ""); // sent — drop the persisted draft
    setHasText(false);
    autoResize();

    try {
      await sendMessage(sessionId, text, []);
    } catch (e) {
      console.error("Send failed:", e);
      // sendMessage already rolled back the optimistic echo/chip; surface the
      // reason (e.g. a 400) so it's not silent.
      addToast({ sessionId, title: "Message not sent", detail: String(e.message || e), type: "error" });
    }
  }, [sessionId, pushHistory, autoResize]);

  const handleSend = useCallback(() => handleSendInner(textareaRef.current), [handleSendInner]);

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
      return;
    }
  }, [sessionId, busy, handleDequeueSteers, handleStop, handleSend, cursorRow, totalRows, autoResize]);

  const handleInput = useCallback((e) => {
    autoResize();
    saveDraft(sessionId, e.target.value);
    setHasText(!!e.target.value.trim());
  }, [sessionId, autoResize]);

  const summary = queueSummary(pendingSteers);
  const placeholder = busy ? "Steer the agent…" : "Message moa — Enter to send, ⇧Enter for a new line, ⌥Enter to queue…";

  return (
    <div class="composer-wrap">
      <div class="composer">
        <textarea
          ref={textareaRef}
          rows={1}
          class="composer-textarea"
          aria-label="Message moa"
          placeholder={placeholder}
          onInput={handleInput}
          onKeyDown={handleKey}
        />
        <div class="composer-bar">
          {/* 5E: attach — no-op until the attachments subphase. */}
          <button type="button" class="composer-btn" title="Attach (coming soon)" aria-label="Attach" onClick={() => { /* 5E */ }}>
            <Plus size={15} />
          </button>
          {/* 5E: slash commands — no-op until the command subphase. */}
          <button type="button" class="composer-btn" title="Slash commands (coming soon)" aria-label="Slash commands" onClick={() => { /* 5E */ }}>
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
          {busy ? (
            <button type="button" class="composer-send composer-stop" aria-label="Stop (Esc)" title="Stop (Esc)" onClick={handleStop}>
              <Square size={14} />
            </button>
          ) : (
            <button type="button" class="composer-send" aria-label="Send" onClick={handleSend}>
              <ArrowUp size={16} />
            </button>
          )}
        </div>
      </div>
    </div>
  );
}
