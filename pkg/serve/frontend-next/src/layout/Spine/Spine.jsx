import { Search, Plus, Settings } from "lucide-preact";
import { Kbd, IconButton } from "../../primitives/index.js";
import { SessionRow } from "../../components/index.js";
import "./Spine.css";

// Spine — left sidebar of sessions. Replaces the current frontend's
// bottom TabBar: header with logo/wordmark/version, search
// (trigger, no real input yet), ACTIVE/SAVED lists of SessionRow
// (variant="card") and footer with Pulse status + settings.
//
// Connected in 5C: the ConversationScreen container builds `activeSessions`/
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

export function Spine({
  version = null,
  activeSessions = ACTIVE_SESSIONS,
  savedSessions = SAVED_SESSIONS,
  activeId,
  onSelectSession,
  onNewSession,
  onSearch,
  onSettings,
}) {
  return (
    <aside class="spine">
      <div class="spine-head">
        <span class="logo" aria-hidden="true">m</span>
        <span class="wordmark">moa</span>
        {version && <span class="ver">{version}</span>}
      </div>

      <button type="button" class="spine-search" onClick={onSearch}>
        <Search size={14} aria-hidden="true" />
        <span>Jump to session…</span>
        <Kbd>⌘K</Kbd>
      </button>

      <div class="spine-sessions">
        <div class="spine-label">Active</div>
        <div class="spine-list">
          {activeSessions.map((s) => (
            <SessionRow
              key={s.id}
              variant="card"
              title={s.title}
              state={s.state}
              active={s.active ?? s.id === activeId}
              unseen={s.unseen}
              meta={s.meta}
              pane={s.pane}
              onClick={() => onSelectSession?.(s.id)}
            />
          ))}
        </div>

        <div class="spine-label">Saved</div>
        <div class="spine-list">
          {savedSessions.map((s) => (
            <SessionRow
              key={s.id}
              variant="card"
              title={s.title}
              state="saved"
              meta={s.meta}
              onClick={() => onSelectSession?.(s.id)}
            />
          ))}
        </div>
      </div>

      <button type="button" class="new-session" onClick={onNewSession}>
        <Plus size={14} aria-hidden="true" />
        New session
      </button>

      <div class="spine-foot">
        {/* 5N: Pulse pairing status is wired with the pairing subphase; until
            then we don't assert a paired state we haven't checked. */}
        <IconButton label="Settings" onClick={onSettings}>
          <Settings size={15} />
        </IconButton>
      </div>
    </aside>
  );
}
