import { useState, useEffect } from "preact/hooks";
import { Composer } from "../../Composer/Composer.jsx";
import { activityPhase, activityLabel, formatElapsed } from "../../../data/util/activity.js";
import { fmtTokens } from "../../../data/util/format.js";
import "./MobileComposer.css";

// MobileComposer — CONNECTED bottom input for the mobile conversation (5I). It
// wraps the REAL, shared <Composer> (5D/5E: send / queue / slash / @-mention /
// attachments / stop) rather than duplicating any of that logic — the only
// mobile addition is the mono STATUS LINE below it. The line mirrors the mockup
// (.m-status): what the agent is DOING on the left (blue dot + activity), the
// live per-run token ↑/↓ counts in the middle, and the session spend on the
// right. Context % is NOT repeated here — it already lives in the header.
// Visual fit is CSS-only (MobileComposer.css); the composer's own textarea uses
// --text-input (≥16px) so iOS never auto-zooms, and this wrapper keeps the
// bottom safe-area inset.

// mobileActivity derives the status line's "doing" label: the live activity
// (gerund + elapsed while running), else the first not-done task title as a
// fallback when there's no live phase, else nothing (hidden, not invented).
function mobileActivity(session, nowMs) {
  const phase = activityPhase(session);
  if (phase) {
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
  const pending = (session.tasks || []).find((t) => t.status !== "done");
  return pending ? pending.title : undefined;
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

  const work = mobileActivity(session, nowMs);
  const spend = fmtSpend(session.costUSD);
  const up = session.runTokensUp || 0;
  const down = session.runTokensDown || 0;
  const hasTokens = up > 0 || down > 0;

  return (
    <div class="mcomposer">
      <Composer sessionId={session.id} session={session} shortPlaceholder />
      <div class="mcomposer-status">
        {work && <span class="work">● {work}</span>}
        {hasTokens && (
          <span class="tokens">↑{fmtTokens(up)} ↓{fmtTokens(down)}</span>
        )}
        {spend && <span class="spend">{spend} today</span>}
      </div>
    </div>
  );
}
