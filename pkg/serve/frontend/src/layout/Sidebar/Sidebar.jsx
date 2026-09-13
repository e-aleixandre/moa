import { useMemo, useState } from "preact/hooks";
import { InboxView } from "../../components/InboxView/InboxView.jsx";
import { SessionCardMenu } from "../../components/SessionCardMenu/SessionCardMenu.jsx";
import { SessionRow, Dot } from "../../components/SessionRow/SessionRow.jsx";
import { formatShortcut } from "../../data/util/shortcut.js";
import {
  attentionKind,
  groupProjectSessions,
  hiddenProjectSavedCount,
  partitionByAttention,
  previewSavedSessions,
  projectCollapsed,
  visibleProjectSessions,
} from "../../data/util/project-sessions.js";
import { projectMonogram } from "../../data/util/format.js";
import "./Sidebar.css";

// Sidebar — the other sessions. Markup and CSS are the catalogue's
// (catalog/zones-lab.jsx `Sidebar` / `SessionList` / `Monogram`, zones-lab.css
// the `.zl-side-head` / `.zl-list` / `.zl-side-new` / `.zl-side-foot` block),
// MOVED here rather than imitated: the classes travelled with the rules, so
// the column IS the accepted design instead of a translation of it. The
// catalogue imports this component now, which is what makes one definition
// rather than two.
//
// What is NOT the catalogue's is everything the prototype never had, grafted
// on top: the real roster, search that FILTERS, ⌘K that JUMPS, the inbox
// taking over the list, session lifecycle menus, the saved-tail cap, and the
// folder accordion. The densities differ in presentation only: 272px docked
// on the desktop, a 300px sheet on the phone (the chassis is SessionDrawer);
// the ⌘K keycap only where there is a keyboard.
//
// Four pieces, top to bottom, the catalogue's own:
//   1. head — the wordmark and the search field
//   2. list — the groups and their rows (or the inbox, on the desktop)
//   3. new  — one labelled action, at the bottom, where the thumb is
//   4. foot — the inbox, the version and settings: the things about the APP

const ORDERS = [
  ["recent", "Recent", "Sort by recent"],
  ["project", "By project", "Group by project"],
];

const ATTENTION_RANK = { permission: 0, error: 1, unseen: 2 };

function PlusIcon() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true">
      <path d="M8 3.5v9M3.5 8h9" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" />
    </svg>
  );
}

function SearchIcon() {
  return (
    <svg class="zl-search-ico" viewBox="0 0 16 16" aria-hidden="true">
      <circle cx="7" cy="7" r="4.5" fill="none" stroke="currentColor" stroke-width="1.6" />
      <path d="M10.5 10.5L14 14" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" />
    </svg>
  );
}

function InboxIcon() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true">
      <path d="M2 9.5V12a1 1 0 0 0 1 1h10a1 1 0 0 0 1-1V9.5M2 9.5h3.2l.8 1.5h4l.8-1.5H14M2 9.5l1.6-5.2A1 1 0 0 1 4.6 3.5h6.8a1 1 0 0 1 1 .8L14 9.5" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round" />
    </svg>
  );
}

function GearIcon() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true">
      {/* Teeth, attached to the body. The previous drawing put eight straight
          rays around a ring with a gap between, which is how every interface
          in the world draws brightness -- it read as a sun, and it opened
          settings. */}
      <circle cx="8" cy="8" r="2.6" fill="none" stroke="currentColor" stroke-width="1.5" />
      <path d="M8 1.3a6.7 6.7 0 0 1 2.05.32l.3 1.52a5.2 5.2 0 0 1 1.19.69l1.46-.5a6.7 6.7 0 0 1 1.27 2.2l-1.16 1.03a5.2 5.2 0 0 1 0 1.38l1.16 1.03a6.7 6.7 0 0 1-1.27 2.2l-1.46-.5a5.2 5.2 0 0 1-1.19.69l-.3 1.52a6.7 6.7 0 0 1-4.1 0l-.3-1.52a5.2 5.2 0 0 1-1.19-.69l-1.46.5a6.7 6.7 0 0 1-1.27-2.2l1.16-1.03a5.2 5.2 0 0 1 0-1.38L1.71 5.53a6.7 6.7 0 0 1 1.27-2.2l1.46.5a5.2 5.2 0 0 1 1.19-.69l.3-1.52A6.7 6.7 0 0 1 8 1.3z" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round" />
    </svg>
  );
}

/* The two ways the list can be arranged. A clock for time, stacked folders
   for grouping -- the distinction has to survive at 14px with no label. */
function ByRecentIcon() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true">
      <circle cx="8" cy="8" r="5.6" fill="none" stroke="currentColor" stroke-width="1.5" />
      <path d="M8 4.7V8l2.2 1.5" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
    </svg>
  );
}

function ByProjectIcon() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true">
      <path d="M1.9 5.1V3.4a.9.9 0 0 1 .9-.9h2.4l1.2 1.4h4.7a.9.9 0 0 1 .9.9v.3" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" />
      <rect x="1.9" y="6.2" width="12.2" height="7.3" rx="1" fill="none" stroke="currentColor" stroke-width="1.4" />
      <path d="M4.6 9.1h6.8M4.6 11.2h4.3" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" />
    </svg>
  );
}

function ChevronIcon() {
  return (
    <svg class="zl-proj-chev" viewBox="0 0 16 16" aria-hidden="true">
      <path d="M6 3.5L10.5 8 6 12.5" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" />
    </svg>
  );
}

function SidebarVersion({ version }) {
  if (!version?.current) return null;
  // current/latest already arrive v-prefixed from the server (release
  // DisplayVersion / cache), so use them verbatim — don't add another "v".
  const current = version.current;
  if (version.update_available && version.latest) {
    return (
      <a
        class="zl-ver zl-data is-update"
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
    <span class="zl-ver zl-data" title="moa version">
      {current}
    </span>
  );
}

function sectionWorst(section) {
  const kinds = section.sessions.map(attentionKind).filter(Boolean);
  if (!kinds.length) return null;
  return kinds.sort((a, b) => ATTENTION_RANK[a] - ATTENTION_RANK[b])[0];
}

export function Sidebar({
  density = "desktop",
  version = null,
  // The roster. ONE row shape for both densities (sessions.js / chrome.js both
  // emit it): id, title, state, when, brief, briefTone, mono, path, cwd.
  active = [],
  saved = [],
  newResults = [],
  activeId,
  onSelectSession,
  onNewSession,
  // The palette. Desktop only — it is what the ⌘K keycap opens.
  onSearch,
  onSettings,
  onCloseSession,
  onReopenSession,
  onDeleteSession,
  groupByProject = false,
  onGroupByProject,
  collapsedProjects = {},
  onToggleProject,
  // wake-on-event: on the desktop the inbox is the sidebar's OTHER list and
  // takes over the same slot, so an event arriving never pushes the sessions
  // down. On the phone it is a handoff: the caller closes this and opens the
  // inbox screen, so `inboxOpen` stays false and only `onInbox` is used.
  inbox = [],
  inboxHealth,
  inboxOpen = false,
  inboxCount = 0,
  inboxVisible = false,
  onInbox,
  onRetryInbox,
  onRouteEvent,
  onNewSessionForEvent,
  onDismissEvent,
  onDismissEventSource,
  // Catalogue scenes at 300px are the drawer WIDTH with pointer sizing; the
  // phone's 44px floor is a density, not a width. `jump` defaults to "there
  // is a keyboard" (!phone) so production does not have to pass it.
  jump,
}) {
  const phone = density === "phone";
  const showJump = jump ?? !phone;
  const [expandedProjects, setExpandedProjects] = useState(() => new Set());
  const [showAllSaved, setShowAllSaved] = useState(false);
  const hasMenu = !!(onCloseSession || onReopenSession || onDeleteSession);

  // Nothing filters this list any more -- the palette is where you look for a
  // session -- so what is left is the roster as it stands.
  const { shownActive, shownSaved, hitCount, projectSections } = useMemo(() => {
    // The phone's selector lifts unread answers out of `active` into their own
    // `newResults` list (chrome.js:55). They go straight back in here: unread
    // IS one of the three ways a session waits for you, so it belongs in Needs
    // attention with the other two.
    const allActive = [...newResults, ...active];
    return {
      shownActive: allActive,
      shownSaved: saved,
      hitCount: allActive.length + saved.length,
      projectSections: groupProjectSessions([...allActive, ...saved]),
    };
  }, [newResults, active, saved]);

  /* Saved sessions are deliberately not offered to the attention split: a saved
     session is parked on purpose, so it belongs under Saved even if it ended
     badly. */
  const { needs: needsAttention, rest: restActive } = partitionByAttention(shownActive);
  const savedPreview = previewSavedSessions(shownSaved, { expanded: showAllSaved, searching: false });
  // The catalogue draws ⌘K. The binding still accepts both modifiers
  // (formatShortcut is how the palette NAMES the same shortcut); the keycap
  // is the accepted drawing, not a platform translation of it.

  const row = (s, hidePath = false) => (
    <div class={`zl-session${hasMenu ? " is-menu" : ""}`} key={s.id}>
      <SessionRow
        title={s.title}
        state={s.state || (s.saved ? "saved" : "idle")}
        active={s.active ?? s.id === activeId}
        unseen={s.unseen}
        when={s.when || s.meta}
        brief={s.brief}
        briefTone={s.briefTone}
        mono={s.mono || projectMonogram(s.cwd)}
        path={s.brief ? undefined : s.path}
        pane={s.pane}
        origin={s.origin}
        onClick={() => onSelectSession?.(s.id)}
      />
      {hasMenu && (
        <SessionCardMenu
          session={s}
          onClose={onCloseSession}
          onReopen={onReopenSession}
          onDelete={onDeleteSession}
          scrollContainerSelector=".zl-list"
        />
      )}
    </div>
  );

  const jumpCap = showJump && (
    onSearch ? (
      <button
        type="button"
        class="zl-kbd zl-data"
        onClick={onSearch}
        aria-label={`Jump to session ${formatShortcut("K", { mod: true })}`}
        title={`Jump to session ${formatShortcut("K", { mod: true })}`}
      >
        ⌘K
      </button>
    ) : (
      <kbd class="zl-kbd zl-data">⌘K</kbd>
    )
  );

  return (
    <aside class={`zl-side-body${phone ? " is-phone" : ""}`}>
      {!inboxOpen && (
        <div class="zl-side-head">
          <span class="zl-side-title">moa</span>
          {/* One search, not two. This used to be a field that FILTERED this
              list, sitting beside a keycap that opened the palette — two ways
              to look for a session in a 272px column, and the weaker one held
              the better seat. The palette finds sessions, goes to projects and
              runs actions; filtering only ever shortened what was already in
              front of you, and still left you pointing at a row.

              So the head keeps the door and not the field. */}
          {onSearch ? (
            <button type="button" class="zl-search is-door" onClick={onSearch} aria-label={`Search ${formatShortcut("K", { mod: true })}`}>
              <SearchIcon />
              <span class="zl-search-txt">Search</span>
              {jumpCap}
            </button>
          ) : (
            <span class="zl-search is-door is-inert">
              <SearchIcon />
              <span class="zl-search-txt">Search</span>
            </span>
          )}
          {/* Two icons at the end of the head, not a band across the list.
              A control for the list's arrangement does not deserve a line of
              its own -- the sidebar is narrow and every row of it is worth
              more than a switch touched once a month. The labels survive as
              the tooltip and the accessible name. */}
          <div class="zl-view" role="radiogroup" aria-label="Session order">
            {ORDERS.map(([id, label, hint]) => {
              const on = (id === "project") === !!groupByProject;
              return (
                <button
                  type="button"
                  role="radio"
                  aria-checked={on}
                  aria-label={hint}
                  title={label}
                  class={`zl-view-b${on ? " is-on" : ""}`}
                  onClick={() => onGroupByProject?.(id === "project")}
                  key={id}
                >
                  {id === "project" ? <ByProjectIcon /> : <ByRecentIcon />}
                </button>
              );
            })}
          </div>
        </div>
      )}

      {inboxOpen ? (
        <div class="zl-list is-inbox">
          <InboxView
            cards={inbox}
            health={inboxHealth}
            onRetry={onRetryInbox}
            onSend={onRouteEvent}
            onNewSession={onNewSessionForEvent}
            onIgnore={onDismissEvent}
            onIgnoreSource={onDismissEventSource}
            onOpenSession={onSelectSession}
            onBack={onInbox}
          />
        </div>
      ) : (
        <div class="zl-list">

          {hitCount === 0 && (
            <button type="button" class="zl-empty-new" onClick={onNewSession}>
              + New session
            </button>
          )}

          {groupByProject ? projectSections.map((section) => {
            const canToggle = typeof onToggleProject === "function";
            const collapsed = canToggle ? projectCollapsed(section, collapsedProjects, false) : false;
            const expanded = expandedProjects.has(section.key);
            const shownSessions = visibleProjectSessions(section, expanded, false);
            const hiddenSaved = hiddenProjectSavedCount(section, expanded, false);
            const mono = projectMonogram(section.key);
            const worst = sectionWorst(section);
            const name = mono?.name || section.label;
            const label = section.attention === "permission"
              ? `${section.label}, ${section.openCount} open, ${section.attentionCount} needs permission`
              : section.attention === "error"
                ? `${section.label}, ${section.openCount} open, ${section.attentionCount} has an error`
                : `${section.label}, ${section.openCount} open${section.savedCount ? `, ${section.savedCount} saved` : ""}`;
            const heading = (
              <>
                {canToggle && <ChevronIcon />}
                {mono && (
                  <span class="zl-mono" style={`--h:${mono.hue}`} aria-hidden="true">
                    {mono.text}
                  </span>
                )}
                <span>{name}</span>
                {worst && <Dot state={worst} />}
                <span class="zl-proj-path zl-data">{section.path}</span>
                <span class="zl-group-n zl-data">{section.sessions.length}</span>
              </>
            );
            return (
              <div class={`zl-proj${collapsed ? "" : " is-open"}`} key={section.key}>
                {canToggle ? (
                  <button
                    type="button"
                    class="zl-group is-proj"
                    aria-expanded={!collapsed}
                    aria-label={label}
                    onClick={() => onToggleProject(section.key, !collapsed)}
                  >
                    {heading}
                  </button>
                ) : (
                  <div class="zl-group is-proj">{heading}</div>
                )}
                {!collapsed && (
                  <>
                    {shownSessions.map((s) => row(s, true))}
                    {hiddenSaved > 0 && (
                      <button
                        type="button"
                        class="zl-show-all"
                        onClick={() => setExpandedProjects((keys) => new Set(keys).add(section.key))}
                      >
                        Show all {hiddenSaved} saved
                      </button>
                    )}
                  </>
                )}
              </div>
            );
          }) : (
            <>
              {/* Needs attention comes first, and belongs to this view only:
                  grouped by folder, a session listed here AND inside its folder
                  is the same row printed twice, so there the alarm rides on the
                  folder heading instead. */}
              {needsAttention.length > 0 && (
                <>
                  <div class="zl-group is-attn">
                    <span>Needs attention</span>
                    <span class="zl-group-n zl-data">{needsAttention.length}</span>
                  </div>
                  {needsAttention.map((s) => row(s))}
                </>
              )}
              {restActive.length > 0 && (
                <>
                  <div class="zl-group"><span>Active</span><span class="zl-group-n zl-data">{restActive.length}</span></div>
                  {restActive.map((s) => row(s))}
                </>
              )}
              {shownSaved.length > 0 && (
                <>
                  <div class="zl-group"><span>Saved</span><span class="zl-group-n zl-data">{shownSaved.length}</span></div>
                  {savedPreview.visible.map((s) => row(s))}
                  {savedPreview.hidden > 0 && (
                    <button type="button" class="zl-show-all" onClick={() => setShowAllSaved(true)}>
                      Show all {shownSaved.length} saved
                    </button>
                  )}
                </>
              )}
            </>
          )}
        </div>
      )}

      {/* New anchors the bottom, where the thumb is. It is the one action, so
          it gets the width — and the word, which a 28px "+" in the head never
          had room for. Hidden while the inbox has the list: filing is not
          starting a session. */}
      {!inboxOpen && (
        <button type="button" class="zl-side-new" onClick={onNewSession}>
          <PlusIcon />New session
        </button>
      )}

      {/* The foot is about the APP, not about a session: the inbox, the build,
          and the global settings. Same place both densities kept settings. */}
      <div class="zl-side-foot">
        {/* wake-on-event: the door appears once anything has ever arrived — a
            permanent icon for someone with no hooks configured would be chrome
            that never does anything. It also appears when the inbox could NOT
            be read: with no list there is no way to know that nothing arrived,
            and hiding the door would hide the failure too. */}
        {inboxVisible && (
          <button
            type="button"
            class={`zl-inbox${inboxOpen ? " is-on" : ""}`}
            aria-pressed={inboxOpen}
            aria-label={inboxCount > 0 ? `Inbox, ${inboxCount} waiting` : "Inbox"}
            onClick={onInbox}
          >
            <InboxIcon />
            Inbox
            {inboxCount > 0 && <span class="zl-inbox-n zl-data">{inboxCount > 9 ? "9+" : inboxCount}</span>}
          </button>
        )}
        {/* Version and settings are one group, and the foot used to read
            action / fact / action -- with the version wedged between the two
            buttons, belonging to neither. Both of these are about the app
            rather than the session, so they sit together and the inbox keeps
            the other end to itself. */}
        <div class="zl-side-app">
          <SidebarVersion version={version} />
          <button type="button" class="zl-gear" aria-label="Settings" onClick={onSettings}>
            <GearIcon />
          </button>
        </div>
      </div>
    </aside>
  );
}
