import { useEffect, useRef, useState } from "preact/hooks";
import { Sidebar } from "../../Sidebar/Sidebar.jsx";
import { openOverlay } from "../../../data/overlay-history.js";
import { NewSessionView } from "./NewSessionView.jsx";
import "./SessionDrawer.css";

const FOCUSABLE_SELECTOR =
  'a[href], button:not([disabled]), textarea:not([disabled]), input:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])';

// SessionDrawer — the phone's CHASSIS for the sidebar. It owns the veil, the
// slide, the focus trap and the second screen; it does not own the list.
//
// It used to own all of it: its own head ("Sessions", "N open · M saved"), its
// own search field, its own group labels, its own footer. That was a second
// implementation of the desktop Spine, kept in step by hand — and by the time
// anyone looked, the phone had no "Needs attention" group and the desktop had
// no filter. Now both mount the same <Sidebar/> and this file is the 300px
// sheet it arrives in.
//
// A SHEET FROM THE LEFT EDGE, not a dropdown. It used to hang from the title
// chip at 390px wide, which covered the screen: the conversation you came from
// was gone, and there was nothing to say where "back" was. At 300px the
// transcript stays visible down the right-hand side, so the drawer reads as a
// thing on top of your session rather than a different page — and the left
// edge is where the other sessions live in the catalogue's spatial grammar.
//
// Open/close is a small state machine so both the enter and the LEAVE animate:
// `open` is the caller's intent; internally the panel stays mounted through the
// close transition (`visible`) and `entered` flips one frame after mount so the
// CSS `.is-open` transition plays from the closed rest state. Only the panel
// (transform) and the veil (opacity) move — the conversation behind stays
// perfectly still.
//
// Global Settings is NOT rendered here: the foot's ⚙ only signals `onSettings`
// and the parent screen performs a sheet HANDOFF — the drawer fully exits, then
// the Settings sheet slides up in its place (one overlay at a time). `onClosed`
// fires once the leave animation has settled, so the parent can sequence that
// handoff without stacking overlays or racing overlay-history's popstate. The
// Inbox door in the foot takes the SAME handoff.
export function SessionDrawer({
  open,
  step = "list",
  onClose,
  onClosed,
  newResults = [],
  active = [],
  saved = [],
  activeId,
  projects = [],
  onSelect,
  onCreate,
  onSettings,
  onInbox,
  inboxCount = 0,
  inboxVisible = false,
  version = null,
  onCloseSession,
  onReopenSession,
  onDeleteSession,
  groupByProject = false,
  drawerCollapsed = {},
  onGroupByProject,
  onToggleProject,
  panelRef: externalPanelRef,
}) {
  const panelRef = useRef(null);
  const previousFocusRef = useRef(null);
  const closeTimerRef = useRef(null);
  const closeOverlayRef = useRef(null);
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;
  const onClosedRef = useRef(onClosed);
  onClosedRef.current = onClosed;
  const wasOpenRef = useRef(open);
  const [visible, setVisible] = useState(open);
  const [entered, setEntered] = useState(open);
  // The drawer has two screens: the list, and "new session". They swap in place
  // instead of handing off to another overlay — the whole point is that
  // everything about sessions happens inside the one sheet. Which one an open
  // lands on comes from the caller (`step`), because creating a session on a
  // phone is ALWAYS this screen: the empty state and the command palette open
  // the drawer on "new" rather than standing up a second create flow. It resets
  // on every open, so it never reopens mid-task.
  const [view, setView] = useState(step);

  // The screen an open lands on — and a step change while the drawer is
  // already open (the palette handing over to an open drawer) — both come from
  // the caller, without touching the enter/leave state machine below.
  useEffect(() => {
    if (open) setView(step);
  }, [step, open]);

  // Register with the shared overlay-history stack whenever open toggles, so
  // the browser/PWA back gesture closes the drawer instead of navigating away
  // (same contract as Sheet/MobileSheet). The effect cleanup consumes the
  // history entry on every close path, and the returned close() is idempotent.
  useEffect(() => {
    if (!open) return undefined;
    closeOverlayRef.current = openOverlay("session-drawer", () => onCloseRef.current?.());
    return () => {
      closeOverlayRef.current?.();
      closeOverlayRef.current = null;
    };
  }, [open]);

  // Enter/leave state machine driven by `open`. Enter: mount, then flip
  // `entered` on the next frame so the .is-open transition runs. Leave: drop
  // `entered` (the sheet slides back out through the left edge) and unmount
  // after the close duration. Reduced motion snaps both ways.
  useEffect(() => {
    const reduce =
      typeof window !== "undefined" &&
      typeof window.matchMedia === "function" &&
      window.matchMedia("(prefers-reduced-motion: reduce)").matches;
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
      // Fire onClosed only on a real open→close transition, once the drawer
      // has fully dismissed — so the parent can hand off to the Settings sheet
      // or the inbox without stacking it above the outgoing drawer.
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
        }, 260);
      }
    }
    return undefined;
  }, [open]);

  useEffect(() => () => clearTimeout(closeTimerRef.current), []);

  // Escape closes; Tab cycles focus within the panel (wrapping at the edges).
  useEffect(() => {
    if (!open) return;
    const onKeyDown = (e) => {
      if (e.key === "Escape") {
        closeOverlayRef.current?.();
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
  // On close: restore focus to the remembered element (the sessions capsule).
  useEffect(() => {
    if (!open) return;
    previousFocusRef.current = document.activeElement;
    // Focus the dialog itself, NOT its first focusable: that is the search
    // input, and focusing it would throw the soft keyboard up over the list the
    // user just asked to see. Tab from here still enters the trap in order.
    panelRef.current?.focus();
    return () => {
      const toRestore = previousFocusRef.current;
      if (toRestore && typeof toRestore.focus === "function") {
        toRestore.focus();
      }
    };
  }, [open]);

  if (!visible) return null;

  const onVeilClick = (e) => {
    if (e.target === e.currentTarget) {
      closeOverlayRef.current?.();
      onClose?.();
    }
  };

  const setPanelRef = (node) => {
    panelRef.current = node;
    externalPanelRef?.(node);
  };

  return (
    <div class={`sdrawer-veil${entered ? " is-open" : ""}`} onClick={onVeilClick}>
      <div
        class={`sdrawer${entered ? " is-open" : ""}`}
        role="dialog"
        aria-modal="true"
        aria-label="Sessions"
        tabIndex={-1}
        ref={setPanelRef}
      >
        {view === "new" ? (
          <NewSessionView
            projects={projects}
            onBack={() => setView("list")}
            onCreate={(cwd) => onCreate?.(cwd)}
          />
        ) : (
          <Sidebar
            density="phone"
            version={version}
            active={active}
            saved={saved}
            newResults={newResults}
            activeId={activeId}
            onSelectSession={onSelect}
            /* The one door to creating a session on a phone: the drawer's own
               second screen, where the working directory is chosen. It is NOT
               the command palette — on a phone that is a whole other chassis
               carrying its own session list (NewSessionView.jsx:6). */
            onNewSession={() => setView("new")}
            onSettings={onSettings}
            onCloseSession={onCloseSession}
            onReopenSession={onReopenSession}
            onDeleteSession={onDeleteSession}
            groupByProject={groupByProject}
            onGroupByProject={onGroupByProject}
            collapsedProjects={drawerCollapsed}
            onToggleProject={onToggleProject}
            inboxCount={inboxCount}
            inboxVisible={inboxVisible}
            onInbox={onInbox}
          />
        )}
      </div>
    </div>
  );
}
