import { useState, useEffect, useRef } from "preact/hooks";
import { AppWindow, Inbox, Plus } from "lucide-preact";
import { updateSession, store } from "../../../data/store.js";
import { useStore } from "../../../hooks/useStore.js";
import { projectStream, liveTrayAgents } from "../../../data/stream-model.js";
import { focusedSessionId } from "../../../data/selectors.js";
import { setActiveSession } from "../../../data/tile-actions.js";
import { openDrawer, closeDrawer, setDrawerProjectCollapsed, setGroupByProject } from "../../../data/drawer.js";
import { openPersistedSubagent, openBashJob, closeSession, deleteSession, resumeSession, createSession, rewindToMessage } from "../../../data/session-actions.js";
import { addToast } from "../../../data/notifications.js";
import { closeInbox, dismissEvent, dismissSource, inboxPendingCount, routeEvent, routeEventToNewSession, toggleInbox } from "../../../data/events.js";
import { PermissionPrompt, AskUserPrompt, McpBanner, GlobalSettings } from "../../../components/index.js";
import { LivePreview } from "../../../components/LivePreview/LivePreview.jsx";
import { MobileComposer } from "../MobileComposer/MobileComposer.jsx";
import { MobileTitleChip } from "../MobileTitleChip/MobileTitleChip.jsx";
import { SessionDrawer } from "../SessionDrawer/SessionDrawer.jsx";
import { MobileSheet } from "../MobileSheet/MobileSheet.jsx";
import { SecretBatch } from "../../../components/SecretBatch/SecretBatch.jsx";
import { RewindTimeline } from "../../RewindTimeline/RewindTimeline.jsx";
import { MobileStream } from "./MobileStream.jsx";
import { MobileNowLine } from "./MobileNowLine.jsx";
import { MobileSubagentView } from "./MobileSubagentView.jsx";
import { MobileBashJobView } from "./MobileBashJobView.jsx";
import { MobileInboxView } from "./MobileInboxView.jsx"; // wake-on-event
import { MobileActionRail } from "../MobileActionRail/MobileActionRail.jsx";
import { LiveDock } from "../../LiveDock/LiveDock.jsx";
import { selectMobileChrome } from "./chrome.js";
import "./MobileConversationScreen.css";

// MobileConversationScreen — the CONNECTED root container of the mobile
// conversation screen. It subscribes to the store, derives the focused (active)
// session, projects its stream, and passes real props down to the SHARED
// content components (via MobileStream) + the REAL Composer + the persistent
// mobile chrome (MobileStatusLine, hosted inside MobileComposer).
//
// There is no header and no session tab bar. The screen is a column: the
// transcript takes the space, then the ephemeral activity now-line
// (MobileNowLine) while the agent works, then the composer with the status line
// under it. Two things float over that column: the title chip at the top
// (MobileTitleChip — the session's name, and the door to the session list) and
// whatever overlay is open.
//
// The screen owns only the OVERLAYS it opens (the SessionDrawer and the
// RewindTimeline) and the store→props wiring. Model/thinking, permissions, path
// and usage live behind the status line's doors (MobileStatusLine); global
// settings (notifications) live behind the SessionDrawer footer. All reuse the
// real shared components.
//
// Architecture (OPTION B): the mobile screen reuses the desktop's data
// projection (projectStream) and shared components; the only divergence is the
// mobile layout chrome (MobileStream renders the SAME tool-group card, just
// denser). No data logic is duplicated. The mock specimen used by the design
// gallery lives in mobile-gallery.jsx (see MobileConversationSpecimen).

export function mobileFocusedSession(state, forceMobile = false) {
  const id = forceMobile ? state.activeSession || null : focusedSessionId(state);
  return { id, session: id ? state.sessions[id] || null : null };
}

export function selectMobileDrawerSession(session, { resume, activate, close }) {
  if (!session) return Promise.resolve();
  if (session.state !== "saved") {
    activate(session.id);
    close();
    return Promise.resolve();
  }
  return resume(session.id)
    .then(close)
    .catch(() => {});
}

export function MobileConversationScreen({ version = null, forceMobile = false }) {
  return (
    <div class="mconv">
      <MobileConversationBody forceMobile={forceMobile} />
      <MobileSessionChrome version={version} forceMobile={forceMobile} />
    </div>
  );
}

function MobileConversationBody({ forceMobile = false }) {
  const session = useStore((s) => mobileFocusedSession(s, forceMobile).session);
  const activeId = useStore((s) => mobileFocusedSession(s, forceMobile).id);
  const loaded = useStore((s) => s.sessionsLoaded);
  const usage = useStore((s) => s.usage);
  const chrome = useStore((s) => selectMobileChrome(s, forceMobile));

  const [rewindOpen, setRewindOpen] = useState(false);
  const [secretAliases, setSecretAliases] = useState(null);

  // --- Live Dock (SUBAGENTS-PERSISTENT-SPEC) ---
  // The dock is the permanent home for live ASYNC work (async subagents + bash)
  // above the composer ("async in the dock, sync inline").
  const liveAgents = session ? liveTrayAgents(session) : [];
  // Keyboard open → the dock folds to its compact bar (writing wins, §1.5). We
  // detect the soft keyboard by a large shrink of visualViewport vs the layout
  // viewport, the standard heuristic (no dedicated API).
  const [kbdOpen, setKbdOpen] = useState(false);
  useEffect(() => {
    const vv = typeof window !== "undefined" && window.visualViewport;
    if (!vv) return;
    let frame = null;
    let settleTimer = null;
    const sync = () => setKbdOpen(window.innerHeight - vv.height > 150);
    // Safari can report its final visual-viewport height a frame or two after
    // it sends the event, especially when an installed PWA returns foreground.
    // Sample both immediately and after that settle period so the Live Dock is
    // not left compact after the keyboard is gone.
    const scheduleSync = () => {
      sync();
      if (frame !== null) cancelAnimationFrame(frame);
      if (settleTimer !== null) clearTimeout(settleTimer);
      frame = requestAnimationFrame(sync);
      settleTimer = setTimeout(sync, 180);
    };
    const onVisibility = () => {
      if (document.visibilityState === "visible") scheduleSync();
    };
    vv.addEventListener("resize", scheduleSync);
    vv.addEventListener("scroll", scheduleSync);
    window.addEventListener("resize", scheduleSync);
    window.addEventListener("orientationchange", scheduleSync);
    document.addEventListener("visibilitychange", onVisibility);
    scheduleSync();
    return () => {
      vv.removeEventListener("resize", scheduleSync);
      vv.removeEventListener("scroll", scheduleSync);
      window.removeEventListener("resize", scheduleSync);
      window.removeEventListener("orientationchange", scheduleSync);
      document.removeEventListener("visibilitychange", onVisibility);
      if (frame !== null) cancelAnimationFrame(frame);
      if (settleTimer !== null) clearTimeout(settleTimer);
    };
  }, []);

  const onSelectFromDrawer = (id) => selectMobileDrawerSession(store.get().sessions[id], {
    resume: resumeSession,
    activate: setActiveSession,
    close: closeDrawer,
  });
  const onNew = () => openDrawer("new");

  useEffect(() => { setRewindOpen(false); }, [activeId]);
  useEffect(() => { setSecretAliases(null); }, [activeId]);

  const { recentSaved, activeCount, savedCount } = chrome;

  let body;
  if (!loaded) {
    body = <div class="mconv-placeholder">Loading sessions…</div>;
  } else if (!session) {
    const recents = recentSaved;
    const totalCount = activeCount + savedCount;
    if (totalCount === 0) {
      // First run — no sessions at all (EMPTY-STATE-SPEC §2.4). New is primary.
      body = (
        <div class="mconv-empty mconv-empty-firstrun">
          <p class="mconv-empty-title">No sessions yet</p>
          <p class="mconv-empty-sub">Start one to begin working with moa.</p>
          <button
            type="button"
            class="mconv-empty-new mconv-empty-new-primary"
            onClick={onNew}
          >
            <Plus size={15} aria-hidden="true" /> New session
          </button>
        </div>
      );
    } else {
      body = (
        <div class="mconv-empty">
          <p class="mconv-empty-title">No open sessions</p>
          <p class="mconv-empty-sub">{savedCount} saved · pick up where you left off</p>
          {recents.length > 0 && (
            <>
              <p class="mconv-empty-label">Recent</p>
              <div class="mconv-empty-recents">
                {recents.map((r) => (
                  <button
                    key={r.id}
                    type="button"
                    class="mconv-empty-recent"
                    aria-label={`${r.title} — saved, ${r.when}`}
                    onClick={() => onSelectFromDrawer(r.id)}
                  >
                    <span class="mconv-empty-recent-top">
                      <span class="mconv-empty-recent-title">{r.title}</span>
                      <span class="mconv-empty-recent-when">{r.when}</span>
                    </span>
                    <span class="mconv-empty-recent-path">{r.path}</span>
                  </button>
                ))}
              </div>
            </>
          )}
          <div class="mconv-empty-actions">
            <button
              type="button"
              class="mconv-empty-browse"
              onClick={() => openDrawer("list")}
            >
              All sessions · {activeCount + savedCount}
            </button>
            <button type="button" class="mconv-empty-new" onClick={onNew}>
              <Plus size={15} aria-hidden="true" /> New session
            </button>
          </div>
        </div>
      );
    }
  } else {
    const blocks = projectStream(session);
    const blocking = session.untrustedMcp || session.pendingPerm;
    if (session.viewingSubagent) {
      // The subagent view takes over the whole conversation surface (below
      // the header/strip), pushed full-screen. onBack clears viewingSubagent.
      body = (
        <MobileSubagentView
          key={session.viewingSubagent}
          session={session}
          jobId={session.viewingSubagent}
          onBack={() => updateSession(session.id, { viewingSubagent: null })}
        />
      );
    } else if (session.viewingBashJob) {
      // Same full-screen push for a background bash job's read-only view — the
      // dock's other openable row. Mutually exclusive with the subagent view by
      // construction (opening one clears the other).
      body = (
        <MobileBashJobView
          key={session.viewingBashJob}
          session={session}
          jobId={session.viewingBashJob}
          onBack={() => updateSession(session.id, { viewingBashJob: null })}
        />
      );
    } else {
      body = (
        <>
          <MobileStream
            key={session.id}
            session={session}
            blocks={blocks}
            // Rewind lives on the waypoints themselves now, not behind a door in
            // the status line: the mark is ON the message you want to go back to,
            // so "rewind to where" is answered by the tap. The full timeline
            // (assistant turns too, and existing branches) is still one link away
            // inside the confirmation — this is its only door on mobile.
            rewind={{
              to: (msgId) => rewindToMessage(session.id, msgId),
              openTimeline: () => setRewindOpen(true),
              disabled: session.state === "running" || session.state === "permission",
            }}
            onOpenSubagent={(id) => openPersistedSubagent(session.id, id)}
            tail={session.pendingAsk ? <AskUserPrompt key={session.id} session={session} /> : null}
          />
          {blocking && (
            <div class="mconv-blocking">
              {session.untrustedMcp && <McpBanner key={session.id} sessionId={session.id} />}
              {session.pendingPerm && <PermissionPrompt key={session.id} session={session} />}
            </div>
          )}
          {liveAgents.length > 0 && (
            <LiveDock
              agents={liveAgents}
              open={!!session.dockOpen}
              onToggle={(next) => updateSession(session.id, { dockOpen: next })}
              onOpen={(id, kind) => (kind === "bash"
                ? openBashJob(session.id, id)
                : openPersistedSubagent(session.id, id))}
              forceCompact={kbdOpen}
            />
          )}
          <MobileNowLine session={session} />
          <MobileComposer key={session.id} session={session} usage={usage} onSecret={setSecretAliases} />
        </>
      );
    }
  }

  return (
    <>
      {body}
      {session && (
        <MobileSheet
          open={secretAliases !== null}
          onClose={() => setSecretAliases(null)}
          title="Send secrets"
          scope="private"
        >
          <SecretBatch
            open={secretAliases !== null}
            sessionId={session.id}
            aliases={secretAliases || []}
            onClose={() => setSecretAliases(null)}
          />
        </MobileSheet>
      )}
      {session && (
        <RewindTimeline
          open={rewindOpen}
          onClose={() => setRewindOpen(false)}
          sessionId={session.id}
        />
      )}
      {session && (
        <LivePreview
          sessionId={session.id}
          open={!!session.previewOpen}
          onClose={() => updateSession(session.id, { previewOpen: false })}
        />
      )}
    </>
  );
}

function MobileSessionChrome({ version, forceMobile = false }) {
  const chrome = useStore((s) => selectMobileChrome(s, forceMobile));
  const [settingsOpen, setSettingsOpen] = useState(false);
  const settingsPendingRef = useRef(false);
  const setDrawerOpen = (next) => (next ? openDrawer("list") : closeDrawer());
  const onSelectFromDrawer = (id) => selectMobileDrawerSession(store.get().sessions[id], {
    resume: resumeSession,
    activate: setActiveSession,
    close: closeDrawer,
  });
  const onCreate = (cwd) => {
    closeDrawer();
    createSession({ cwd }).catch((e) =>
      addToast({ title: "Could not create session", detail: String(e.message || e), type: "error" })
    );
  };
  const onSettingsFromDrawer = () => {
    settingsPendingRef.current = true;
    closeDrawer();
  };
  const onDrawerClosed = () => {
    if (!settingsPendingRef.current) return;
    settingsPendingRef.current = false;
    setSettingsOpen(true);
  };

  return (
    <>
      {chrome.showChip && !chrome.inboxOpen && (
        <MobileTitleChip
          title={chrome.title}
          attention={chrome.attention}
          open={chrome.drawerOpen}
          onToggle={setDrawerOpen}
        />
      )}
      {chrome.showChip && !chrome.drawerOpen && !chrome.inboxOpen && (
        <MobileActionRail actions={[
          {
            id: "preview",
            icon: AppWindow,
            label: "Live preview",
            onClick: () => updateSession(chrome.activeId, { previewOpen: true }),
            active: chrome.previewOpen,
            visible: !!chrome.activeId,
          },
          {
            id: "inbox",
            icon: Inbox,
            label: "Inbox",
            badge: inboxPendingCount(chrome.inbox),
            onClick: toggleInbox,
            visible: chrome.inbox.length > 0,
          },
        ]} />
      )}
      {chrome.inboxOpen && (
        <MobileInboxView
          cards={chrome.inbox}
          onBack={closeInbox}
          onSend={(id, sessionId) => { routeEvent(id, sessionId).catch(() => {}); }}
          onNewSession={(id, spec) => { routeEventToNewSession(id, spec).catch(() => {}); }}
          onIgnore={(id) => { dismissEvent(id).catch(() => {}); }}
          onIgnoreSource={(source) => { dismissSource(source).catch(() => {}); }}
          onOpenSession={(id) => { closeInbox(); setActiveSession(id); }}
        />
      )}
      <SessionDrawer
        open={chrome.drawerOpen}
        step={chrome.drawerStep}
        onClose={() => setDrawerOpen(false)}
        onClosed={onDrawerClosed}
        active={chrome.active}
        newResults={chrome.newResults}
        saved={chrome.saved}
        activeCount={chrome.activeCount}
        savedCount={chrome.savedCount}
        projects={chrome.projects}
        onSelect={onSelectFromDrawer}
        onCreate={onCreate}
        onSettings={onSettingsFromDrawer}
        version={version}
        onCloseSession={(id) => { closeSession(id).catch(() => {}); }}
        onReopenSession={(id) => { resumeSession(id).catch(() => {}); }}
        onDeleteSession={(id) => { deleteSession(id).catch(() => {}); }}
        groupByProject={chrome.groupByProject}
        drawerCollapsed={chrome.drawerCollapsed}
        onGroupByProject={setGroupByProject}
        onToggleProject={setDrawerProjectCollapsed}
      />
      <MobileSheet
        open={settingsOpen}
        onClose={() => setSettingsOpen(false)}
        title="Settings"
        scope="everywhere"
      >
        <GlobalSettings soundEnabled={chrome.soundEnabled} version={version} />
      </MobileSheet>
    </>
  );
}
