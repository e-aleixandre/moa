import { useState, useEffect } from "preact/hooks";
import { Composer } from "../../Composer/Composer.jsx";
import { Sheet, UsagePanel } from "../../../components/index.js";
import { activityPhase, activityLabel, formatElapsed } from "../../../data/util/activity.js";
import { fmtCost, fmtReset } from "../../../data/util/usage-pills.js";
import { statusStripModel } from "../../../data/util/status-strip-model.js";
import "./MobileComposer.css";

// MobileComposer — CONNECTED bottom input for the mobile conversation (5I). It
// wraps the REAL, shared <Composer> (5D/5E: send / queue / slash / @-mention /
// attachments / stop) rather than duplicating any of that logic — the only
// mobile addition is the mono STATUS LINE below it. The line mirrors the mockup
// (.m-status): what the agent is DOING on the left (blue dot + activity) and
// the session spend on the right. Context % is NOT repeated here — it already
// lives in the header.
//
// TELEMETRY-SETTINGS-REDESIGN §2: on mobile the line is glance-only. Per-run
// tokens and the fixed 5h/wk meters that 5I put here are REMOVED — they are
// level 2 (they drop to the Usage panel). The plan windows only come back as
// PROMOTED chips (≥ threshold, from statusStripModel.alerts.promoted). The spend
// is the Usage panel trigger: tapping it opens the panel inside a Sheet.
//
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
  return fmtCost(costUSD);
}

export function MobileComposer({ session, usage }) {
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

  const [usageOpen, setUsageOpen] = useState(false);
  useEffect(() => { setUsageOpen(false); }, [session.id]);

  const work = mobileActivity(session, nowMs);
  const spend = fmtSpend(session.costUSD);

  // Level 1 promotions only: the plan windows surface here just when they climb
  // to the threshold (statusStripModel decides). Fixed meters/tokens are gone.
  const model = statusStripModel(session, usage);
  const promoted = model.alerts.promoted;

  return (
    <div class="mcomposer">
      <Composer sessionId={session.id} session={session} shortPlaceholder />
      <div class="mcomposer-status">
        {work && <span class="work">● {work}</span>}
        <span class={`mstatus-perm perm-${model.perm.mode}`} title={`Permission mode: ${model.perm.mode}`}>
          {model.perm.mode.toUpperCase()}
        </span>
        {model.alerts.onExtra && (
          <span class="meter usage-high" title="Served from extra usage (pay-as-you-go)">on extra</span>
        )}
        {promoted.map((m) => (
          <span
            key={m.kind}
            class={`meter usage-${m.level}`}
            title={`${m.label}: ${m.pct}%${m.resetsAt ? ` · resets in ${fmtReset(m.resetsAt)}` : ""}`}
          >
            {m.kind} {m.pct}%
          </span>
        ))}
        {spend ? (
          <button
            type="button"
            class="spend spend-btn"
            onClick={() => setUsageOpen(true)}
            aria-label="Show usage"
          >
            {spend} today
          </button>
        ) : (
          <button
            type="button"
            class="spend spend-btn spend-empty"
            onClick={() => setUsageOpen(true)}
            aria-label="Show usage"
          >
            usage
          </button>
        )}
      </div>
      <Sheet open={usageOpen} onClose={() => setUsageOpen(false)} title="Usage">
        <UsagePanel
          session={session}
          usage={usage}
          ctxPercent={session.contextPercent}
          costUSD={session.costUSD}
        />
      </Sheet>
    </div>
  );
}
