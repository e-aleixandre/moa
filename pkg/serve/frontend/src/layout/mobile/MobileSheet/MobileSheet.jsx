import { useEffect, useLayoutEffect, useRef, useState } from "preact/hooks";
import { MOTION, prefersReducedMotion } from "../../../hooks/motion.js";
import { useSheetDismiss } from "../../../hooks/useSheetDismiss.js";
import "./MobileSheet.css";

const FOCUSABLE_SELECTOR =
  'a[href], button:not([disabled]), textarea:not([disabled]), input:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])';

// MobileSheet — the approved Four Doors bottom-sheet (future-control-ia/a.html →
// fx.css .fx-scrim/.fx-sheet). It is NOT the centered generic <Sheet> modal: it
// slides UP from the bottom and — per real-device feedback — OCCUPIES the bottom
// of the mobile container exactly like the SessionDrawer, with the scrim dimming
// the whole surface (transcript AND composer) and the sheet's lower edge flush at
// the container bottom, so the composer is never left exposed under it and there
// is no gap. One scope per sheet; the header carries the title and a
// right-aligned scope tag.
//
// It reuses the exact proven mobile-sheet plumbing the SessionDrawer already
// ships: the enter/leave state machine (both directions animate, MOBILE-POLISH
// §5), swipe-down-to-dismiss via useSheetDismiss, and focus trap + restore.
//
// Placement: the panel/scrim are absolutely positioned inside the nearest
// positioned ancestor (.mconv), pinned to its edges (scrim inset:0, sheet
// bottom:0) — same anchoring the SessionDrawer uses.
//
// `bare` is for a child that IS a surface with its own head — the session panel,
// which carries the eyebrow/back/X the dossier keeps in both densities. The
// sheet still owns the scrim, the enter/leave, the focus trap and the grabber;
// it just does not draw a second title over the child's.
export function MobileSheet({ open, onClose, onClosed, title, scope, bare = false, children }) {
  const { sheetRef, veilRef, dragging, grabBind } = useSheetDismiss({ onClose });
  const panelRef = useRef(null);
  const previousFocusRef = useRef(null);
  const closeTimerRef = useRef(null);
  const onClosedRef = useRef(onClosed);
  onClosedRef.current = onClosed;
  const wasOpenRef = useRef(open);
  const [visible, setVisible] = useState(open);
  const [entered, setEntered] = useState(open);

  // Enter/leave state machine (mirrors SessionDrawer): mount then flip `entered`
  // next frame so the .is-open transition runs; on close drop `entered` and
  // unmount after the leave duration. Reduced motion snaps both ways.
  useEffect(() => {
    const reduce = prefersReducedMotion();
    if (open) {
      wasOpenRef.current = true;
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
      // Fire onClosed only on a real open→close transition, once the sheet has
      // fully dismissed/unmounted — so a caller can hand off to another overlay
      // (e.g. the Rewind timeline) without stacking it above an outgoing sheet.
      const fireClosed = () => {
        if (!wasOpenRef.current) return;
        wasOpenRef.current = false;
        onClosedRef.current?.();
      };
      if (reduce) {
        setVisible(false);
        fireClosed();
      } else {
        closeTimerRef.current = setTimeout(() => {
          setVisible(false);
          fireClosed();
        }, MOTION.exitBase);
      }
    }
    return undefined;
  }, [open]);

  useEffect(() => () => clearTimeout(closeTimerRef.current), []);

  // Clear inline styles the swipe hook wrote once the committed .is-open rest
  // state is in effect, so the class owns transform/opacity again.
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
      const focusable = Array.from(panel.querySelectorAll(FOCUSABLE_SELECTOR)).filter(
        (el) => el.offsetParent !== null || el === document.activeElement
      );
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

  // On open: remember the trigger and focus the panel. On close: restore focus.
  useLayoutEffect(() => {
    if (!open) return undefined;
    previousFocusRef.current = document.activeElement;
    const panel = panelRef.current;
    if (panel) panel.focus();
    return () => {
      const toRestore = previousFocusRef.current;
      if (toRestore && typeof toRestore.focus === "function") toRestore.focus();
    };
  }, [open]);

  if (!visible && !dragging) return null;

  const isOpen = entered && !dragging;
  const onScrimClick = (e) => {
    if (e.target === e.currentTarget) {
      onClose?.();
    }
  };

  return (
    <div
      class={`msheet-scrim${isOpen ? " is-open" : ""}${dragging ? " is-drag" : ""}`}
      ref={veilRef}
      onClick={onScrimClick}
    >
      <section
        class={`msheet${isOpen ? " is-open" : ""}${dragging ? " is-drag" : ""}${bare ? " is-bare" : ""}`}
        role="dialog"
        aria-modal="true"
        aria-label={title}
        tabIndex={-1}
        ref={(el) => {
          panelRef.current = el;
          sheetRef.current = el;
        }}
      >
        <button
          type="button"
          class="msheet-grab"
          aria-label="Close"
          onClick={() => onClose?.()}
          {...grabBind}
        >
          <span class="msheet-grab-bar" aria-hidden="true" />
        </button>
        {bare ? (
          children
        ) : (
          <>
            <div class="msheet-head" {...grabBind}>
              <span class="msheet-title">{title}</span>
              {scope && <span class="msheet-scope">{scope}</span>}
            </div>
            <div class="msheet-body">{children}</div>
          </>
        )}
      </section>
    </div>
  );
}
