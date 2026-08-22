import { Search, Plus, Settings, MoreHorizontal, Check } from "lucide-preact";
import { useEffect, useRef, useState } from "preact/hooks";
import { Kbd, IconButton } from "../../primitives/index.js";
import { SessionCardMenu, SessionRow } from "../../components/index.js";
import { formatShortcut } from "../../data/util/shortcut.js";
import { groupProjectSessions, hiddenProjectSavedCount, visibleProjectSessions } from "../../data/util/project-sessions.js";
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

// Spine — left sidebar of sessions. Replaces the current frontend's
// bottom TabBar: header with logo/wordmark/version, search
// (trigger, no real input yet), ACTIVE/SAVED lists of SessionRow
// (variant="card") and footer with Pulse status + settings.
//
// Connected: the ConversationScreen container builds `activeSessions`/
// `savedSessions` from the store and passes them in, along with `activeId`
// (the focused session, highlighted). The mock arrays below are kept only as a
// fallback for isolated rendering (e.g. galleries) — with real data the
// container always supplies the props.
const ACTIVE_SESSIONS = [
  { id: "ws-race-fix", title: "ws race fix", state: "running", pane: "P1" },
  { id: "deploy-pulse-api", title: "deploy pulse api", state: "permission", pane: "P2", unseen: true },
  { id: "frontend-polish", title: "frontend polish", state: "idle", meta: "2h" },
  { id: "migrate-sqlite", title: "migrate sqlite", state: "error", pane: "P3", unseen: true },
];

const SAVED_SESSIONS = [
  { id: "verifier-design-notes", title: "verifier design notes", meta: "3d" },
  { id: "changelog-0-10", title: "changelog 0.10", meta: "6d" },
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
}) {
  const [expandedProjects, setExpandedProjects] = useState(() => new Set());
  const projectSections = groupProjectSessions([...activeSessions, ...savedSessions]);
  const row = (s) => (
    <div class="spine-session-card" key={s.id}>
      <SessionRow variant="card" title={s.title} state={s.state || (s.saved ? "saved" : "idle")} active={s.active ?? s.id === activeId} unseen={s.unseen} meta={s.meta} pane={s.pane} origin={s.origin} onClick={() => onSelectSession?.(s.id)} />
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
        <span class="logo" aria-hidden="true">m</span>
        <span class="wordmark">moa</span>
        <SpineVersion version={version} />
        <SpineGroupMenu groupByProject={groupByProject} onChange={onGroupByProject} />
      </div>

      <button type="button" class="spine-search" onClick={onSearch}>
        <Search size={14} aria-hidden="true" />
        <span>Jump to session…</span>
        <Kbd>{formatShortcut("K", { mod: true })}</Kbd>
      </button>

      <div class="spine-sessions">
        {groupByProject ? projectSections.map((section) => {
          const expanded = expandedProjects.has(section.key);
          const shownSessions = visibleProjectSessions(section, expanded);
          const hiddenSaved = hiddenProjectSavedCount(section, expanded);
          return <div class="spine-project" key={section.key}>
            <div class="spine-label">{section.label}{section.attention && <span class={`state-dot spine-project-attention ${section.attention}`} />}<small>{section.openCount} open{section.savedCount ? ` · ${section.savedCount} saved` : ""}</small></div>
            {section.path && <div class="spine-project-path">{section.path}</div>}
            <div class="spine-list">{shownSessions.map(row)}{hiddenSaved > 0 && <button type="button" class="spine-show-all" onClick={() => setExpandedProjects((keys) => new Set(keys).add(section.key))}>Show all {hiddenSaved} saved</button>}</div>
          </div>;
        }) : <><div class="spine-label">Active</div><div class="spine-list">{activeSessions.map(row)}</div><div class="spine-label">Saved</div><div class="spine-list">{savedSessions.map(row)}</div></>}
      </div>

      <button type="button" class="new-session" onClick={onNewSession}>
        <Plus size={14} aria-hidden="true" />
        New session
      </button>

      <div class="spine-foot">
        <IconButton label="Settings" onClick={onSettings}>
          <Settings size={15} />
        </IconButton>
      </div>
    </aside>
  );
}
