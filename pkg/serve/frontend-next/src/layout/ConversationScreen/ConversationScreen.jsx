import { useState, useEffect, useRef } from "preact/hooks";
import { Spine } from "../Spine/Spine.jsx";
import { ChatHead } from "../ChatHead/ChatHead.jsx";
import { Stream } from "../Stream/Stream.jsx";
import { AgentTray } from "../AgentTray/AgentTray.jsx";
import { SubagentView } from "../SubagentView/SubagentView.jsx";
import { Composer } from "../Composer/Composer.jsx";
import { StatusStrip } from "../StatusStrip/StatusStrip.jsx";
import { RewindTimeline } from "../RewindTimeline/RewindTimeline.jsx";
import { ModelSelector, PermissionPrompt, AskUserPrompt, McpBanner, NotificationSettings, UsagePanel } from "../../components/index.js";
import { Button, Kbd } from "../../primitives/index.js";
import { store, updateSession } from "../../data/store.js";
import { projectStream, liveTrayAgents } from "../../data/stream-model.js";
import { focusedSession, focusedSessionId, modelAccent, deriveModelSpecs, matchSelectedModel, nextThinkingLevel } from "../../data/selectors.js";
import { openSession } from "../../data/tile-actions.js";
import { navigate } from "../../data/router.js";
import { openPalette } from "../../data/palette.js";
import { registerOverlay } from "../../data/overlays.js";
import { shortModel, shortPath, modelCodename } from "../../data/util/format.js";
import { fmtCost } from "../../data/util/usage-pills.js";
import { activityPhase, activityText, formatElapsed } from "../../data/util/activity.js";
import { formatShortcut } from "../../data/util/shortcut.js";
import { Plus } from "lucide-preact";
import { api } from "../../data/api.js";
import { configureSession, archiveSession, unarchiveSession, openPersistedSubagent } from "../../data/session-actions.js";
import "./ConversationScreen.css";

// ConversationScreen — root organism AND container of the desktop conversation
// screen. This is the 5C wiring pattern (the standard for the following
// subphases): ONE component per screen subscribes to the store, derives the
// focused session, and passes real props DOWN to the presentational children
// (Spine/ChatHead/Stream/StatusStrip). The children stay dumb — they never read
// the store themselves. App owns the bootstrap (polling/WS/version); this
// container owns only the read-side projection.
//
// Three states: LOADING (sessions not fetched yet), EMPTY (no focused session),
// and a normal shown session.
//
// AgentTray (5J) and Composer (5D) are left with their own mock/empty render in
// 5C — not connected. PermissionCard/AskUserCard (5F) are not rendered by
// Stream yet.

// spineSessions splits the store's sessions into the Spine's ACTIVE and SAVED
// lists. Active = not 'saved' and not archived, ordered by `updated` desc.
// Saved = state 'saved'. Titles fall back to the id so a not-yet-titled session
// still renders. `meta` is a coarse relative age placeholder derived from
// `updated` (full relative-time formatting is a later polish, kept simple here).
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

function spineSessions(sessions) {
  const all = Object.values(sessions).filter((s) => !s.archived);
  const active = all
    .filter((s) => s.state !== "saved")
    .sort((a, b) => (b.updated || 0) - (a.updated || 0))
    .map((s) => ({
      id: s.id,
      title: s.title || s.id,
      state: s.state || "idle",
      unseen: !!s.unseen,
      meta: relAge(s.updated),
    }));
  const saved = all
    .filter((s) => s.state === "saved")
    .sort((a, b) => (b.updated || 0) - (a.updated || 0))
    .map((s) => ({ id: s.id, title: s.title || s.id, meta: relAge(s.updated) }));
  return { active, saved };
}

// currentActivity derives the StatusStrip's activity label from the shared
// activityText resolver: the synthesized action while the agent works (e.g.
// "Running tests", "Editing code"), the fixed phase copy for special phases,
// with an elapsed timer appended while running; nothing when idle. The task
// title is deliberately NOT shown here — task progress lives in the N/M tasks
// pill. `nowMs` is the ticking clock (see ConversationScreen's interval) so the
// timer advances on its own — its origin is always the server-stamped
// runStartedAtMs, never a client Date.now() start.
function currentActivity(session, nowMs) {
  const label = activityText(session);
  if (!label) return undefined;
  const phase = activityPhase(session);
  const runStartedAtMs = session.runStartedAtMs || 0;
  // Show the timer only for the running phases, not the momentary
  // compacting/verifying/waiting states where an age counter reads oddly.
  const showTimer = runStartedAtMs > 0 && (phase === "thinking" || phase === "working");
  if (showTimer) {
    const elapsedText = formatElapsed(Math.max(0, nowMs - runStartedAtMs));
    return elapsedText ? `${label} · ${elapsedText}` : label;
  }
  return label;
}

function fmtSpend(costUSD) {
  if (!costUSD || costUSD <= 0) return undefined;
  return fmtCost(costUSD);
}

export function ConversationScreen({ version }) {
  const [state, setState] = useState(store.get());
  useEffect(() => store.subscribe(setState), []);

  const session = focusedSession(state);
  const activeId = focusedSessionId(state);
  const { active, saved } = spineSessions(state.sessions);
  const loaded = state.sessionsLoaded;

  // Activity clock: while the focused session shows live activity, tick once a
  // second so the StatusStrip's elapsed timer advances on its own. The timer
  // origin is the server-stamped runStartedAtMs (read in currentActivity), not
  // this clock — the clock only supplies "now".
  const activityActive = activityPhase(session) !== null;
  const [nowMs, setNowMs] = useState(() => Date.now());
  useEffect(() => {
    if (!activityActive) return;
    setNowMs(Date.now());
    const t = setInterval(() => setNowMs(Date.now()), 1000);
    return () => clearInterval(t);
  }, [activityActive]);

  // onSelectSession routes a Spine click to the focused tile (desktop) via
  // openSession, which leaves the tile showing that session. // 5G: the next's
  // own pane model replaces the tile tree; until then we reuse it verbatim.
  const onSelectSession = (id) => { openSession(id); };

  // --- Model selector popover (ChatHead's ModelPill) ---
  const [modelOpen, setModelOpen] = useState(false);
  const [models, setModels] = useState(null); // null = not fetched yet
  const modelAnchorRef = useRef(null);
  useEffect(() => {
    if (!modelOpen || models) return;
    api("GET", "/api/models").then(setModels).catch(() => setModels([]));
  }, [modelOpen, models]);
  useEffect(() => {
    if (!modelOpen) return;
    const unregister = registerOverlay("conv-model-popover");
    const onDocDown = (e) => {
      if (modelAnchorRef.current && !modelAnchorRef.current.contains(e.target)) setModelOpen(false);
    };
    const onKeyDown = (e) => { if (e.key === "Escape") setModelOpen(false); };
    document.addEventListener("mousedown", onDocDown);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      unregister();
      document.removeEventListener("mousedown", onDocDown);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [modelOpen]);

  // --- Session settings popover (ChatHead's MoreHorizontal) ---
  const [settingsOpen, setSettingsOpen] = useState(false);
  const settingsAnchorRef = useRef(null);
  useEffect(() => {
    if (!settingsOpen) return;
    const unregister = registerOverlay("conv-settings-popover");
    const onDocDown = (e) => {
      if (settingsAnchorRef.current && !settingsAnchorRef.current.contains(e.target)) setSettingsOpen(false);
    };
    const onKeyDown = (e) => { if (e.key === "Escape") setSettingsOpen(false); };
    document.addEventListener("mousedown", onDocDown);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      unregister();
      document.removeEventListener("mousedown", onDocDown);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [settingsOpen]);
  // Close both popovers when the focused session changes, so a stale popover
  // from a different session doesn't linger visually.
  useEffect(() => {
    setModelOpen(false);
    setSettingsOpen(false);
    setNotifOpen(false);
  }, [activeId]);

  // --- Notifications popover (ChatHead's Bell) — device-wide push + sound (5N).
  // Not session-scoped, but anchored in the head next to the other popovers so
  // it inherits the same click-outside + Escape wiring.
  const [notifOpen, setNotifOpen] = useState(false);
  const notifAnchorRef = useRef(null);
  useEffect(() => {
    if (!notifOpen) return;
    const unregister = registerOverlay("conv-notif-popover");
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

  // --- Rewind timeline sheet (ChatHead/MobileHeader's Rewind button) ---
  const [rewindOpen, setRewindOpen] = useState(false);
  useEffect(() => { setRewindOpen(false); }, [activeId]);

  // --- Usage panel popover (StatusStrip's cost segment) — level 2 telemetry
  // (TELEMETRY-SETTINGS-REDESIGN §2). Anchored to the strip, not the head, but
  // reuses the exact same click-outside + Escape wiring as the head popovers.
  const [usageOpen, setUsageOpen] = useState(false);
  const usageAnchorRef = useRef(null);
  useEffect(() => { setUsageOpen(false); }, [activeId]);
  useEffect(() => {
    if (!usageOpen) return;
    const unregister = registerOverlay("conv-usage-popover");
    const onDocDown = (e) => {
      if (usageAnchorRef.current && !usageAnchorRef.current.contains(e.target)) setUsageOpen(false);
    };
    const onKeyDown = (e) => { if (e.key === "Escape") setUsageOpen(false); };
    document.addEventListener("mousedown", onDocDown);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      unregister();
      document.removeEventListener("mousedown", onDocDown);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [usageOpen]);

  const spine = (
    <Spine
      version={version}
      activeSessions={active}
      savedSessions={saved}
      activeId={activeId}
      onSelectSession={onSelectSession}
      onNewSession={() => openPalette("create")}
      onSearch={() => openPalette("search")}
      onSettings={() => { /* 5x: settings */ }}
    />
  );

  let body;
  if (!loaded) {
    body = <div class="conversation-placeholder">Loading sessions…</div>;
  } else if (!session) {
    body = (
      <div class="conversation-empty">
        <span class="conversation-empty-glyph" aria-hidden="true">m</span>
        <p class="conversation-empty-title">No session open</p>
        <p class="conversation-empty-hint">
          Pick a session from the sidebar, or press{" "}
          <Kbd>{formatShortcut("K", { mod: true })}</Kbd> to jump.
        </p>
        <div class="conversation-empty-actions">
          <Button variant="solid" size="md" onClick={() => openPalette("create")}>
            <Plus size={14} aria-hidden="true" /> New session
          </Button>
        </div>
      </div>
    );
  } else {
    const blocks = projectStream(session);
    const specs = deriveModelSpecs(models);
    const selectedModel = matchSelectedModel(specs, session.model);
    const thinking = session.thinking === "none" ? "off" : (session.thinking || "off");
    const settingsBusy = session.state === "running" || session.state === "permission";
    // 5J: when a subagent is being viewed, the SubagentView takes over the main
    // column (in place of the parent stream/composer/status). Its jobId must
    // still exist in the session (the view itself rebounds to null via onBack if
    // it was pruned).
    const viewingSub = session.viewingSubagent;

    const modelPopover = modelOpen && (
      <div class="head-popover">
        <ModelSelector
          models={specs}
          selected={selectedModel}
          thinking={thinking}
          onSelect={(spec) => configureSession(session.id, { model: spec })}
          onThinkingChange={(value) => configureSession(session.id, { thinking: value })}
        />
      </div>
    );

    const settingsPopover = settingsOpen && (
      <div class="head-popover session-settings-popover">
        {settingsBusy && (
          <div class="session-settings-busy">Settings locked while agent is running</div>
        )}
        <Button
          variant="ghost"
          size="sm"
          className="session-settings-archive"
          onClick={() => {
            const action = session.archived ? unarchiveSession(session.id) : archiveSession(session.id);
            Promise.resolve(action).finally(() => setSettingsOpen(false));
          }}
        >
          {session.archived ? "Reopen session" : "Close session"}
        </Button>
      </div>
    );

    const notifPopover = notifOpen && (
      <div class="head-popover">
        <NotificationSettings soundEnabled={state.soundEnabled} />
      </div>
    );

    body = (
      <>
        <ChatHead
          title={session.title || session.id}
          state={session.state || "idle"}
          path={shortPath(session.cwd) || session.cwd || ""}
          model={modelCodename(session.model) || shortModel(session.model) || session.model || ""}
          modelAccent={modelAccent(session.model)}
          thinkingLevel={thinking}
          onTitleClick={() => { /* 5x: rename / session menu */ }}
          onGridToggle={() => navigate("grid")}
          onRewind={() => setRewindOpen(true)}
          rewindDisabled={settingsBusy}
          onNotifications={() => setNotifOpen((v) => !v)}
          onSessionSettings={() => setSettingsOpen((v) => !v)}
          onModelClick={() => setModelOpen((v) => !v)}
          onModelMeterClick={() => configureSession(session.id, { thinking: nextThinkingLevel(thinking) })}
          modelPopover={modelPopover}
          settingsPopover={settingsPopover}
          notifPopover={notifPopover}
          modelAnchorRef={modelAnchorRef}
          settingsAnchorRef={settingsAnchorRef}
          notifAnchorRef={notifAnchorRef}
        />
        {viewingSub ? (
          <SubagentView
            key={viewingSub}
            session={session}
            jobId={viewingSub}
            onBack={() => updateSession(session.id, { viewingSubagent: null })}
          />
        ) : (
          <>
            <Stream
              session={session}
              blocks={blocks}
              onOpenSubagent={(id) => openPersistedSubagent(session.id, id)}
            />
            {(session.untrustedMcp || session.pendingPerm || session.pendingAsk) && (
              <div class="conversation-blocking">
                {session.untrustedMcp && <McpBanner key={session.id} sessionId={session.id} />}
                {session.pendingPerm && <PermissionPrompt key={session.id} session={session} />}
                {session.pendingAsk && <AskUserPrompt key={session.id} session={session} />}
              </div>
            )}
            {/* 5J: AgentTray — sticky mirror of the live fanout, connected to
                the session's live subagents/bash jobs; a chip opens its
                SubagentView (INC-06). */}
            <AgentTray
              agents={liveTrayAgents(session)}
              onOpen={(id) => openPersistedSubagent(session.id, id)}
            />
            <Composer key={session.id} sessionId={session.id} session={session} />
            <div class="status-strip-anchor" ref={usageAnchorRef}>
              <StatusStrip
                ctxPercent={session.contextPercent}
                tokensUp={session.runTokensUp}
                tokensDown={session.runTokensDown}
                task={currentActivity(session, nowMs)}
                spend={fmtSpend(session.costUSD)}
                session={session}
                usage={state.usage}
                onOpenUsage={() => setUsageOpen((v) => !v)}
                onPermChange={(mode) => configureSession(session.id, { permissionMode: mode })}
                permBusy={settingsBusy}
                showTokens={true}
              />
              {usageOpen && (
                <div class="status-strip-usage-popover">
                  <UsagePanel
                    session={session}
                    usage={state.usage}
                    ctxPercent={session.contextPercent}
                    costUSD={session.costUSD}
                  />
                </div>
              )}
            </div>
          </>
        )}
      </>
    );
  }

  return (
    <div class="conversation-screen">
      {spine}
      <main class="conversation-main">{body}</main>
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
