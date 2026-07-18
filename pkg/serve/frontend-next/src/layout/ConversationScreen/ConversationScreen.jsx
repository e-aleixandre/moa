import { useState, useEffect, useRef } from "preact/hooks";
import { Spine } from "../Spine/Spine.jsx";
import { ChatHead } from "../ChatHead/ChatHead.jsx";
import { Stream } from "../Stream/Stream.jsx";
import { AgentTray } from "../AgentTray/AgentTray.jsx";
import { Composer } from "../Composer/Composer.jsx";
import { StatusStrip } from "../StatusStrip/StatusStrip.jsx";
import { ModelSelector, PermissionPrompt, AskUserPrompt, McpBanner, Segmented } from "../../components/index.js";
import { Button } from "../../primitives/index.js";
import { store } from "../../data/store.js";
import { projectStream } from "../../data/stream-model.js";
import { focusedSession, focusedSessionId, modelAccent } from "../../data/selectors.js";
import { openSession } from "../../data/tile-actions.js";
import { shortModel, shortPath } from "../../data/util/format.js";
import { formatElapsed } from "../../data/util/activity.js";
import { activityPhase, activityLabel } from "../../data/util/activity.js";
import { api } from "../../data/api.js";
import { configureSession, archiveSession, unarchiveSession } from "../../data/session-actions.js";
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

// currentTask derives the StatusStrip's task label: the first not-done task if
// the session tracks tasks, else the live activity label (gerund/phase) plus an
// elapsed timer anchored to `runStartedAtMs`, else nothing (hidden rather than
// invented). `nowMs` is the ticking clock (see ConversationScreen's interval)
// so the gerund rotation and the timer advance on their own — the timer origin
// is always the server-stamped runStartedAtMs, never a client Date.now() start.
function currentTask(session, nowMs) {
  const tasks = session.tasks || [];
  const pending = tasks.find((t) => t.status !== "done");
  if (pending) return pending.title;
  const phase = activityPhase(session);
  if (!phase) return undefined;
  const runStartedAtMs = session.runStartedAtMs || 0;
  const elapsedMs = runStartedAtMs ? Math.max(0, nowMs - runStartedAtMs) : 0;
  const label = activityLabel(phase, elapsedMs);
  // Show the timer only for the running phases, not the momentary
  // compacting/verifying/waiting states where an age counter reads oddly.
  const showTimer = runStartedAtMs > 0 && (phase === "thinking" || phase === "working");
  if (showTimer) {
    const elapsedText = formatElapsed(elapsedMs);
    return elapsedText ? `${label} · ${elapsedText}` : label;
  }
  return label;
}

function fmtSpend(costUSD) {
  if (!costUSD || costUSD <= 0) return undefined;
  return `$${costUSD.toFixed(2)}`;
}

// deriveModelSpecs maps /api/models entries ({id, name, provider, alias?})
// into the shape ModelSelector expects ({id, name, desc, sigil, accent}). `id`
// here is the full "provider/id" spec configureSession sends over the wire —
// matches the old SettingsDropdown's `m.provider + '/' + m.id`.
function deriveModelSpecs(models) {
  return (models || []).map((m) => ({
    id: `${m.provider}/${m.id}`,
    name: m.name,
    desc: m.alias || m.provider,
    sigil: (m.name || m.id || "?").charAt(0).toUpperCase(),
    accent: modelAccent(m.name),
  }));
}

// matchSelectedModel finds the spec whose display name matches the session's
// current model string (session.model is the display name the backend
// reports, e.g. "GPT-5.6 Sol" — not the "provider/id" spec).
function matchSelectedModel(specs, sessionModel) {
  if (!sessionModel) return undefined;
  const short = shortModel(sessionModel);
  const found = specs.find((s) => s.name === sessionModel || s.name === short);
  return found?.id;
}

const PERMISSION_MODE_OPTIONS = [
  { value: "yolo", label: "YOLO" },
  { value: "ask", label: "ASK" },
  { value: "auto", label: "AUTO" },
];

export function ConversationScreen({ version }) {
  const [state, setState] = useState(store.get());
  useEffect(() => store.subscribe(setState), []);

  const session = focusedSession(state);
  const activeId = focusedSessionId(state);
  const { active, saved } = spineSessions(state.sessions);
  const loaded = state.sessionsLoaded;

  // Activity clock: while the focused session shows live activity, tick once a
  // second so the StatusStrip's gerund rotation and elapsed timer advance on
  // their own. The timer origin is the server-stamped runStartedAtMs (read in
  // currentTask), not this clock — the clock only supplies "now".
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
    const onDocDown = (e) => {
      if (modelAnchorRef.current && !modelAnchorRef.current.contains(e.target)) setModelOpen(false);
    };
    const onKeyDown = (e) => { if (e.key === "Escape") setModelOpen(false); };
    document.addEventListener("mousedown", onDocDown);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("mousedown", onDocDown);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [modelOpen]);

  // --- Session settings popover (ChatHead's MoreHorizontal) ---
  const [settingsOpen, setSettingsOpen] = useState(false);
  const settingsAnchorRef = useRef(null);
  useEffect(() => {
    if (!settingsOpen) return;
    const onDocDown = (e) => {
      if (settingsAnchorRef.current && !settingsAnchorRef.current.contains(e.target)) setSettingsOpen(false);
    };
    const onKeyDown = (e) => { if (e.key === "Escape") setSettingsOpen(false); };
    document.addEventListener("mousedown", onDocDown);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("mousedown", onDocDown);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [settingsOpen]);
  // Close both popovers when the focused session changes, so a stale popover
  // from a different session doesn't linger visually.
  useEffect(() => {
    setModelOpen(false);
    setSettingsOpen(false);
  }, [activeId]);

  const spine = (
    <Spine
      version={version?.current ? `v${version.current}` : undefined}
      activeSessions={active}
      savedSessions={saved}
      activeId={activeId}
      onSelectSession={onSelectSession}
      onNewSession={() => { /* 5H: create session */ }}
      onSearch={() => { /* 5x: command palette */ }}
      onSettings={() => { /* 5x: settings */ }}
    />
  );

  let body;
  if (!loaded) {
    body = <div class="conversation-placeholder">Loading sessions…</div>;
  } else if (!session) {
    body = (
      <div class="conversation-empty">
        <p class="conversation-empty-title">No active session</p>
        <p class="conversation-empty-hint">
          Select a session from the sidebar, or start a new one.
        </p>
      </div>
    );
  } else {
    const blocks = projectStream(session);
    const specs = deriveModelSpecs(models);
    const selectedModel = matchSelectedModel(specs, session.model);
    const thinking = session.thinking === "none" ? "off" : (session.thinking || "off");
    const settingsBusy = session.state === "running" || session.state === "permission";
    const permissionMode = session.permissionMode || "yolo";

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
        <div class="session-settings-section">
          <div class="session-settings-label">Permission mode</div>
          <Segmented
            options={PERMISSION_MODE_OPTIONS}
            value={permissionMode}
            disabled={settingsBusy}
            onChange={(mode) => configureSession(session.id, { permissionMode: mode })}
          />
        </div>
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

    body = (
      <>
        <ChatHead
          title={session.title || session.id}
          state={session.state || "idle"}
          path={shortPath(session.cwd) || session.cwd || ""}
          model={shortModel(session.model) || session.model || ""}
          modelAccent={modelAccent(session.model)}
          thinkingLevel={session.thinking || "off"}
          onTitleClick={() => { /* 5x: rename / session menu */ }}
          onGridToggle={() => { /* 5G: pane grid */ }}
          onRewind={() => { /* 5x: rewind picker */ }}
          onNotifications={() => { /* 5x: notifications */ }}
          onSessionSettings={() => setSettingsOpen((v) => !v)}
          onModelClick={() => setModelOpen((v) => !v)}
          modelPopover={modelPopover}
          settingsPopover={settingsPopover}
          modelAnchorRef={modelAnchorRef}
          settingsAnchorRef={settingsAnchorRef}
        />
        <Stream session={session} blocks={blocks} />
        {(session.untrustedMcp || session.pendingPerm || session.pendingAsk) && (
          <div class="conversation-blocking">
            {session.untrustedMcp && <McpBanner key={session.id} sessionId={session.id} />}
            {session.pendingPerm && <PermissionPrompt key={session.id} session={session} />}
            {session.pendingAsk && <AskUserPrompt key={session.id} session={session} />}
          </div>
        )}
        {/* 5J: AgentTray — live subagent chips, not connected yet. */}
        <AgentTray agents={[]} />
        <Composer key={session.id} sessionId={session.id} session={session} />
        <StatusStrip
          ctxPercent={session.contextPercent}
          task={currentTask(session, nowMs)}
          spend={fmtSpend(session.costUSD)}
        />
      </>
    );
  }

  return (
    <div class="conversation-screen">
      {spine}
      <main class="conversation-main">{body}</main>
    </div>
  );
}
