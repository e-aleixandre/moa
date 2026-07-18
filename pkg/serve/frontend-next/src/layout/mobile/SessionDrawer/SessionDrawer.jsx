import { useEffect, useRef } from "preact/hooks";
import { Plus } from "lucide-preact";
import { StateDot } from "../../../primitives/index.js";
import "./SessionDrawer.css";

const FOCUSABLE_SELECTOR =
  'a[href], button:not([disabled]), textarea:not([disabled]), input:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])';

// SessionDrawerCard — one session tile inside the drawer. Richer than the
// SessionStrip chip: three rows (title + state + time / last message / path).
// The whole card is a tap target.
function SessionDrawerCard({ session, onSelect }) {
  const { id, title, state, when, last, needsLabel, path, unseen } = session;
  const needs = state === "permission" || state === "error";
  const cls = ["sdcard", session.active ? "sdcard-active" : "", needs ? "sdcard-needs" : ""]
    .filter(Boolean)
    .join(" ");
  const ariaLabel = `${title} — ${state}`;
  return (
    <button
      type="button"
      class={cls}
      aria-label={ariaLabel}
      onClick={() => onSelect?.(id)}
    >
      {unseen && <span class="sdcard-unseen" aria-hidden="true" />}
      <span class="sdcard-top">
        <StateDot state={state} size={9} />
        <span class="sdcard-title">{title}</span>
        <span class="sdcard-when">{when}</span>
      </span>
      <span class="sdcard-last">
        {needsLabel && <b class="sdcard-needs-label">{needsLabel} </b>}
        {last}
      </span>
      <span class="sdcard-path">{path}</span>
    </button>
  );
}

// SessionDrawer — mobile bottom-sheet that slides up over the conversation
// screen to list every session. Replicates Sheet's focus-trap / Escape /
// restore-focus behaviour with hooks (Sheet is a centered modal, not a
// bottom-sheet, so we don't reuse it). Anchors to its positioned container.
export function SessionDrawer({
  open,
  onClose,
  sessions = [],
  activeCount = 0,
  savedCount = 0,
  onSelect,
  onNew,
  onEdit,
}) {
  const panelRef = useRef(null);
  const previousFocusRef = useRef(null);

  // Escape closes; Tab cycles focus within the panel (wrapping at the edges).
  useEffect(() => {
    if (!open) return;
    const onKeyDown = (e) => {
      if (e.key === "Escape") {
        onClose?.();
        return;
      }
      if (e.key !== "Tab") return;
      const panel = panelRef.current;
      if (!panel) return;
      const focusable = Array.from(
        panel.querySelectorAll(FOCUSABLE_SELECTOR)
      ).filter((el) => el.offsetParent !== null || el === document.activeElement);
      if (focusable.length === 0) {
        e.preventDefault();
        panel.focus();
        return;
      }
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault();
        first.focus();
      }
    };
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [open, onClose]);

  // On open: remember the trigger and focus the panel's first focusable.
  // On close: restore focus to the remembered element.
  useEffect(() => {
    if (!open) return;
    previousFocusRef.current = document.activeElement;
    const panel = panelRef.current;
    if (panel) {
      const firstFocusable = panel.querySelector(FOCUSABLE_SELECTOR);
      (firstFocusable || panel).focus();
    }
    return () => {
      const toRestore = previousFocusRef.current;
      if (toRestore && typeof toRestore.focus === "function") {
        toRestore.focus();
      }
    };
  }, [open]);

  if (!open) return null;

  const onVeilClick = (e) => {
    if (e.target === e.currentTarget) onClose?.();
  };

  return (
    <div class="sdrawer-veil" onClick={onVeilClick}>
      <div
        class="sdrawer"
        role="dialog"
        aria-modal="true"
        aria-label="Sessions"
        tabIndex={-1}
        ref={panelRef}
      >
        <div class="sdrawer-grab" aria-hidden="true" />
        <div class="sdrawer-head">
          <h2>Sessions</h2>
          <span class="sdrawer-count">
            {activeCount} active · {savedCount} saved
          </span>
          <button type="button" class="sdrawer-edit" onClick={() => onEdit?.()}>
            Edit
          </button>
        </div>
        <div class="sdrawer-list">
          {sessions.map((s) => (
            <SessionDrawerCard key={s.id} session={s} onSelect={onSelect} />
          ))}
        </div>
        <button type="button" class="sdrawer-new" onClick={() => onNew?.()}>
          <Plus size={15} aria-hidden="true" /> New session
        </button>
      </div>
    </div>
  );
}
