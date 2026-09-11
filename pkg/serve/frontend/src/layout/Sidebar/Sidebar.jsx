import { Plus, Search, Settings, Inbox, ChevronRight } from "lucide-preact";
import { useMemo, useState } from "preact/hooks";
import { Field, IconButton, Kbd } from "../../primitives/index.js";
import { InboxView, SessionCardMenu, SessionRow } from "../../components/index.js"; // wake-on-event: InboxView
import { formatShortcut } from "../../data/util/shortcut.js";
import {
  filterProjectSections,
  groupProjectSessions,
  hiddenProjectSavedCount,
  partitionByAttention,
  previewSavedSessions,
  projectCollapsed,
  sessionSearchMatch,
  visibleProjectSessions,
} from "../../data/util/project-sessions.js";
import { projectMonogram } from "../../data/util/format.js";
import "./Sidebar.css";

// Sidebar — the other sessions. ONE component, both densities.
//
// It used to be two: `Spine` (a permanent desktop column) and `SessionDrawer`
// (a phone dropdown that covered the screen, with its own head, its own search
// field and its own group labels). They were kept in step by hand, and drifted:
// the phone had no "Needs attention" group at all, the desktop had no filter,
// and the two spelled the same list with two different sets of class names.
//
// The catalogue draws it as one piece (zones-lab.jsx:185 — "The left drawer
// body, shared by both densities"), and that is what this is. The densities
// differ in exactly two things, both of them presentation:
//   · the frame — 272px docked on the desktop, a 300px sheet that slides in
//     from the left edge on the phone (the chassis is SessionDrawer, which now
//     owns nothing but the veil, the focus trap and the "new session" screen);
//   · the ⌘K keycap, which only means something where there is a keyboard.
//
// Four pieces, top to bottom, the catalogue's own:
//   1. head — the wordmark and the search field
//   2. list — the groups and their rows (or the inbox, on the desktop)
//   3. new  — one labelled action, at the bottom, where the thumb is
//   4. foot — the inbox, the version and settings: the things about the APP
//
// SEARCH vs ⌘K. The field FILTERS this list; ⌘K JUMPS to a session from
// anywhere. Two jobs, so two controls, and the keycap inside the field is a
// real button to the palette rather than a decoration.

// The list's own order. Both options visible, per the catalogue: a "⋯" menu
// (what both surfaces used before) made a mode you cannot see you are in, and
// there are only two of them. The second is worded "By folder" and not "By
// project" on purpose — data/drawer.js:36 argues it, and it is still true: this
// groups by cwd, and memory's notion of a project is wider.
const ORDERS = [
  ["recent", "Recent"],
  ["folder", "By folder"],
];

function SidebarVersion({ version }) {
  if (!version?.current) return null;
  // current/latest already arrive v-prefixed from the server (release
  // DisplayVersion / cache), so use them verbatim — don't add another "v".
  const current = version.current;
  if (version.update_available && version.latest) {
    return (
      <a
        class="sidebar-ver sidebar-ver-update"
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
    <span class="sidebar-ver" title="moa version">
      {current}
    </span>
  );
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
}) {
  const phone = density === "phone";
  const [query, setQuery] = useState("");
  const [expandedProjects, setExpandedProjects] = useState(() => new Set());
  const [showAllSaved, setShowAllSaved] = useState(false);

  // Session search is word-substring, not the palette's subsequence matcher:
  // session titles are sentence-length and a few hundred rows turn ordinary
  // words into noise (project-sessions.js:80).
  //
  // Memoized because this runs on every render and a render happens on every
  // keystroke: unmemoized, a few hundred saved sessions were re-filtered and
  // re-grouped per typed character, which is felt as a laggy keyboard.
  const q = query.trim();
  const { shownActive, shownSaved, hitCount, projectSections } = useMemo(() => {
    // The phone's selector lifts unread answers out of `active` into their own
    // `newResults` list (chrome.js:55). They go straight back in here: unread
    // IS one of the three ways a session waits for you, so it belongs in Needs
    // attention with the other two — which is where the desktop has always put
    // it, and what the catalogue draws (zones-lab.jsx:59). Two headings for one
    // idea, one per density, is the drift this component exists to end.
    const hit = (s) => sessionSearchMatch(q, s);
    const allActive = [...newResults, ...active];
    const activeHits = allActive.filter(hit);
    const savedHits = saved.filter(hit);
    return {
      shownActive: activeHits,
      shownSaved: savedHits,
      hitCount: activeHits.length + savedHits.length,
      projectSections: filterProjectSections(groupProjectSessions([...allActive, ...saved]), query),
    };
  }, [q, query, newResults, active, saved]);

  /* Saved sessions are deliberately not offered to the attention split: a saved
     session is parked on purpose, so it belongs under Saved even if it ended
     badly. */
  const { needs: needsAttention, rest: restActive } = partitionByAttention(shownActive);
  // The recency view caps its saved tail behind the same "Show all" the grouped
  // view uses for its own: without one, a roster of 273 builds a row each on
  // open, which a phone pays for in dropped frames before the first one reads.
  const savedPreview = previewSavedSessions(shownSaved, { expanded: showAllSaved, searching: !!q });

  const row = (s, hidePath = false) => (
    <div class="sidebar-session" key={s.id}>
      <SessionRow
        variant="card"
        title={s.title}
        state={s.state || (s.saved ? "saved" : "idle")}
        active={s.active ?? s.id === activeId}
        unseen={s.unseen}
        when={s.when || s.meta}
        brief={s.brief}
        briefTone={s.briefTone}
        /* The monogram is the project's identity, in a form you can read at a
           glance, and it frees the second line for the reason. */
        mono={s.mono || projectMonogram(s.cwd)}
        /* Two lines is the budget: a row that says WHY it wants you has spent
           the second one, so the path stands down. Inside a project group the
           heading has already said where these live. */
        path={hidePath || s.brief ? undefined : s.path}
        pane={s.pane}
        origin={s.origin}
        onClick={() => onSelectSession?.(s.id)}
      />
      <SessionCardMenu
        session={s}
        onClose={onCloseSession}
        onReopen={onReopenSession}
        onDelete={onDeleteSession}
        scrollContainerSelector=".sidebar-list"
      />
    </div>
  );

  return (
    <aside class={`sidebar${phone ? " is-phone" : ""}`}>
      <div class="sidebar-head">
        <span class="sidebar-wordmark">moa</span>
        {/* Search is a recess cut into the sheet: present at rest, so it reads
            as an object you can reach for, but sunken so it never competes
            with the raised things (the current row, New session). The control
            is the Field primitive — surface, height and the 16px iOS floor come
            from there; what belongs to the sidebar is only where it sits.

            The keycap is a BUTTON, not an ornament: this field FILTERS the list
            and ⌘K JUMPS to a session from anywhere, so the two do not get in
            each other's way. Only where there is a keyboard. */}
        <Field
          variant="inset"
          size={phone ? "lg" : "md"}
          class="sidebar-search"
          leading={<Search size={15} />}
          trailing={phone ? undefined : (
            <button
              type="button"
              class="sidebar-jump"
              onClick={onSearch}
              aria-label={`Jump to session ${formatShortcut("K", { mod: true })}`}
              title={`Jump to session ${formatShortcut("K", { mod: true })}`}
            >
              <Kbd>{formatShortcut("K", { mod: true })}</Kbd>
            </button>
          )}
          type="text"
          aria-label="Filter sessions"
          placeholder="Search"
          autocomplete="off"
          autocapitalize="off"
          autocorrect="off"
          spellcheck={false}
          value={query}
          onInput={(e) => setQuery(e.target.value)}
        />
      </div>

      {inboxOpen ? (
        <div class="sidebar-list is-inbox">
          <InboxView
            cards={inbox}
            health={inboxHealth}
            onRetry={onRetryInbox}
            onSend={onRouteEvent}
            onNewSession={onNewSessionForEvent}
            onIgnore={onDismissEvent}
            onIgnoreSource={onDismissEventSource}
            onOpenSession={onSelectSession}
          />
        </div>
      ) : (
        <div class="sidebar-list">
          <div class="sidebar-order" role="radiogroup" aria-label="Session order">
            {ORDERS.map(([id, label]) => {
              const on = (id === "folder") === !!groupByProject;
              return (
                <button
                  type="button"
                  role="radio"
                  aria-checked={on}
                  class={`sidebar-order-b${on ? " is-on" : ""}`}
                  onClick={() => onGroupByProject?.(id === "folder")}
                  key={id}
                >
                  {label}
                </button>
              );
            })}
          </div>

          {!q && hitCount === 0 && (
            <button type="button" class="sidebar-empty-new" onClick={onNewSession}>
              + New session
            </button>
          )}
          {q && hitCount === 0 && (
            <span class="sidebar-note">No session matches “{query}”</span>
          )}

          {/* wake-on-event note: unread answers are NOT a fourth group. They
              are the third state that makes a session wait for you, so they sit
              at the top of Needs attention, after blocked and broken
              (project-sessions.js:150). */}

          {groupByProject ? projectSections.map((section) => {
            const collapsed = projectCollapsed(section, collapsedProjects, !!q);
            const expanded = expandedProjects.has(section.key);
            const shownSessions = visibleProjectSessions(section, expanded, !!q);
            const hiddenSaved = hiddenProjectSavedCount(section, expanded, !!q);
            const label = section.attention === "permission"
              ? `${section.label}, ${section.openCount} open, ${section.attentionCount} needs permission`
              : section.attention === "error"
                ? `${section.label}, ${section.openCount} open, ${section.attentionCount} has an error`
                : `${section.label}, ${section.openCount} open${section.savedCount ? `, ${section.savedCount} saved` : ""}`;
            return (
              <section class={`sidebar-project${collapsed ? "" : " is-open"}`} key={section.key}>
                {/* The monogram identifies, the path locates, the count sizes.
                    Same row grammar as a session, one step quieter. */}
                <button
                  type="button"
                  class="sidebar-label is-project"
                  aria-expanded={!collapsed}
                  aria-label={label}
                  onClick={() => onToggleProject?.(section.key, !collapsed)}
                >
                  <span class="sidebar-project-chevron"><ChevronRight size={14} aria-hidden="true" /></span>
                  <span class="mono sidebar-project-mono" style={`--mono-h:${projectMonogram(section.key).hue}`} aria-hidden="true">
                    {projectMonogram(section.key).text}
                  </span>
                  <span class="sidebar-project-name">{section.label}</span>
                  {section.attention && <span class={`state-dot ${section.attention}`} aria-hidden="true" />}
                  <span class="sidebar-project-path">{section.path}</span>
                  <span class="sidebar-count">{section.sessions.length}</span>
                </button>
                {!collapsed && (
                  <div class="sidebar-group">
                    {shownSessions.map((s) => row(s, true))}
                    {hiddenSaved > 0 && (
                      <button
                        type="button"
                        class="sidebar-show-all"
                        onClick={() => setExpandedProjects((keys) => new Set(keys).add(section.key))}
                      >
                        Show all {hiddenSaved} saved
                      </button>
                    )}
                  </div>
                )}
              </section>
            );
          }) : (
            <>
              {/* Needs attention comes first, and belongs to this view only:
                  grouped by folder, a session listed here AND inside its folder
                  is the same row printed twice, so there the alarm rides on the
                  folder heading instead. */}
              {needsAttention.length > 0 && (
                <>
                  <div class="sidebar-label is-attention">
                    Needs attention<span class="sidebar-count is-attention">{needsAttention.length}</span>
                  </div>
                  <div class="sidebar-group">{needsAttention.map((s) => row(s))}</div>
                </>
              )}
              {restActive.length > 0 && (
                <>
                  <div class="sidebar-label">Active<span class="sidebar-count">{restActive.length}</span></div>
                  <div class="sidebar-group">{restActive.map((s) => row(s))}</div>
                </>
              )}
              {shownSaved.length > 0 && (
                <>
                  <div class="sidebar-label">Saved<span class="sidebar-count">{shownSaved.length}</span></div>
                  <div class="sidebar-group">
                    {savedPreview.visible.map((s) => row(s))}
                    {savedPreview.hidden > 0 && (
                      <button type="button" class="sidebar-show-all" onClick={() => setShowAllSaved(true)}>
                        Show all {shownSaved.length} saved
                      </button>
                    )}
                  </div>
                </>
              )}
            </>
          )}
        </div>
      )}

      {/* New anchors the bottom, where the thumb is. It is the one action, so
          it gets the width — and the word, which a 28px "+" in the head never
          had room for. */}
      <button type="button" class="sidebar-new" onClick={onNewSession}>
        <Plus size={16} aria-hidden="true" />New session
      </button>

      {/* The foot is about the APP, not about a session: the inbox, the build,
          and the global settings. Same place both densities kept settings. */}
      <div class="sidebar-foot">
        {/* wake-on-event: the door appears once anything has ever arrived — a
            permanent icon for someone with no hooks configured would be chrome
            that never does anything. It also appears when the inbox could NOT
            be read: with no list there is no way to know that nothing arrived,
            and hiding the door would hide the failure too. */}
        {inboxVisible && (
          <button
            type="button"
            class={`sidebar-inbox${inboxOpen ? " is-on" : ""}`}
            aria-pressed={inboxOpen}
            aria-label={inboxCount > 0 ? `Inbox, ${inboxCount} waiting` : "Inbox"}
            onClick={onInbox}
          >
            <Inbox size={16} aria-hidden="true" />
            Inbox
            {/* The count is yellow because the inbox IS "needs you": state, not
                accent. */}
            {inboxCount > 0 && <span class="sidebar-inbox-n">{inboxCount > 9 ? "9+" : inboxCount}</span>}
          </button>
        )}
        <span class="sidebar-foot-spacer" />
        <SidebarVersion version={version} />
        <IconButton label="Settings" onClick={onSettings}>
          <Settings size={16} />
        </IconButton>
      </div>
    </aside>
  );
}
