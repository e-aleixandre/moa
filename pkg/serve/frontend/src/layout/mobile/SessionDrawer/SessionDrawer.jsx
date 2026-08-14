import { useEffect, useLayoutEffect, useRef, useState } from "preact/hooks";
import { Plus, MoreHorizontal, Settings, Search, Check, ChevronRight } from "lucide-preact";
import { copyToClipboard } from "../../../data/util/format.js";
import { SessionRow } from "../../../components/index.js";
import { openOverlay } from "../../../data/overlay-history.js";
import { filterProjectSections, groupProjectSessions, hiddenProjectSavedCount, projectCollapsed, sessionSearchMatch, visibleProjectSessions } from "../../../data/util/project-sessions.js";
import { useMenuKeyboard } from "../../../hooks/useMenuKeyboard.js";
import { NewSessionView } from "./NewSessionView.jsx";
import "./SessionDrawer.css";

const FOCUSABLE_SELECTOR =
  'a[href], button:not([disabled]), textarea:not([disabled]), input:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])';

// DrawerVersion — the build version in the drawer footer, mirroring the desktop
// Spine's SpineVersion: plain "vX.Y.Z" normally, or a link to the latest release
// when the server's update check reports one available. The mobile app had no
// version surface after the redesign; this is it (gap #3 decision: it lives on
// the drawer, next to Settings).
function DrawerVersion({ version }) {
  if (!version?.current) return null;
  // current/latest already arrive v-prefixed from the server (release
  // DisplayVersion / cache), so use them verbatim — don't add another "v".
  const current = version.current;
  if (version.update_available && version.latest) {
    return (
      <a
        class="sdrawer-ver sdrawer-ver-update"
        href="https://github.com/e-aleixandre/moa/releases/latest"
        target="_blank"
        rel="noreferrer"
        title={`Update available: ${version.latest}`}
      >
        {current} ↑ {version.latest}
      </a>
    );
  }
  return (
    <span class="sdrawer-ver" title="moa version">
      {current}
    </span>
  );
}

// SessionCardMenu — the per-card ⋯ overflow (TELEMETRY-SETTINGS-REDESIGN §3.3).
// Session lifecycle (close / reopen / delete) is list management, not a
// conversation setting, so it lives here on the card rather than inside the
// chat view. Close unloads the session (it stays in the list as saved);
// Reopen resumes a saved session; Delete is
// irreversible so it takes a deliberate second
// tap to confirm. Self-contained: owns its open state, click-outside and
// Escape, and stops taps from bubbling to the card's own select handler.
function SessionCardMenu({ session, onClose, onReopen, onDelete }) {
  const [open, setOpen] = useState(false);
  const [confirmingDelete, setConfirmingDelete] = useState(false);
  const [dropUp, setDropUp] = useState(false);
  const ref = useRef(null);
  const actionsRef = useRef(null);
  const triggerRef = useRef(null);
  const { onMenuKeyDown, closeMenu } = useMenuKeyboard(open, setOpen, triggerRef, actionsRef);

  useEffect(() => {
    if (!open) return;
    const onDocDown = (e) => {
      if (ref.current && !ref.current.contains(e.target)) setOpen(false);
    };
    document.addEventListener("mousedown", onDocDown);
    return () => {
      document.removeEventListener("mousedown", onDocDown);
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
        ref={triggerRef}
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
          onKeyDown={onMenuKeyDown}
        >
          {isSaved ? (
            <button type="button" role="menuitem" class="sdcard-action" onClick={() => { closeMenu(); onReopen?.(session.id); }}>
              Reopen session
            </button>
          ) : (
            <button type="button" role="menuitem" class="sdcard-action" onClick={() => { closeMenu(); onClose?.(session.id); }}>
              Close session
            </button>
          )}
          <button type="button" role="menuitem" class="sdcard-action" onClick={() => { copyToClipboard(session.id); closeMenu(); }}>
            Copy session ID
          </button>
          {confirmingDelete ? (
            <button
              type="button"
              role="menuitem"
              class="sdcard-action sdcard-action-danger"
              onClick={() => { closeMenu(); onDelete?.(session.id); }}
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
function SessionDrawerCard({ session, hidePath = false, onSelect, onCloseSession, onReopenSession, onDeleteSession }) {
  const { id, title, state, when, last, needsLabel, path, unseen, origin } = session;
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
        origin={origin}
        brief={brief}
        path={hidePath ? undefined : path}
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

function DrawerGroupMenu({ groupByProject, onChange }) {
  const [open, setOpen] = useState(false);
  const ref = useRef(null);
  const menuRef = useRef(null);
  const triggerRef = useRef(null);
  const [dropUp, setDropUp] = useState(false);
  const { onMenuKeyDown, closeMenu } = useMenuKeyboard(open, setOpen, triggerRef, menuRef);
  useEffect(() => {
    if (!open) return;
    const onDocDown = (e) => { if (ref.current && !ref.current.contains(e.target)) setOpen(false); };
    document.addEventListener("mousedown", onDocDown);
    return () => { document.removeEventListener("mousedown", onDocDown); };
  }, [open]);
  useLayoutEffect(() => {
    if (!open || !ref.current || !menuRef.current) return;
    const drawer = ref.current.closest(".sdrawer");
    setDropUp(menuRef.current.offsetHeight + 8 > drawer.getBoundingClientRect().bottom - ref.current.getBoundingClientRect().bottom);
  }, [open]);
  const choose = (on) => { onChange?.(on); closeMenu(); };
  return <div class="sdrawer-more-wrap" ref={ref}>
    <button type="button" ref={triggerRef} class="sdrawer-more" aria-label="Session grouping" aria-haspopup="menu" aria-expanded={open} onClick={() => setOpen((v) => !v)}>
      <MoreHorizontal size={16} aria-hidden="true" />
    </button>
    {open && <div class={`sdrawer-group-menu${dropUp ? " sdrawer-group-menu--up" : ""}`} role="menu" aria-label="Session grouping" ref={menuRef} onKeyDown={onMenuKeyDown}>
      <span class="sdrawer-group-menu-label">Group sessions</span>
      <button type="button" role="menuitemradio" aria-checked={!groupByProject} onClick={() => choose(false)}><span class="sdrawer-group-menu-tick">{!groupByProject && <Check size={14} />}</span>By recency</button>
      <button type="button" role="menuitemradio" aria-checked={groupByProject} onClick={() => choose(true)}><span class="sdrawer-group-menu-tick">{groupByProject && <Check size={14} />}</span>By folder</button>
    </div>}
  </div>;
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
  step = "list",
  onClose,
  onClosed,
  newResults = [],
  active = [],
  saved = [],
  activeCount = 0,
  savedCount = 0,
  projects = [],
  onSelect,
  onCreate,
  onSettings,
  version = null,
  onCloseSession,
  onReopenSession,
  onDeleteSession,
  groupByProject = false,
  drawerCollapsed = {},
  onGroupByProject,
  onToggleProject,
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
  // title. Which one an open lands on comes from the caller (`step`), because
  // creating a session on a phone is ALWAYS this screen: the empty state and
  // the command palette open the drawer on "new" rather than standing up a
  // second create flow. Both reset on every open, so it never reopens mid-task.
  const [view, setView] = useState(step);
  const [query, setQuery] = useState("");
  const [expandedProjects, setExpandedProjects] = useState(() => new Set());
  const [showAllNew, setShowAllNew] = useState(false);

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

  // Search filters the list in place — no second surface, no second list.
  // Session search intentionally uses word substrings rather than the command
  // palette's subsequence matcher; long session titles otherwise match noise.
  const q = query.trim();
  const hit = (s) => sessionSearchMatch(q, s);
  const shownNew = newResults.filter(hit);
  const shownActive = active.filter(hit);
  const shownSaved = saved.filter(hit);
  const hitCount = shownNew.length + shownActive.length + shownSaved.length;
  const projectSections = filterProjectSections(groupProjectSessions([...active, ...saved]), query);

  const card = (s, hidePath = false) => (
    <SessionDrawerCard
      key={s.id}
      session={s}
      hidePath={hidePath}
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
              <DrawerGroupMenu groupByProject={groupByProject} onChange={onGroupByProject} />
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
              {shownNew.length > 0 && <>
                <span class="sdrawer-group sdrawer-group-new">New results · {shownNew.length}</span>
                {shownNew.slice(0, showAllNew ? undefined : 3).map((s) => card(s))}
                {!showAllNew && shownNew.length > 3 && <button type="button" class="sdrawer-show-all" onClick={() => setShowAllNew(true)}>Show all {shownNew.length} new results</button>}
              </>}
              {groupByProject ? projectSections.map((section) => {
                const collapsed = projectCollapsed(section, drawerCollapsed, !!q);
                const expanded = expandedProjects.has(section.key);
                const shownSessions = visibleProjectSessions(section, expanded, !!q);
                const hiddenSaved = hiddenProjectSavedCount(section, expanded, !!q);
                const needs = section.attention === "permission" ? `${section.label}, ${section.openCount} open, ${section.attentionCount} needs permission` : section.attention === "error" ? `${section.label}, ${section.openCount} open, ${section.attentionCount} has an error` : `${section.label}, ${section.openCount} open${section.savedCount ? `, ${section.savedCount} saved` : ""}`;
                return <section class={`sdrawer-project${collapsed ? "" : " is-open"}`} key={section.key}>
                  <button type="button" class="sdrawer-project-head" aria-expanded={!collapsed} aria-label={needs} onClick={() => onToggleProject?.(section.key, !collapsed)}>
                    <span class="sdrawer-project-chevron"><ChevronRight size={14} aria-hidden="true" /></span>
                    <span class="sdrawer-project-id"><span class="sdrawer-project-name">{section.label}{section.attention && <span class={`state-dot sdrawer-project-attention ${section.attention}`} aria-hidden="true" />}</span>{section.path && <span class="sdrawer-project-path">{section.path}</span>}</span>
                    <span class="sdrawer-project-count">{section.openCount} open{section.savedCount ? ` · ${section.savedCount} saved` : ""}</span>
                  </button>
                  {!collapsed && <div class="sdrawer-project-cards">
                    {shownSessions.map((s) => card(s, true))}
                    {hiddenSaved > 0 && <button type="button" class="sdrawer-show-all" onClick={() => setExpandedProjects((keys) => new Set(keys).add(section.key))}>Show all {hiddenSaved} saved</button>}
                  </div>}
                </section>;
              }) : <>{shownActive.length > 0 && <span class="sdrawer-group">Active</span>}{shownActive.map((s) => card(s))}{shownSaved.length > 0 && <span class="sdrawer-group">Saved</span>}{shownSaved.map((s) => card(s))}</>}
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
              <DrawerVersion version={version} />
            </div>
          </>
        )}
      </div>
    </div>
  );
}
