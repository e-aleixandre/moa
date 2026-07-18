import { useState, useEffect } from "preact/hooks";
import { Spine } from "../Spine/Spine.jsx";
import { ChatHead } from "../ChatHead/ChatHead.jsx";
import { Stream } from "../Stream/Stream.jsx";
import { AgentTray } from "../AgentTray/AgentTray.jsx";
import { Composer } from "../Composer/Composer.jsx";
import { StatusStrip } from "../StatusStrip/StatusStrip.jsx";
import { store } from "../../data/store.js";
import { projectStream } from "../../data/stream-model.js";
import { focusedSession, focusedSessionId, modelAccent } from "../../data/selectors.js";
import { openSession } from "../../data/tile-actions.js";
import { shortModel, shortPath } from "../../data/util/format.js";
import { formatElapsed } from "../../data/util/activity.js";
import { activityPhase, activityLabel } from "../../data/util/activity.js";
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
          onSessionSettings={() => { /* 5x: session settings */ }}
          onModelClick={() => { /* 5x: model selector */ }}
        />
        <Stream session={session} blocks={blocks} />
        {/* 5J: AgentTray — live subagent chips, not connected yet. */}
        <AgentTray agents={[]} />
        <Composer sessionId={session.id} session={session} />
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
