import { Search, Plus, Settings, MoreHorizontal, Check } from "lucide-preact";
import { useEffect, useRef, useState } from "preact/hooks";
import { IconButton, Kbd } from "../../primitives/index.js";
import { InboxButton, InboxView, SessionCardMenu, SessionRow } from "../../components/index.js"; // wake-on-event: InboxButton/InboxView
import { formatShortcut } from "../../data/util/shortcut.js";
import { groupProjectSessions, hiddenProjectSavedCount, visibleProjectSessions } from "../../data/util/project-sessions.js";
import { inboxPendingCount } from "../../data/events.js"; // wake-on-event
import { useMenuKeyboard } from "../../hooks/useMenuKeyboard.js";
import "./Spine.css";

function SpineVersion({ version }) {
  if (!version?.current) return null;
  // current/latest already arrive v-prefixed from the server (release
  // DisplayVersion / cache), so use them verbatim — don't add another "v".
  const current = version.current;
  if (version.update_available && version.latest) {
    return (
      <a
        class="ver ver-update"
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
    <span class="ver" title="moa version">
      {current}
    </span>
  );
}

// Spine — left column of sessions: wordmark, jump, the list, settings.
//
// Connected: the ConversationScreen container builds `activeSessions`/
// `savedSessions` from the store and passes them in, along with `activeId`
// (the focused session, highlighted). The mock arrays below are kept only as a
// fallback for isolated rendering (e.g. galleries) — with real data the
// container always supplies the props.
const ACTIVE_SESSIONS = [
  { id: "ws-race-fix", title: "ws race fix", state: "running", when: "now", brief: "Working…", path: "~/dev/moa/main" },
  { id: "deploy-pulse-api", title: "deploy pulse api", state: "permission", when: "now", brief: "Needs you", path: "~/dev/moa/pulse-api", unseen: true },
  { id: "frontend-polish", title: "frontend polish", state: "idle", when: "2h", path: "~/dev/moa/frontend-polish" },
  { id: "migrate-sqlite", title: "migrate sqlite", state: "error", when: "18m", brief: "Error", path: "~/dev/moa/migrate", unseen: true },
];

const SAVED_SESSIONS = [
  { id: "verifier-design-notes", title: "verifier design notes", when: "3d", path: "~/dev/moa/main", saved: true },
  { id: "changelog-0-10", title: "changelog 0.10", when: "6d", path: "~/dev/moa/main", saved: true },
];

function SpineGroupMenu({ groupByProject, onChange }) {
  const [open, setOpen] = useState(false);
  const ref = useRef(null);
  const menuRef = useRef(null);
  const triggerRef = useRef(null);
  const { onMenuKeyDown, closeMenu } = useMenuKeyboard(open, setOpen, triggerRef, menuRef);
  useEffect(() => {
    if (!open) return;
    const close = (e) => { if (ref.current && !ref.current.contains(e.target)) setOpen(false); };
    document.addEventListener("mousedown", close);
    return () => { document.removeEventListener("mousedown", close); };
  }, [open]);
  const choose = (value) => { onChange?.(value); closeMenu(); };
  return <div class="spine-group-wrap" ref={ref}>
    <button type="button" ref={triggerRef} class="spine-group-more" aria-label="Session grouping" aria-haspopup="menu" aria-expanded={open} onClick={() => setOpen((v) => !v)}><MoreHorizontal size={16} /></button>
    {open && <div class="spine-group-menu" role="menu" aria-label="Session grouping" ref={menuRef} onKeyDown={onMenuKeyDown}>
      <span>Group sessions</span>
      <button type="button" role="menuitemradio" aria-checked={!groupByProject} onClick={() => choose(false)}><i>{!groupByProject && <Check size={14} />}</i>By recency</button>
      <button type="button" role="menuitemradio" aria-checked={groupByProject} onClick={() => choose(true)}><i>{groupByProject && <Check size={14} />}</i>By folder</button>
    </div>}
  </div>;
}

export function Spine({
  version = null,
  activeSessions = ACTIVE_SESSIONS,
  savedSessions = SAVED_SESSIONS,
  activeId,
  onSelectSession,
  onNewSession,
  onSearch,
  onSettings,
  onCloseSession,
  onReopenSession,
  onDeleteSession,
  groupByProject = false,
  onGroupByProject,
  // wake-on-event: the inbox is the spine's OTHER list, not a group inside the
  // session list. `inboxOpen` swaps which one this same column shows, so an
  // event arriving never pushes the sessions down.
  inbox = [],
  inboxOpen = false,
  onToggleInbox,
  onRouteEvent,
  onNewSessionForEvent,
  onDismissEvent,
  onDismissEventSource,
}) {
  const [expandedProjects, setExpandedProjects] = useState(() => new Set());
  const pendingInbox = inboxPendingCount(inbox); // wake-on-event
  const projectSections = groupProjectSessions([...activeSessions, ...savedSessions]);
  const row = (s) => (
    <div class="spine-session-card" key={s.id}>
      <SessionRow
        variant="card"
        title={s.title}
        state={s.state || (s.saved ? "saved" : "idle")}
        active={s.active ?? s.id === activeId}
        unseen={s.unseen}
        when={s.when || s.meta}
        brief={s.brief}
        path={s.path}
        pane={s.pane}
        origin={s.origin}
        onClick={() => onSelectSession?.(s.id)}
      />
      <SessionCardMenu
        session={s}
        onClose={onCloseSession}
        onReopen={onReopenSession}
        onDelete={onDeleteSession}
        scrollContainerSelector=".spine-sessions"
      />
    </div>
  );
  return (
    <aside class="spine">
      <div class="spine-head">
        <span class="wordmark">moa</span>
        <button type="button" class="spine-jump" onClick={onSearch} aria-label={`Jump to session ${formatShortcut("K", { mod: true })}`} title={`Jump to session ${formatShortcut("K", { mod: true })}`}>
          <Search size={13} aria-hidden="true" />
          <Kbd>{formatShortcut("K", { mod: true })}</Kbd>
        </button>
        <button type="button" class="spine-new" aria-label="New session" onClick={onNewSession}>
          <Plus size={14} aria-hidden="true" />
        </button>
        {/* wake-on-event: the inbox's door. It appears once anything has ever
            arrived — a permanent icon for someone with no hooks configured
            would be chrome that never does anything. */}
        {inbox.length > 0 && (
          <InboxButton count={pendingInbox} open={inboxOpen} onClick={onToggleInbox} size={15} />
        )}
        <SpineGroupMenu groupByProject={groupByProject} onChange={onGroupByProject} />
      </div>

      {inboxOpen ? (
        <div class="spine-sessions spine-inbox">
          <InboxView
            cards={inbox}
            onSend={onRouteEvent}
            onNewSession={onNewSessionForEvent}
            onIgnore={onDismissEvent}
            onIgnoreSource={onDismissEventSource}
            onOpenSession={onSelectSession}
          />
        </div>
      ) : (
      <div class="spine-sessions">
        {activeSessions.length === 0 && savedSessions.length === 0 && (
          <button type="button" class="spine-empty-new" onClick={onNewSession}>
            + New session
          </button>
        )}
        {groupByProject ? projectSections.map((section) => {
          const expanded = expandedProjects.has(section.key);
          const shownSessions = visibleProjectSessions(section, expanded);
          const hiddenSaved = hiddenProjectSavedCount(section, expanded);
          return <div class="spine-project" key={section.key}>
            <div class="spine-label">{section.label}{section.attention && <span class={`state-dot spine-project-attention ${section.attention}`} />}<small>{section.openCount} open{section.savedCount ? ` · ${section.savedCount} saved` : ""}</small></div>
            {section.path && <div class="spine-project-path">{section.path}</div>}
            <div class="spine-list">{shownSessions.map(row)}{hiddenSaved > 0 && <button type="button" class="spine-show-all" onClick={() => setExpandedProjects((keys) => new Set(keys).add(section.key))}>Show all {hiddenSaved} saved</button>}</div>
          </div>;
        }) : <>
          <div class="spine-list">{activeSessions.map(row)}</div>
          {savedSessions.length > 0 && <>
            <div class="spine-label">Saved</div>
            <div class="spine-list">{savedSessions.map(row)}</div>
          </>}
        </>}
      </div>
      )}

      <div class="spine-foot">
        <SpineVersion version={version} />
        <IconButton label="Settings" onClick={onSettings}>
          <Settings size={15} />
        </IconButton>
      </div>
    </aside>
  );
}
