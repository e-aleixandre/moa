import { useEffect, useLayoutEffect, useRef, useState } from "preact/hooks";
import { Plus, MoreHorizontal } from "lucide-preact";
import { StateDot } from "../../../primitives/index.js";
import { useSheetDismiss } from "../../../hooks/useSheetDismiss.js";
import "./SessionDrawer.css";

const FOCUSABLE_SELECTOR =
  'a[href], button:not([disabled]), textarea:not([disabled]), input:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])';

// SessionCardMenu — the per-card ⋯ overflow (TELEMETRY-SETTINGS-REDESIGN §3.3).
// Session lifecycle (close / reopen / delete) is list management, not a
// conversation setting, so it lives here on the card rather than inside the
// chat view. Close archives; Reopen resumes a saved session; Delete is
// irreversible so it takes a deliberate second
// tap to confirm. Self-contained: owns its open state, click-outside and
// Escape, and stops taps from bubbling to the card's own select handler.
function SessionCardMenu({ session, onClose, onReopen, onDelete }) {
  const [open, setOpen] = useState(false);
  const [confirmingDelete, setConfirmingDelete] = useState(false);
  const [dropUp, setDropUp] = useState(false);
  const ref = useRef(null);
  const actionsRef = useRef(null);

  useEffect(() => {
    if (!open) return;
    const onDocDown = (e) => {
      if (ref.current && !ref.current.contains(e.target)) setOpen(false);
    };
    // Capture Escape here and stop it: otherwise the drawer's own key handler
    // (added on document) would also fire and close the whole drawer.
    const onKeyDown = (e) => {
      if (e.key === "Escape") {
        e.stopPropagation();
        setOpen(false);
      }
    };
    document.addEventListener("mousedown", onDocDown);
    document.addEventListener("keydown", onKeyDown, true);
    return () => {
      document.removeEventListener("mousedown", onDocDown);
      document.removeEventListener("keydown", onKeyDown, true);
    };
  }, [open]);

  // The drawer list is a scroll container, so an absolutely-positioned popup on
  // the last cards would be clipped when it opens downward. Measure the space
  // below the ⋯ button against the list's viewport and flip the menu upward
  // when it wouldn't fit.
  useLayoutEffect(() => {
    if (!open) {
      setDropUp(false);
      return;
    }
    const btn = ref.current?.querySelector(".sdcard-menu-btn");
    const menu = actionsRef.current;
    if (!btn || !menu) return;
    const scroller = ref.current.closest(".sdrawer-list");
    const bounds = scroller ? scroller.getBoundingClientRect() : { bottom: window.innerHeight };
    const spaceBelow = bounds.bottom - btn.getBoundingClientRect().bottom;
    setDropUp(menu.offsetHeight + 8 > spaceBelow);
  }, [open, confirmingDelete]);

  // Reset the delete confirmation whenever the menu closes.
  useEffect(() => { if (!open) setConfirmingDelete(false); }, [open]);

  const isSaved = session.saved;
  const stop = (e) => { e.stopPropagation(); };

  return (
    <div class="sdcard-menu" ref={ref} onClick={stop}>
      <button
        type="button"
        class="sdcard-menu-btn"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label="Session actions"
        onClick={(e) => { stop(e); setOpen((v) => !v); }}
      >
        <MoreHorizontal size={16} aria-hidden="true" />
      </button>
      {open && (
        <div
          class={dropUp ? "sdcard-actions sdcard-actions--up" : "sdcard-actions"}
          role="menu"
          aria-label="Session actions"
          ref={actionsRef}
        >
          {isSaved ? (
            <button type="button" role="menuitem" class="sdcard-action" onClick={() => { setOpen(false); onReopen?.(session.id); }}>
              Reopen session
            </button>
          ) : (
            <button type="button" role="menuitem" class="sdcard-action" onClick={() => { setOpen(false); onClose?.(session.id); }}>
              Close session
            </button>
          )}
          {confirmingDelete ? (
            <button
              type="button"
              role="menuitem"
              class="sdcard-action sdcard-action-danger"
              onClick={() => { setOpen(false); onDelete?.(session.id); }}
            >
              Delete — this cannot be undone
            </button>
          ) : (
            <button
              type="button"
              role="menuitem"
              class="sdcard-action sdcard-action-danger"
              onClick={(e) => { stop(e); setConfirmingDelete(true); }}
            >
              Delete…
            </button>
          )}
        </div>
      )}
    </div>
  );
}

// SessionDrawerCard — one session tile inside the drawer. Richer than the
// SessionStrip chip: three rows (title + state + time / last message / path).
// The card body is a tap target (select); the ⋯ overflow handles lifecycle.
function SessionDrawerCard({ session, onSelect, onCloseSession, onReopenSession, onDeleteSession }) {
  const { id, title, state, when, last, needsLabel, path, unseen } = session;
  const needs = state === "permission" || state === "error";
  const cls = ["sdcard", session.active ? "sdcard-active" : "", needs ? "sdcard-needs" : ""]
    .filter(Boolean)
    .join(" ");
  const ariaLabel = `${title} — ${state}`;
  return (
    <div class={cls}>
      <button
        type="button"
        class="sdcard-body"
        aria-label={ariaLabel}
        onClick={() => onSelect?.(id)}
      >
        {unseen && <span class="sdcard-unseen" aria-hidden="true" />}
        <span class="sdcard-top">
          <StateDot state={state} size={9} />
          <span class="sdcard-title">{title}</span>
          <span class="sdcard-when">{when}</span>
        </span>
        {(needsLabel || last) && (
          <span class="sdcard-last">
            {needsLabel && <b class="sdcard-needs-label">{needsLabel} </b>}
            {last}
          </span>
        )}
        <span class="sdcard-path">{path}</span>
      </button>
      <SessionCardMenu
        session={session}
        onClose={onCloseSession}
        onReopen={onReopenSession}
        onDelete={onDeleteSession}
      />
    </div>
  );
}

// SessionDrawer — mobile bottom-sheet that slides up over the conversation
// screen to list every session. Replicates Sheet's focus-trap / Escape /
// restore-focus behaviour with hooks (Sheet is a centered modal, not a
// bottom-sheet, so we don't reuse it). Anchors to its positioned container.
//
// Open/close is a small state machine so both the enter and the LEAVE animate
// (MOBILE-POLISH-SPEC §5): `open` is the caller's intent; internally we keep the
// sheet mounted through the close transition (`visible`) and toggle `entered`
// one frame after mount so the CSS `.is-open` transition plays from the closed
// rest state. Only the sheet (transform) and veil (opacity) move — the
// conversation behind stays perfectly still.
//
// Swipe-to-close: the drawer owns the dismiss gesture end-to-end via
// useSheetDismiss. While dragging we render the sheet in its `is-drag` state
// (CSS transitions off) and let the hook drive translate/opacity inline; on
// release the hook animates to rest and, when it settles closed, calls
// `onClose` (spring-back to open calls nothing), and we clear the inline styles
// once the committed CSS state has taken over.
export function SessionDrawer({
  open,
  onClose,
  sessions = [],
  activeCount = 0,
  savedCount = 0,
  onSelect,
  onNew,
  onCloseSession,
  onReopenSession,
  onDeleteSession,
}) {
  const { sheetRef, veilRef, dragging, grabBind } = useSheetDismiss({ onClose });
  const panelRef = useRef(null);
  const previousFocusRef = useRef(null);
  const closeTimerRef = useRef(null);
  const [visible, setVisible] = useState(open);
  const [entered, setEntered] = useState(open);

  // Enter/leave state machine driven by `open`. Enter: mount, then flip
  // `entered` on the next frame so the .is-open transition runs. Leave: drop
  // `entered` (sheet transitions back down) and unmount after the close
  // duration. Reduced motion snaps both ways.
  useEffect(() => {
    const reduce =
      typeof window !== "undefined" &&
      typeof window.matchMedia === "function" &&
      window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    if (open) {
      clearTimeout(closeTimerRef.current);
      setVisible(true);
      if (reduce) {
        setEntered(true);
      } else {
        const raf = requestAnimationFrame(() => setEntered(true));
        return () => cancelAnimationFrame(raf);
      }
    } else {
      setEntered(false);
      if (reduce) {
        setVisible(false);
      } else {
        closeTimerRef.current = setTimeout(() => setVisible(false), 220);
      }
    }
    return undefined;
  }, [open]);

  useEffect(() => () => clearTimeout(closeTimerRef.current), []);

  // Once the drawer is committed open AND the CSS `.is-open` rest state is in
  // effect (entered, not mid-drag), clear any inline styles the swipe hook left
  // on the veil/sheet so the class owns them again. Gating on `entered` (not
  // just `open`) matters for the drag→open handoff: clearing before `.is-open`
  // is applied would drop the sheet to its closed rest position for a frame.
  useEffect(() => {
    if (!open || !entered || dragging) return;
    if (sheetRef.current) {
      sheetRef.current.style.transition = "";
      sheetRef.current.style.transform = "";
    }
    if (veilRef.current) {
      veilRef.current.style.transition = "";
      veilRef.current.style.opacity = "";
    }
  }, [open, entered, dragging, sheetRef, veilRef]);

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

  if (!visible && !dragging) return null;

  const onVeilClick = (e) => {
    if (e.target === e.currentTarget) onClose?.();
  };

  const isOpen = entered && !dragging;
  const veilCls = `sdrawer-veil${isOpen ? " is-open" : ""}${dragging ? " is-drag" : ""}`;
  const sheetCls = `sdrawer${isOpen ? " is-open" : ""}${dragging ? " is-drag" : ""}`;

  return (
    <div class={veilCls} ref={veilRef} onClick={onVeilClick}>
      <div
        class={sheetCls}
        role="dialog"
        aria-modal="true"
        aria-label="Sessions"
        tabIndex={-1}
        ref={(el) => {
          panelRef.current = el;
          sheetRef.current = el;
        }}
      >
        <button
          type="button"
          class="sdrawer-grab"
          aria-label="Close sessions"
          onClick={() => onClose?.()}
          {...grabBind}
        >
          <span class="sdrawer-grab-bar" aria-hidden="true" />
        </button>
        <div class="sdrawer-head" {...grabBind}>
          <h2>Sessions</h2>
          <span class="sdrawer-count">
            {activeCount} active · {savedCount} saved
          </span>
        </div>
        <div class="sdrawer-list">
          {sessions.map((s) => (
            <SessionDrawerCard
              key={s.id}
              session={s}
              onSelect={onSelect}
              onCloseSession={onCloseSession}
              onReopenSession={onReopenSession}
              onDeleteSession={onDeleteSession}
            />
          ))}
        </div>
        <button type="button" class="sdrawer-new" onClick={() => onNew?.()}>
          <Plus size={15} aria-hidden="true" /> New session
        </button>
      </div>
    </div>
  );
}
