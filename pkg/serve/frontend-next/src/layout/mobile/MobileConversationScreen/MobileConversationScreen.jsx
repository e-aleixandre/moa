import { useState, useEffect, useRef } from "preact/hooks";
import { Plus } from "lucide-preact";
import { store } from "../../../data/store.js";
import { updateSession } from "../../../data/store.js";
import { projectStream } from "../../../data/stream-model.js";
import { focusedSession, focusedSessionId, deriveModelSpecs, matchSelectedModel } from "../../../data/selectors.js";
import { setActiveSession } from "../../../data/tile-actions.js";
import { openPalette } from "../../../data/palette.js";
import { openPersistedSubagent, configureSession, archiveSession, deleteSession, resumeSession } from "../../../data/session-actions.js";
import { api } from "../../../data/api.js";
import { mobileModelLabel, shortPath, sessionDotState } from "../../../data/util/format.js";
import { activityPhase } from "../../../data/util/activity.js";
import { Composer } from "../../Composer/Composer.jsx";
import { PermissionPrompt, AskUserPrompt, McpBanner, ModelSelector, Sheet } from "../../../components/index.js";
import { MobileHeader } from "../MobileHeader/MobileHeader.jsx";
import { SessionStrip } from "../SessionStrip/SessionStrip.jsx";
import { MobileComposer } from "../MobileComposer/MobileComposer.jsx";
import { SessionDrawer } from "../SessionDrawer/SessionDrawer.jsx";
import { NotificationSettings } from "../../../components/index.js";
import { registerOverlay } from "../../../data/overlays.js";
import { RewindTimeline } from "../../RewindTimeline/RewindTimeline.jsx";
import { MobileStream } from "./MobileStream.jsx";
import { MobileSubagentView } from "./MobileSubagentView.jsx";
import "./MobileConversationScreen.css";

// MobileConversationScreen — the CONNECTED root container of the mobile
// conversation screen (5I). It replaces the 4A mock (hardcoded SESSIONS /
// READ_ROWS / conversation) with the SAME store-driven wiring as the desktop
// ConversationScreen (5C): subscribe to the store, derive the focused (active)
// session, project its stream, and pass real props down to the presentational
// mobile chrome (header / strip / drawer) + the SHARED content components (via
// MobileStream) + the REAL Composer.
//
// Architecture (OPTION B): the mobile screen reuses the desktop's data
// projection (projectStream) and shared components; the only divergence is the
// mobile layout chrome and the ledger→MobileLedger remap (MobileStream). No
// data logic is duplicated. The mock specimen used by the design gallery now
// lives in mobile-gallery.jsx (see MobileConversationSpecimen there).

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

// stripSessions builds the horizontal SessionStrip's chip list: active (non-
// saved, non-archived) sessions, newest first. `needs` = a blocking prompt.
function stripSessions(sessions) {
  return Object.values(sessions)
    .filter((s) => !s.archived && s.state !== "saved")
    .sort((a, b) => (b.updated || 0) - (a.updated || 0))
    .map((s) => ({
      id: s.id,
      name: s.title || s.id,
      state: sessionDotState(s),
      unseen: !!s.unseen,
      needs: !!(s.pendingPerm || s.pendingAsk || s.state === "permission"),
    }));
}

// drawerSessions builds the full bottom-sheet list: active first (newest), then
// saved. activeCount/savedCount come from the two groups.
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
      title: s.title || s.id,
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
    list: [...active, ...saved].map(toCard),
    activeCount: active.length,
    savedCount: saved.length,
  };
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
      title: s.title || s.id,
      when: relAge(s.updated),
      path: shortPath(s.cwd) || s.cwd || "",
    }));
}

export function MobileConversationScreen() {
  const [state, setState] = useState(store.get());
  useEffect(() => store.subscribe(setState), []);

  const [drawerOpen, setDrawerOpen] = useState(false);
  const [rewindOpen, setRewindOpen] = useState(false);
  const [notifOpen, setNotifOpen] = useState(false);
  const notifAnchorRef = useRef(null);
  // Model + thinking sheet (TELEMETRY-SETTINGS-REDESIGN §3.1): the header
  // ModelPill is tappable on mobile too, opening the shared ModelSelector inside
  // a Sheet (which brings overlay-history / back-gesture / scroll-lock). Models
  // are fetched lazily on first open, same as the desktop popover.
  const [modelOpen, setModelOpen] = useState(false);
  const [models, setModels] = useState(null); // null = not fetched yet
  useEffect(() => {
    if (!modelOpen || models) return;
    api("GET", "/api/models").then(setModels).catch(() => setModels([]));
  }, [modelOpen, models]);

  const session = focusedSession(state);
  const activeId = focusedSessionId(state);
  const loaded = state.sessionsLoaded;

  const onSelect = (id) => setActiveSession(id);
  const onSelectFromDrawer = (id) => { setActiveSession(id); setDrawerOpen(false); };
  const onNew = () => openPalette("create");

  useEffect(() => { setRewindOpen(false); setModelOpen(false); }, [activeId]);

  // Notifications popover (Bell in the header) — device-wide push + sound (5N).
  // Same click-outside + Escape wiring as the desktop head popovers.
  useEffect(() => {
    if (!notifOpen) return;
    const unregister = registerOverlay("mconv-notif-popover");
    const onDocDown = (e) => {
      if (notifAnchorRef.current && !notifAnchorRef.current.contains(e.target)) setNotifOpen(false);
    };
    const onKeyDown = (e) => { if (e.key === "Escape") setNotifOpen(false); };
    document.addEventListener("mousedown", onDocDown);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      unregister();
      document.removeEventListener("mousedown", onDocDown);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [notifOpen]);

  const strip = stripSessions(state.sessions);
  const { list: drawerList, activeCount, savedCount } = drawerSessions(state.sessions, activeId);

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
    const blocking =
      session.untrustedMcp || session.pendingPerm || session.pendingAsk;
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
            onOpenSubagent={(id) => openPersistedSubagent(session.id, id)}
          />
          {blocking && (
            <div class="mconv-blocking">
              {session.untrustedMcp && <McpBanner key={session.id} sessionId={session.id} />}
              {session.pendingPerm && <PermissionPrompt key={session.id} session={session} />}
              {session.pendingAsk && <AskUserPrompt key={session.id} session={session} />}
            </div>
          )}
          <MobileComposer key={session.id} session={session} usage={state.usage} />
        </>
      );
    }
  }

  return (
    <div class="mconv">
      <MobileHeader
        state={session ? session.state || "idle" : "idle"}
        title={session ? session.title || session.id : "moa"}
        model={session ? mobileModelLabel(session.model) : ""}
        level={session ? (session.thinking === "none" ? "off" : (session.thinking || "off")) : "off"}
        path={session ? shortPath(session.cwd) || session.cwd || "" : ""}
        ctx={session ? session.contextPercent : undefined}
        onOpenSessions={() => setDrawerOpen(true)}
        onRewind={session ? () => setRewindOpen(true) : undefined}
        rewindDisabled={session ? session.state === "running" || session.state === "permission" : true}
        onNotifications={() => setNotifOpen((v) => !v)}
        notifAnchorRef={notifAnchorRef}
        notifPopover={notifOpen && <NotificationSettings soundEnabled={state.soundEnabled} />}
        onModelClick={session ? () => setModelOpen(true) : undefined}
        empty={loaded && !session}
      />
      {strip.length > 1 && (
        <SessionStrip
          sessions={strip}
          activeId={activeId}
          onSelect={onSelect}
          onNew={onNew}
        />
      )}

      {body}

      <SessionDrawer
        open={drawerOpen}
        onClose={() => setDrawerOpen(false)}
        sessions={drawerList}
        activeCount={activeCount}
        savedCount={savedCount}
        onSelect={onSelectFromDrawer}
        onNew={onNew}
        onCloseSession={(id) => archiveSession(id)}
        onReopenSession={(id) => resumeSession(id)}
        onDeleteSession={(id) => deleteSession(id)}
      />
      {session && (
        <RewindTimeline
          open={rewindOpen}
          onClose={() => setRewindOpen(false)}
          sessionId={session.id}
        />
      )}
      {session && (() => {
        const specs = deriveModelSpecs(models);
        const thinking = session.thinking === "none" ? "off" : (session.thinking || "off");
        return (
          <Sheet open={modelOpen} onClose={() => setModelOpen(false)} title="Model & thinking">
            <ModelSelector
              models={specs}
              selected={matchSelectedModel(specs, session.model)}
              thinking={thinking}
              embedded
              onSelect={(spec) => configureSession(session.id, { model: spec })}
              onThinkingChange={(value) => configureSession(session.id, { thinking: value })}
            />
          </Sheet>
        );
      })()}
    </div>
  );
}
