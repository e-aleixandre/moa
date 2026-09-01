import { useEffect, useState } from "preact/hooks";
import { Spine } from "../Spine/Spine.jsx";
import { Sheet, GlobalSettings } from "../../components/index.js";
import { store } from "../../data/store.js";
import { openSession } from "../../data/tile-actions.js";
import { openPalette } from "../../data/palette.js";
import { setGroupByProject } from "../../data/drawer.js";
import { closeSession, deleteSession, resumeSession } from "../../data/session-actions.js";
import { focusedSessionId } from "../../data/selectors.js";
import { focusedTileSessionId, paneBadges, spineSessions } from "../Spine/sessions.js";
import "./DesktopShell.css";

// DesktopShell — the desktop chrome. Spine lives here once. Conversation and
// grid only swap the main column, so Close / reopen / delete cannot drift
// between views.

export function DesktopShell({ version, children }) {
  const [state, setState] = useState(store.get());
  useEffect(() => store.subscribe(setState), []);
  const [globalSettingsOpen, setGlobalSettingsOpen] = useState(false);

  const inGrid = state.view === "grid";
  const paneOf = inGrid ? paneBadges(state.tileTree) : undefined;
  const { active, saved } = spineSessions(state.sessions, paneOf);
  const activeId = inGrid ? focusedTileSessionId(state) : focusedSessionId(state);

  return (
    <div class="desktop-shell">
      <Spine
        version={version}
        activeSessions={active}
        savedSessions={saved}
        activeId={activeId}
        groupByProject={state.groupByProject}
        onGroupByProject={setGroupByProject}
        onSelectSession={(id) => openSession(id)}
        onNewSession={() => openPalette("create")}
        onSearch={() => openPalette("search")}
        onSettings={() => setGlobalSettingsOpen(true)}
        onCloseSession={(id) => { closeSession(id).catch(() => {}); }}
        onReopenSession={(id) => { resumeSession(id).catch(() => {}); }}
        onDeleteSession={(id) => { deleteSession(id).catch(() => {}); }}
      />
      {children}
      <Sheet
        open={globalSettingsOpen}
        onClose={() => setGlobalSettingsOpen(false)}
        title="Settings"
        class="global-settings-sheet"
      >
        <GlobalSettings soundEnabled={state.soundEnabled} version={version} />
      </Sheet>
    </div>
  );
}
