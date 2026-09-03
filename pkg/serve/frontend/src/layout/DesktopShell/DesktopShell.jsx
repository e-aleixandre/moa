import { useState } from "preact/hooks";
import { Spine } from "../Spine/Spine.jsx";
import { Sheet, GlobalSettings } from "../../components/index.js";
import { useStore } from "../../hooks/useStore.js";
import { openSession } from "../../data/tile-actions.js";
import { openPalette } from "../../data/palette.js";
import { setGroupByProject } from "../../data/drawer.js";
import { closeSession, deleteSession, resumeSession } from "../../data/session-actions.js";
import { dismissEvent, dismissSource, routeEvent, routeEventToNewSession, toggleInbox } from "../../data/events.js"; // wake-on-event
import { selectDesktopChrome } from "../Spine/sessions.js";
import "./DesktopShell.css";

// DesktopShell — the desktop chrome. Spine lives here once. Conversation and
// grid only swap the main column, so Close / reopen / delete cannot drift
// between views. Subscribes to the roster snapshot, not the whole store: a
// streaming token must not rebuild the sidebar.

export function DesktopShell({ version, children }) {
  const chrome = useStore(selectDesktopChrome);
  const [globalSettingsOpen, setGlobalSettingsOpen] = useState(false);

  return (
    <div class="desktop-shell">
      <Spine
        version={version}
        activeSessions={chrome.active}
        inbox={chrome.inbox}
        inboxOpen={chrome.inboxOpen}
        onToggleInbox={toggleInbox}
        onRouteEvent={(id, sessionId) => { routeEvent(id, sessionId).catch(() => {}); }}
        onNewSessionForEvent={(id, spec) => { routeEventToNewSession(id, spec).catch(() => {}); }}
        onDismissEvent={(id) => { dismissEvent(id).catch(() => {}); }}
        onDismissEventSource={(source) => { dismissSource(source).catch(() => {}); }}
        savedSessions={chrome.saved}
        activeId={chrome.activeId}
        groupByProject={chrome.groupByProject}
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
        <GlobalSettings soundEnabled={chrome.soundEnabled} version={version} />
      </Sheet>
    </div>
  );
}
