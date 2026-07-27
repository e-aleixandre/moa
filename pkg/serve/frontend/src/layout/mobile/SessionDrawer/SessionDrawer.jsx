import { useEffect, useLayoutEffect, useRef, useState } from "preact/hooks";
import { Plus, MoreHorizontal, Settings, Search } from "lucide-preact";
import { SessionRow } from "../../../components/index.js";
import { openOverlay } from "../../../data/overlay-history.js";
import { fuzzyMatch } from "../../../data/fuzzy.js";
import { NewSessionView } from "./NewSessionView.jsx";
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

// SessionDrawerCard — one session in the list. The card itself is the SHARED
// SessionRow in its `card` variant — the very same component the desktop Spine
// lists sessions with, so the two surfaces can't drift apart. The mobile-only
// part is the ⋯ overflow laid over its top-right corner; SessionRow's own
// `onClose` X is deliberately not used, because lifecycle here is a menu
// (close/reopen/delete), not a single dismiss.
function SessionDrawerCard({ session, onSelect, onCloseSession, onReopenSession, onDeleteSession }) {
  const { id, title, state, when, last, needsLabel, path, unseen } = session;
  const brief = last
    ? needsLabel
      ? <><b class="sdcard-needs-label">{needsLabel} </b>{last}</>
      : last
    : null;
  return (
    <div class="sdcard-slot">
      <SessionRow
        variant="card"
        title={title}
        state={state}
        active={session.active}
        unseen={unseen}
        when={when}
        brief={brief}
        path={path}
        onClick={() => onSelect?.(id)}
      />
      <SessionCardMenu
        session={session}
        onClose={onCloseSession}
        onReopen={onReopenSession}
        onDelete={onDeleteSession}
      />
    </div>
  );
}

// SessionDrawer — the mobile session list, unfurled from the title chip it is
// anchored under (MobileTitleChip). It is a DROPDOWN, not a bottom sheet: the
// title is the door, so the list has to visibly hang from it, and the composer
// stays put underneath the veil where the eye left it.
//
// Its structure deliberately mirrors the desktop Spine — search, then active
// sessions, then saved — so the same job looks like the same job on both
// frontends. What differs is only what the form factor forces: the desktop
// keeps the list permanently in a column, mobile borrows the screen for it.
//
// Open/close is a small state machine so both the enter and the LEAVE animate
// (MOBILE-POLISH-SPEC §5): `open` is the caller's intent; internally we keep the
// panel mounted through the close transition (`visible`) and toggle `entered`
// one frame after mount so the CSS `.is-open` transition plays from the closed
// rest state. Only the panel (transform/opacity) and veil (opacity) move — the
// conversation behind stays perfectly still.
//
// Global Settings is NOT rendered here: the footer's ⚙ button only signals
// `onSettings` and the parent screen performs a sheet HANDOFF — the drawer
// fully exits, then the global Settings bottom-sheet slides up in its place
// (one overlay at a time, the approved mock's closeAll→open grammar). The
// `onClosed` callback fires once the leave animation has settled (mirroring
// MobileSheet.onClosed) so the parent can sequence that handoff without
// stacking overlays or racing the shared overlay-history back()/popstate.
export function SessionDrawer({
  open,
  onClose,
  onClosed,
  active = [],
  saved = [],
  activeCount = 0,
  savedCount = 0,
  projects = [],
  onSelect,
  onCreate,
  onSettings,
  onCloseSession,
  onReopenSession,
  onDeleteSession,
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
  // instead of handing off to another overlay — the whole point of the dropdown
  // is that everything about sessions happens inside the thing hanging from the
  // title. Both reset on every open, so the drawer never reopens mid-task.
  const [view, setView] = useState("list");
  const [query, setQuery] = useState("");

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
  // `entered` (panel folds back up into the chip) and unmount after the close
  // duration. Reduced motion snaps both ways.
  useEffect(() => {
    const reduce =
      typeof window !== "undefined" &&
      typeof window.matchMedia === "function" &&
      window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    if (open) {
      wasOpenRef.current = true;
      clearTimeout(closeTimerRef.current);
      setVisible(true);
      setView("list");
      setQuery("");
      if (reduce) {
        setEntered(true);
      } else {
        const raf = requestAnimationFrame(() => setEntered(true));
        return () => cancelAnimationFrame(raf);
      }
    } else {
      setEntered(false);
      // Fire onClosed only on a real open→close transition, once the drawer
      // has fully dismissed — so the parent can hand off to the global
      // Settings sheet without stacking it above the outgoing drawer.
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
        }, 180);
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
  // On close: restore focus to the remembered element (the title chip).
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

  // Search filters the list in place — no second surface, no second list. Fuzzy
  // over title and path, the same two fields the palette matches on.
  const q = query.trim().toLowerCase();
  const hit = (s) => !q || fuzzyMatch(q, `${s.title || ""} ${s.path || ""}`.toLowerCase());
  const shownActive = active.filter(hit);
  const shownSaved = saved.filter(hit);
  const hitCount = shownActive.length + shownSaved.length;

  const card = (s) => (
    <SessionDrawerCard
      key={s.id}
      session={s}
      onSelect={onSelect}
      onCloseSession={onCloseSession}
      onReopenSession={onReopenSession}
      onDeleteSession={onDeleteSession}
    />
  );

  return (
    <div class={`sdrawer-veil${entered ? " is-open" : ""}`} onClick={onVeilClick}>
      <div
        class={`sdrawer${entered ? " is-open" : ""}`}
        role="dialog"
        aria-modal="true"
        aria-label="Sessions"
        tabIndex={-1}
        ref={panelRef}
      >
        {view === "new" ? (
          <NewSessionView
            projects={projects}
            onBack={() => setView("list")}
            onCreate={(cwd) => onCreate?.(cwd)}
          />
        ) : (
          <>
            <div class="sdrawer-head">
              <h2>Sessions</h2>
              <span class="sdrawer-count">
                {q
                  ? `${hitCount} ${hitCount === 1 ? "match" : "matches"}`
                  : `${activeCount} active · ${savedCount} saved`}
              </span>
              <button
                type="button"
                class="sdrawer-new"
                aria-label="New session"
                onClick={() => setView("new")}
              >
                <Plus size={15} aria-hidden="true" />
              </button>
            </div>

            <div class="sdrawer-search">
              <Search size={15} aria-hidden="true" />
              <input
                type="text"
                aria-label="Search sessions"
                placeholder="Search sessions…"
                autocomplete="off"
                autocapitalize="off"
                spellcheck={false}
                value={query}
                onInput={(e) => setQuery(e.target.value)}
              />
            </div>

            <div class="sdrawer-list">
              {shownActive.map(card)}
              {shownSaved.length > 0 && <span class="sdrawer-group">Saved</span>}
              {shownSaved.map(card)}
              {q && hitCount === 0 && (
                <span class="sdrawer-note">No session matches “{query}”</span>
              )}
            </div>

            <div class="sdrawer-foot">
              <button
                type="button"
                class="sdrawer-settings"
                onClick={() => onSettings?.()}
                aria-haspopup="dialog"
              >
                <Settings size={14} aria-hidden="true" /> Settings
              </button>
            </div>
          </>
        )}
      </div>
    </div>
  );
}
