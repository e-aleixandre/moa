import { useState, useEffect } from "preact/hooks";
import { activityPhase, activityText, formatElapsed } from "../../../data/util/activity.js";
import "./MobileNowLine.css";

// MobileNowLine — the ephemeral activity now-line, rendered directly above the
// composer (between MobileStream and MobileComposer). It is the product's own
// live-activity grammar relocated: the SubagentView NowLine (running dot +
// work-shimmer text + elapsed) brought to the parent/foreground run. No new
// gadget, no new vocabulary — data is activityPhase / activityText /
// formatElapsed verbatim.
//
// It is display-only (role="status" aria-live="polite", non-focusable) and a
// flex sibling with flex:none, so its presence PUSHES the stream up rather than
// overlaying or tap-blocking the composer. Absent when idle (returns null → the
// transcript reclaims the space). While working/thinking it shimmers and shows
// elapsed; while waiting/permission it goes amber with no shimmer and no timer
// (the run is parked on you — motion would lie), a quiet echo of the loud inline
// PermissionPrompt that stays primary above it.
export function MobileNowLine({ session }) {
  const phase = activityPhase(session);
  // Tick once a second while there is live activity so the elapsed timer
  // advances on its own (mirrors the ConversationScreen clock). The timer origin
  // is the server-stamped runStartedAtMs, not this clock.
  const active = phase !== null;
  const [nowMs, setNowMs] = useState(() => Date.now());
  useEffect(() => {
    if (!active) return;
    setNowMs(Date.now());
    const t = setInterval(() => setNowMs(Date.now()), 1000);
    return () => clearInterval(t);
  }, [active]);

  if (!phase) return null;

  const text = activityText(session, nowMs);
  if (!text) return null;

  const waiting = phase === "waiting";
  // Elapsed only for the running phases; waiting parks the run, so no
  // elapsed-as-work counter (mirrors the app's timerless "Waiting for you").
  const runStartedAtMs = session.runStartedAtMs || 0;
  const showTimer = !waiting && runStartedAtMs > 0 && (phase === "thinking" || phase === "working");
  const elapsed = showTimer ? formatElapsed(Math.max(0, nowMs - runStartedAtMs)) : "";

  return (
    <div class={`mnowline${waiting ? " is-waiting" : ""}`} role="status" aria-live="polite">
      <span class="mnowline-act">
        <span class={`txt${waiting ? "" : " is-live"}`}>{text}</span>
      </span>
      {elapsed && <span class="mnowline-elapsed">{elapsed}</span>}
    </div>
  );
}
