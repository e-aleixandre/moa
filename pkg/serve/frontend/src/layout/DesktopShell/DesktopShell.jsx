import { useState } from "preact/hooks";
import { Sidebar } from "../Sidebar/Sidebar.jsx";
import { DesktopDossier } from "./DesktopDossier.jsx";
import { GlobalSettings } from "../../components/index.js";
import { useStore } from "../../hooks/useStore.js";
import { openSession } from "../../data/tile-actions.js";
import { openPalette } from "../../data/palette.js";
import { setDrawerProjectCollapsed, setSidebarMode } from "../../data/drawer.js";
import { createOwner, openOwnerConversation, retryOwners } from "../../data/owners.js";
import { closeSession, deleteSession, resumeSession } from "../../data/session-actions.js";
import { dismissEvent, dismissSource, retryEvents, routeEvent, routeEventToNewSession, toggleInbox } from "../../data/events.js"; // wake-on-event
import { selectDesktopChrome } from "../Sidebar/sessions.js";
import "./DesktopShell.css";

// DesktopShell — the desktop chrome, in THREE ZONES: the other sessions on the
// left, the result in the middle, this session's dossier on the right.
// Conversation and grid only swap the middle zone, so Close / reopen / delete
// cannot drift between views. Subscribes to the roster snapshot, not the whole
// store: a streaming token must not rebuild the sidebar (which is also why the
// dossier subscribes on its own, inside DesktopDossier).
//
// The third zone is a real column only where one fits; narrower than that it
// stays the drawer it has always been. Both live at the same place in the DOM —
// the switch is in DesktopShell.css, because a media query can restyle a node
// but cannot move it.

export function DesktopShell({ version, children }) {
  const chrome = useStore(selectDesktopChrome);
  const [globalSettingsOpen, setGlobalSettingsOpen] = useState(false);

  return (
    <div class="desktop-shell">
      <Sidebar
        version={version}
        active={chrome.active}
        inbox={chrome.inbox}
        inboxHealth={chrome.inboxHealth}
        inboxOpen={chrome.inboxOpen}
        inboxCount={chrome.inboxPending}
        /* wake-on-event: the door appears once anything has ever arrived, and
           also when the inbox could NOT be read — with no list there is no way
           to know that nothing arrived, and hiding the door hides the failure. */
        inboxVisible={chrome.inbox.length > 0 || chrome.inboxHealth?.status === "error"}
        onInbox={toggleInbox}
        onRetryInbox={() => { retryEvents().catch(() => {}); }}
        onRouteEvent={(id, sessionId) => { routeEvent(id, sessionId).catch(() => {}); }}
        onNewSessionForEvent={(id, spec) => { routeEventToNewSession(id, spec).catch(() => {}); }}
        onDismissEvent={(id) => { dismissEvent(id).catch(() => {}); }}
        onDismissEventSource={(source) => { dismissSource(source).catch(() => {}); }}
        saved={chrome.saved}
        activeId={chrome.activeId}
        mode={chrome.sidebarMode}
        onMode={setSidebarMode}
        owners={chrome.owners}
        ownersHealth={chrome.ownersHealth}
        activeOwnerId={chrome.activeOwnerId}
        onOpenOwner={(own) => openOwnerConversation(own)}
        onOpenOwnerChild={(child) => openSession(child.id)}
        onCreateOwner={(spec) => createOwner(spec)}
        onRetryOwners={() => { retryOwners().catch(() => {}); }}
        collapsedProjects={chrome.drawerCollapsed}
        onToggleProject={setDrawerProjectCollapsed}
        onSelectSession={(id) => openSession(id)}
        onNewSession={() => openPalette("create")}
        onSearch={() => openPalette("search")}
        onSettings={() => setGlobalSettingsOpen(true)}
        onCloseSession={(id) => { closeSession(id).catch(() => {}); }}
        onReopenSession={(id) => { resumeSession(id).catch(() => {}); }}
        onDeleteSession={(id) => { deleteSession(id).catch(() => {}); }}
      />
      {children}
      <DesktopDossier />
      {/* The settings sheet draws its own surface now: it is the catalogue's
          centred panel, with its own head, scrim and pushed pages, so wrapping
          it in the generic Sheet would give it a second head and a second
          shell. See components/GlobalSettings. */}
      <GlobalSettings
        open={globalSettingsOpen}
        onClose={() => setGlobalSettingsOpen(false)}
        soundEnabled={chrome.soundEnabled}
        version={version}
      />
    </div>
  );
}
