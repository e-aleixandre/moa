import { useState, useEffect, useRef } from "preact/hooks";
import { Plus } from "lucide-preact";
import { store } from "../../../data/store.js";
import { updateSession } from "../../../data/store.js";
import { projectStream } from "../../../data/stream-model.js";
import { focusedSession, focusedSessionId } from "../../../data/selectors.js";
import { setActiveSession } from "../../../data/tile-actions.js";
import { openPalette } from "../../../data/palette.js";
import { openPersistedSubagent, archiveSession, deleteSession, resumeSession, createSession, rewindToMessage } from "../../../data/session-actions.js";
import { addToast } from "../../../data/notifications.js";
import { shortPath, sessionDotState, sessionTitle } from "../../../data/util/format.js";
import { activityPhase } from "../../../data/util/activity.js";
import { PermissionPrompt, AskUserPrompt, McpBanner, NotificationSettings } from "../../../components/index.js";
import { MobileComposer } from "../MobileComposer/MobileComposer.jsx";
import { MobileTitleChip } from "../MobileTitleChip/MobileTitleChip.jsx";
import { SessionDrawer } from "../SessionDrawer/SessionDrawer.jsx";
import { MobileSheet } from "../MobileSheet/MobileSheet.jsx";
import { RewindTimeline } from "../../RewindTimeline/RewindTimeline.jsx";
import { MobileStream } from "./MobileStream.jsx";
import { MobileNowLine } from "./MobileNowLine.jsx";
import { MobileSubagentView } from "./MobileSubagentView.jsx";
import { LiveDock } from "../../LiveDock/LiveDock.jsx";
import { liveTrayAgents } from "../../../data/stream-model.js";
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
// real shared components — so the screen no longer manages those overlays itself.
//
// Architecture (OPTION B): the mobile screen reuses the desktop's data
// projection (projectStream) and shared components; the only divergence is the
// mobile layout chrome (MobileStream renders the SAME tool-group card, just
// denser). No data logic is duplicated. The mock specimen used by the design
// gallery now lives in mobile-gallery.jsx (see MobileConversationSpecimen).

// relAge — coarse relative age from an `updated` epoch (mirrors the desktop
// ConversationScreen's spineSessions helper; kept local to avoid a shared
// import churn). "now" under a minute, then m/h/d.
function relAge(updated) {
  if (!updated) return "";
  const diff = Date.now() - updated;
  const min = Math.floor(diff / 60000);
  if (min < 1) return "now";
  if (min < 60) return `${min}m`;
  const h = Math.floor(min / 60);
  if (h < 24) return `${h}h`;
  const d = Math.floor(h / 24);
  return `${d}d`;
}

// sessionBrief derives the drawer card's "last" line from the real session
// fields the /api/sessions poll carries: the server-owned brief (progress →
// attempting) when present, else the live activity label, else the raw state.
// There is NO per-session "last message" field in the poll model, so we DO NOT
// invent a summary — we degrade to the brief, then to the activity/state.
// Saved sessions render NO brief (return ""): the grey StateDot and the drawer's
// group counter already carry the "saved" state, so a "Saved" line is redundant.
// // TODO(5x): a true last-message preview would need the projection or the API
// to carry one; not added here (out of scope, would touch the backend/model).
function sessionBrief(sess) {
  if (sess.briefProgress) return sess.briefProgress;
  if (sess.briefAttempting) return sess.briefAttempting;
  if (sess.error) return sess.error;
  const phase = activityPhase(sess);
  if (phase === "waiting") return "Waiting for you";
  if (sess.state === "running") return "Working…";
  if (sess.state === "saved") return "";
  return sess.state || "idle";
}

// aggregateAttention counts OTHER sessions (excluding the active one) that are
// blocked on the user — the exact "needs you" datum the desktop GridToolbar's
// attn-lamp consumes (permission ∪ error), so the mobile Sessions badge and the
// desktop lamp never disagree. It reuses the same per-session "needs" predicate
// the drawer cards use (pendingPerm / pendingAsk / permission), plus error.
function aggregateAttention(sessions, activeId) {
  return Object.values(sessions).filter(
    (s) =>
      !s.archived &&
      s.id !== activeId &&
      (s.pendingPerm || s.pendingAsk || s.state === "permission" || s.state === "error")
  ).length;
}

// drawerSessions builds the drawer's two groups — active (newest first) and
// saved — kept apart rather than concatenated because the drawer labels them
// separately, exactly as the desktop Spine does.
function drawerSessions(sessions, activeId) {
  const all = Object.values(sessions).filter((s) => !s.archived);
  const active = all
    .filter((s) => s.state !== "saved")
    .sort((a, b) => (b.updated || 0) - (a.updated || 0));
  const saved = all
    .filter((s) => s.state === "saved")
    .sort((a, b) => (b.updated || 0) - (a.updated || 0));
  const toCard = (s) => {
    const needs = !!(s.pendingPerm || s.pendingAsk || s.state === "permission");
    return {
      id: s.id,
      title: sessionTitle(s),
      state: sessionDotState(s),
      when: relAge(s.updated),
      last: sessionBrief(s),
      needsLabel: needs ? "Needs you:" : undefined,
      path: shortPath(s.cwd) || s.cwd || "",
      unseen: !!s.unseen,
      active: s.id === activeId,
      saved: s.state === "saved",
    };
  };
  return {
    active: active.map(toCard),
    saved: saved.map(toCard),
    activeCount: active.length,
    savedCount: saved.length,
  };
}

// drawerProjects — the folders you already have sessions in, most recent first.
// That list IS the mobile "pick a project" surface: no directory explorer here
// (see NewSessionView), so the only thing that matters is that a folder you have
// worked in is one tap away.
function drawerProjects(sessions) {
  const byCwd = {};
  for (const s of Object.values(sessions)) {
    const cwd = s.cwd || "";
    if (!cwd) continue;
    const updated = s.updated || 0;
    if (!byCwd[cwd] || updated > byCwd[cwd].updated) byCwd[cwd] = { cwd, updated };
  }
  return Object.values(byCwd).sort((a, b) => b.updated - a.updated);
}

// recentSavedSessions builds the 3 most-recent saved sessions for the empty
// state's RECENT list (EMPTY-STATE-SPEC §2.2). Same card fields the drawer uses,
// trimmed to what the compact row shows.
function recentSavedSessions(sessions, limit = 3) {
  return Object.values(sessions)
    .filter((s) => !s.archived && s.state === "saved")
    .sort((a, b) => (b.updated || 0) - (a.updated || 0))
    .slice(0, limit)
    .map((s) => ({
      id: s.id,
      title: sessionTitle(s),
      when: relAge(s.updated),
      path: shortPath(s.cwd) || s.cwd || "",
    }));
}

export function MobileConversationScreen() {
  const [state, setState] = useState(store.get());
  useEffect(() => store.subscribe(setState), []);

  const [drawerOpen, setDrawerOpen] = useState(false);
  const [rewindOpen, setRewindOpen] = useState(false);
  // Global Settings bottom-sheet, reached from the drawer footer via a sheet
  // HANDOFF: tapping ⚙ closes the drawer, and only once the drawer's leave
  // animation has settled (onClosed) does the Settings sheet slide up — one
  // overlay at a time, never stacked (same pattern as the status line's Rewind
  // handoff). A plain drawer close leaves the pending flag false and hands
  // nothing off. Closing Settings returns to the conversation, not the drawer.
  const [settingsOpen, setSettingsOpen] = useState(false);
  const settingsPendingRef = useRef(false);

  const session = focusedSession(state);
  const activeId = focusedSessionId(state);
  const loaded = state.sessionsLoaded;

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
    const onResize = () => setKbdOpen(window.innerHeight - vv.height > 150);
    vv.addEventListener("resize", onResize);
    onResize();
    return () => vv.removeEventListener("resize", onResize);
  }, []);

  const onSelectFromDrawer = (id) => { setActiveSession(id); setDrawerOpen(false); };
  // The empty state has no drawer to host the "new session" screen, so there it
  // still opens the palette straight on its create step.
  const onNew = () => { openPalette("create"); setDrawerOpen(false); };
  // From inside the drawer, creating is done in place (NewSessionView) — this
  // just performs it and closes.
  const onCreate = (cwd) => {
    setDrawerOpen(false);
    createSession({ cwd }).catch((e) =>
      addToast({ title: "Could not create session", detail: String(e.message || e), type: "error" })
    );
  };
  const onSettingsFromDrawer = () => {
    settingsPendingRef.current = true;
    setDrawerOpen(false);
  };
  const onDrawerClosed = () => {
    if (!settingsPendingRef.current) return;
    settingsPendingRef.current = false;
    setSettingsOpen(true);
  };

  useEffect(() => { setRewindOpen(false); }, [activeId]);

  // Aggregate cross-session attention for the title chip's dot: OTHER sessions
  // blocked on the user (excludes the active one, whose block is the inline
  // PermissionPrompt in the conversation).
  const attnCount = aggregateAttention(state.sessions, activeId);
  const {
    active: drawerActive,
    saved: drawerSaved,
    activeCount,
    savedCount,
  } = drawerSessions(state.sessions, activeId);

  let body;
  if (!loaded) {
    body = <div class="mconv-placeholder">Loading sessions…</div>;
  } else if (!session) {
    const recents = recentSavedSessions(state.sessions);
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
              onClick={() => setDrawerOpen(true)}
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
      // 5J: the subagent view takes over the whole conversation surface (below
      // the header/strip), pushed full-screen. onBack clears viewingSubagent.
      body = (
        <MobileSubagentView
          key={session.viewingSubagent}
          session={session}
          jobId={session.viewingSubagent}
          onBack={() => updateSession(session.id, { viewingSubagent: null })}
        />
      );
    } else {
      body = (
        <>
          <MobileStream
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
              onOpen={(id) => openPersistedSubagent(session.id, id)}
              forceCompact={kbdOpen}
            />
          )}
          <MobileNowLine session={session} />
          <MobileComposer key={session.id} session={session} usage={state.usage} />
        </>
      );
    }
  }

  return (
    <div class="mconv">
      {body}

      {/* The title chip floats over the transcript and is the door to the
          session list. It is hidden inside a subagent, which is a full-screen
          push carrying its own header and its own way back. */}
      {session && !session.viewingSubagent && (
        <MobileTitleChip
          title={sessionTitle(session)}
          attnCount={attnCount}
          open={drawerOpen}
          onToggle={setDrawerOpen}
        />
      )}

      <SessionDrawer
        open={drawerOpen}
        onClose={() => setDrawerOpen(false)}
        onClosed={onDrawerClosed}
        active={drawerActive}
        saved={drawerSaved}
        activeCount={activeCount}
        savedCount={savedCount}
        projects={drawerProjects(state.sessions)}
        onSelect={onSelectFromDrawer}
        onCreate={onCreate}
        onSettings={onSettingsFromDrawer}
        onCloseSession={(id) => archiveSession(id)}
        onReopenSession={(id) => resumeSession(id)}
        onDeleteSession={(id) => deleteSession(id)}
      />
      <MobileSheet
        open={settingsOpen}
        onClose={() => setSettingsOpen(false)}
        title="Settings"
        scope="everywhere"
      >
        <div class="mconv-settings-body">
          <div class="mconv-settings-lbl">Notifications</div>
          <NotificationSettings soundEnabled={state.soundEnabled} />
        </div>
      </MobileSheet>
      {session && (
        <RewindTimeline
          open={rewindOpen}
          onClose={() => setRewindOpen(false)}
          sessionId={session.id}
        />
      )}
    </div>
  );
}
