import { useMemo, useState } from "preact/hooks";
import { useFlip } from "../../hooks/useFlip.js";
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
import { projectName } from "../../data/util/format.js";
import "./Sidebar.css";

// Sidebar — the other sessions. Markup and CSS are the catalogue's
// (catalog/zones-lab.jsx `Sidebar` / `SessionList` / `Monogram`, zones-lab.css
// the `.zl-side-head` / `.zl-list` / `.zl-side-add` / `.zl-side-foot` block),
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
      {/* Eight teeth, generated rather than drawn by hand: the first attempt
          chained arcs by eye and they did not meet at the bottom. These are
          two radii and one angle stepped eight times, so every tooth is the
          same tooth and the outline closes. */}
      <circle cx="8" cy="8" r="2.6" fill="none" stroke="currentColor" stroke-width="1.5" />
      <path d="M7.01 1.07 L8.99 1.07 L9.33 2.82 L10.72 3.40 L12.20 2.40 L13.60 3.80 L12.60 5.28 L13.18 6.67 L14.93 7.01 L14.93 8.99 L13.18 9.33 L12.60 10.72 L13.60 12.20 L12.20 13.60 L10.72 12.60 L9.33 13.18 L8.99 14.93 L7.01 14.93 L6.67 13.18 L5.28 12.60 L3.80 13.60 L2.40 12.20 L3.40 10.72 L2.82 9.33 L1.07 8.99 L1.07 7.01 L2.82 6.67 L3.40 5.28 L2.40 3.80 L3.80 2.40 L5.28 3.40 L6.67 2.82 Z" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round" />
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
  // emit it): id, title, state, when, brief, briefTone, path, cwd.
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
}) {
  const phone = density === "phone";
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

  // The order of the rows as rendered: a session that answers moves from
  // Active to Needs attention, a saved one rises to the top of Saved. useFlip
  // carries each row from its old slot to the new one instead of redrawing
  // it there (motion language, rule 4). Keyed by ids only, as one string: a
  // title or a timestamp changing must not re-measure a list that did not
  // move, and the partitions above are fresh arrays every render.
  const order = (groupByProject
    ? projectSections.flatMap((section) => section.sessions.map((s) => s.id))
    : [...needsAttention, ...restActive, ...savedPreview.visible].map((s) => s.id)
  ).join("\n");
  const listRef = useFlip([order, inboxOpen, collapsedProjects, expandedProjects, showAllSaved]);

  const row = (s, underProject = false) => (
    <div class={`zl-session${hasMenu ? " is-menu" : ""}`} key={s.id} data-flip={s.id}>
      <SessionRow
        title={s.title}
        state={s.state || (s.saved ? "saved" : "idle")}
        active={s.active ?? s.id === activeId}
        unseen={s.unseen}
        when={s.when || s.meta}
        brief={s.brief}
        briefTone={s.briefTone}
        path={s.brief ? undefined : s.path}
        /* A row with a brief has no path, so it names its project at the end
           of that line -- unless the heading above already did. */
        project={s.brief && !underProject ? projectName(s.cwd) || undefined : undefined}
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

              So the head keeps the door and not the field. The door is an icon
              and not a labelled box, because the label never fitted: measured
              in the head, a box wanting 10+16+8+45("Search")+8+24(⌘K)+8 = 119px
              was handed 73, so the keycap sat on top of the word and the head
              read "S⌘Kch". Dropping the cap still needs 87. The shortcut and
              the name live in the tooltip and the accessible name, where they
              cost no width at all. */}
          {onSearch ? (
            <button
              type="button"
              class="zl-search is-door"
              onClick={onSearch}
              aria-label={`Search ${formatShortcut("K", { mod: true })}`}
              title={`Search ${formatShortcut("K", { mod: true })}`}
            >
              <SearchIcon />
            </button>
          ) : (
            <span class="zl-search is-door is-inert" title="Search">
              <SearchIcon />
            </span>
          )}
          {/* Two icons at the end of the head, not a band across the list.
              A control for the list's arrangement does not deserve a line of
              its own -- the sidebar is narrow and every row of it is worth
              more than a switch touched once a month. The labels survive as
              the tooltip and the accessible name. */}
          {/* New is a "+" in the head rather than a filled bar across the
              bottom. Starting a session is frequent but it is not the reason
              the sidebar exists -- the list is -- and a full-width accent
              button spent a whole row, plus the eye's first stop, on an action
              the palette also offers. As an icon it sits with the other things
              you DO here, and the list gets the room back. */}
          {!inboxOpen && (
            <button type="button" class="zl-side-add" onClick={onNewSession} title="New session" aria-label="New session">
              <PlusIcon />
            </button>
          )}
          <div class={`zl-view${groupByProject ? " is-project" : ""}`} role="radiogroup" aria-label="Session order">
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
        <div class="zl-list" ref={listRef}>

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
            const worst = sectionWorst(section);
            // The heading says the project's name, not its last two segments:
            // "moa" over ~/dev/moa/main, when the path beside it already has
            // the branch.
            const name = projectName(section.key) || section.label;
            const label = section.attention === "permission"
              ? `${section.label}, ${section.openCount} open, ${section.attentionCount} needs permission`
              : section.attention === "error"
                ? `${section.label}, ${section.openCount} open, ${section.attentionCount} has an error`
                : `${section.label}, ${section.openCount} open${section.savedCount ? `, ${section.savedCount} saved` : ""}`;
            const heading = (
              <>
                {canToggle && <ChevronIcon />}
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
