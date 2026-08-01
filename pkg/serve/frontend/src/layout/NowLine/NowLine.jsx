import { useState, useEffect } from "preact/hooks";
import { activityPhase, activityText, formatElapsed } from "../../data/util/activity.js";
import "./NowLine.css";

// NowLine — the ephemeral activity now-line rendered directly ABOVE the
// composer, in both densities: it is the product's live-activity grammar
// (running dot + work-shimmer text + elapsed) applied to the foreground run.
// Data is activityPhase / activityText / formatElapsed verbatim, so the line can
// never drift from the TUI's statusline or from the mobile screen.
//
// It is display-only (role="status" aria-live="polite", non-focusable) and a
// flex sibling with flex:none, so its presence PUSHES the stream up rather than
// overlaying or click-blocking the composer. Absent when idle (returns null →
// the transcript reclaims the space). While working/thinking it shimmers and
// shows elapsed; while waiting/permission it goes amber with no shimmer and no
// timer (the run is parked on you — motion would lie), a quiet echo of the loud
// inline PermissionPrompt that stays primary above it.
//
// `base` is the CSS class prefix: the two densities share this JSX but NOT their
// stylesheets (a mobile line is not a desktop line — width, padding and
// alignment differ), so each passes its own prefix and owns its rules. `nowMs`
// lets a caller that already runs a one-second clock (ConversationScreen) drive
// the timer instead of us starting a second interval for the same tick.

// nowLineModel decides WHAT the line says, given a session and a "now": the
// phrase, whether the run is parked on the user, and the elapsed counter (empty
// unless the agent is actually running). Pure, so the decision is testable
// without a DOM — the component below only renders it.
export function nowLineModel(session, nowMs) {
  const phase = activityPhase(session);
  if (!phase) return null;

  const text = activityText(session, nowMs);
  if (!text) return null;

  const waiting = phase === "waiting";
  // Elapsed only for the running phases; waiting parks the run, so no
  // elapsed-as-work counter (mirrors the app's timerless "Waiting for you").
  const runStartedAtMs = session.runStartedAtMs || 0;
  const showTimer = !waiting && runStartedAtMs > 0 && (phase === "thinking" || phase === "working");
  const elapsed = showTimer ? formatElapsed(Math.max(0, nowMs - runStartedAtMs)) : "";

  return { text, waiting, elapsed };
}

export function NowLine({ session, base = "nowline", nowMs: nowMsProp }) {
  const active = activityPhase(session) !== null;
  // Own clock, used only when the caller does not supply one. Hooks run
  // unconditionally (rules of hooks); the interval is what we skip.
  const driven = nowMsProp != null;
  const [ownNowMs, setOwnNowMs] = useState(() => Date.now());
  useEffect(() => {
    if (driven || !active) return;
    setOwnNowMs(Date.now());
    const t = setInterval(() => setOwnNowMs(Date.now()), 1000);
    return () => clearInterval(t);
  }, [driven, active]);
  // The timer ORIGIN is always the server-stamped runStartedAtMs; the clock only
  // supplies "now".
  const nowMs = driven ? nowMsProp : ownNowMs;

  const model = nowLineModel(session, nowMs);
  if (!model) return null;
  const { text, waiting, elapsed } = model;

  return (
    <div class={`${base}${waiting ? " is-waiting" : ""}`} role="status" aria-live="polite">
      <span class={`${base}-act`}>
        <span class={`txt${waiting ? "" : " is-live"}`}>{text}</span>
      </span>
      {elapsed && <span class={`${base}-elapsed`}>{elapsed}</span>}
    </div>
  );
}
