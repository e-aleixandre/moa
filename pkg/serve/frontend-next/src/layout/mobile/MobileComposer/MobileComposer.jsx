import { useState, useEffect } from "preact/hooks";
import { Composer } from "../../Composer/Composer.jsx";
import { activityPhase, activityLabel, formatElapsed } from "../../../data/util/activity.js";
import "./MobileComposer.css";

// MobileComposer — CONNECTED bottom input for the mobile conversation (5I). It
// wraps the REAL, shared <Composer> (5D/5E: send / queue / slash / @-mention /
// attachments / stop) rather than duplicating any of that logic — the only
// mobile addition is the mono STATUS LINE below it (current work · context ·
// today's spend), the same data the desktop StatusStrip shows. Visual fit is
// handled with mobile CSS only (see MobileComposer.css); the composer's own
// textarea already uses --text-input (≥16px) so iOS never auto-zooms, and this
// wrapper keeps the bottom safe-area inset.

// mobileWork derives the status line's "work" label: the first not-done task,
// else the live activity label + elapsed while running, else nothing.
function mobileWork(session, nowMs) {
  const tasks = session.tasks || [];
  const pending = tasks.find((t) => t.status !== "done");
  if (pending) return pending.title;
  const phase = activityPhase(session);
  if (!phase) return undefined;
  const runStartedAtMs = session.runStartedAtMs || 0;
  const elapsedMs = runStartedAtMs ? Math.max(0, nowMs - runStartedAtMs) : 0;
  const label = activityLabel(phase, elapsedMs);
  const showTimer = runStartedAtMs > 0 && (phase === "thinking" || phase === "working");
  if (showTimer) {
    const t = formatElapsed(elapsedMs);
    return t ? `${label} · ${t}` : label;
  }
  return label;
}

function fmtSpend(costUSD) {
  if (!costUSD || costUSD <= 0) return undefined;
  return `$${costUSD.toFixed(2)}`;
}

export function MobileComposer({ session }) {
  // Activity clock — tick once a second while the session shows live activity
  // so the gerund rotation and elapsed timer advance on their own (mirrors the
  // desktop ConversationScreen clock).
  const active = activityPhase(session) !== null;
  const [nowMs, setNowMs] = useState(() => Date.now());
  useEffect(() => {
    if (!active) return;
    setNowMs(Date.now());
    const t = setInterval(() => setNowMs(Date.now()), 1000);
    return () => clearInterval(t);
  }, [active]);

  const work = mobileWork(session, nowMs);
  const ctx = session.contextPercent;
  const hasCtx = typeof ctx === "number" && ctx >= 0;
  const spend = fmtSpend(session.costUSD);

  return (
    <div class="mcomposer">
      <Composer sessionId={session.id} session={session} />
      <div class="mcomposer-status">
        {work && <span class="work">● {work}</span>}
        {hasCtx && <span class="ctx">ctx {ctx}%</span>}
        {spend && <span class="spend">{spend} today</span>}
      </div>
    </div>
  );
}
