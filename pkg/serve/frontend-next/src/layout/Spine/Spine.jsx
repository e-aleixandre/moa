import { Search, Plus, Settings } from "lucide-preact";
import { Kbd, IconButton } from "../../primitives/index.js";
import { SessionRow } from "../../components/index.js";
import "./Spine.css";

// Spine — barra lateral izquierda de sesiones. Sustituye a la TabBar
// inferior del frontend actual: cabecera con logo/wordmark/versión, buscador
// (trigger, no input real todavía), listas ACTIVE/SAVED de SessionRow
// (variant="card") y pie con estado de Pulse + settings.
const ACTIVE_SESSIONS = [
  { key: "ws-race-fix", title: "ws race fix", state: "running", active: true, pane: "P1" },
  {
    key: "deploy-pulse-api",
    title: "deploy pulse api",
    state: "permission",
    pane: "P2",
    unseen: true,
  },
  { key: "frontend-polish", title: "frontend polish", state: "idle", meta: "2h" },
  { key: "migrate-sqlite", title: "migrate sqlite", state: "error", pane: "P3", unseen: true },
];

const SAVED_SESSIONS = [
  { key: "verifier-design-notes", title: "verifier design notes", meta: "3d" },
  { key: "changelog-0-10", title: "changelog 0.10", meta: "6d" },
];

export function Spine({
  version = "v0.10.2",
  activeSessions = ACTIVE_SESSIONS,
  savedSessions = SAVED_SESSIONS,
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
        <span class="ver">{version}</span>
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
              key={s.key}
              variant="card"
              title={s.title}
              state={s.state}
              active={s.active}
              unseen={s.unseen}
              meta={s.meta}
              pane={s.pane}
              onClick={() => onSelectSession?.(s.key)}
            />
          ))}
        </div>

        <div class="spine-label">Saved</div>
        <div class="spine-list">
          {savedSessions.map((s) => (
            <SessionRow
              key={s.key}
              variant="card"
              title={s.title}
              state="saved"
              meta={s.meta}
              onClick={() => onSelectSession?.(s.key)}
            />
          ))}
        </div>
      </div>

      <button type="button" class="new-session" onClick={onNewSession}>
        <Plus size={14} aria-hidden="true" />
        New session
      </button>

      <div class="spine-foot">
        <span class="pulse-status">
          <span class="pdot" aria-hidden="true" />
          Pulse paired
        </span>
        <IconButton label="Settings" onClick={onSettings}>
          <Settings size={15} />
        </IconButton>
      </div>
    </aside>
  );
}
