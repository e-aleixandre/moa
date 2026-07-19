import "./StatusStrip.css";
import { ClipboardList, Map, Shield, Zap, Flame, Target } from "lucide-preact";
import { fmtTokens } from "../../data/util/format.js";
import { usageForSession, usageLevel, fmtReset, money } from "../../data/util/usage-pills.js";

// StatusStrip — mono strip under the composer: the app's bottom telemetry line,
// mirroring the TUI statusline (5O). It shows the context ring, per-run tokens,
// the current task and today's spend PLUS the ported TaskBar pills: permission
// mode, extra-usage / session-overage, the plan 5h + weekly windows, plan mode,
// goal and task progress. All plan-usage numbers live HERE, never in the header
// (coherence decision P2/INC-07).
//
// The connected container (ConversationScreen) passes the full `session` and
// the global `usage` snapshot (from /api/usage, shared across sessions); the
// dual Anthropic/OpenAI source logic lives in the pure usageForSession
// selector. Every segment is optional — a missing value hides its segment
// rather than showing an invented one.
export function StatusStrip({
  ctxPercent,
  tokensUp,
  tokensDown,
  task,
  spend,
  session,
  usage,
}) {
  const s = session || {};
  const hasCtx = typeof ctxPercent === "number" && ctxPercent >= 0;
  const hasTokens = tokensUp != null && tokensDown != null;
  const ringStyle = hasCtx
    ? { background: `conic-gradient(var(--teal) 0 ${ctxPercent}%, var(--surface0) ${ctxPercent}% 100%)` }
    : undefined;

  const permMode = s.permissionMode || "yolo";
  const planMode = s.planMode;
  const hasPlan = planMode && planMode !== "off";
  const goalActive = !!s.goalActive;
  const tasks = s.tasks || [];
  const hasTasks = tasks.length > 0;
  const done = tasks.filter((t) => t.status === "done").length;
  const total = tasks.length;

  const u = usageForSession(s, usage);
  const extra = u.extra;
  const extraOn = extra && (extra.used ?? 0) > 0;
  const extraTitle = extra
    ? `Extra usage ON — ${money(extra.used, { decimal_places: extra.decimalPlaces, currency: extra.currency })} used` +
      (extra.limit != null
        ? ` of ${money(extra.limit, { decimal_places: extra.decimalPlaces, currency: extra.currency })}`
        : "")
    : "";

  const meterTitle = (label, m) =>
    m.source === "anthropic" && m.resetsAt
      ? `${label}: ${m.pct}% · resets in ${fmtReset(m.resetsAt)}`
      : `${label}: ${m.pct}%`;

  return (
    <div class="status-strip">
      {hasCtx && (
        <span class="status-strip-ctx">
          <span class="status-strip-ring" style={ringStyle} aria-hidden="true" />
          ctx {ctxPercent}%
        </span>
      )}
      {hasTokens && <span class="status-strip-tokens">↑ {fmtTokens(tokensUp)} · ↓ {fmtTokens(tokensDown)} tok</span>}

      <span class={`status-strip-pill perm-${permMode}`} title={`Permission mode: ${permMode}`}>
        <Shield />
        {permMode.toUpperCase()}
      </span>

      {u.onOverage && (
        <span
          class="status-strip-pill session-overage"
          title="This session is being served from extra usage (pay-as-you-go)"
        >
          <Flame />
          on extra
        </span>
      )}

      {extra && (
        <span class={`status-strip-pill usage-extra ${extraOn ? "on" : ""}`} title={extraTitle}>
          <Zap />
          {money(extra.used, { decimal_places: extra.decimalPlaces, currency: extra.currency })}
          {extra.limit != null &&
            `/${money(extra.limit, { decimal_places: extra.decimalPlaces, currency: extra.currency })}`}
        </span>
      )}

      {u.fiveHour && (
        <span class={`status-strip-pill usage-${usageLevel(u.fiveHour.pct)}`} title={meterTitle("Session (5h)", u.fiveHour)}>
          5h {u.fiveHour.pct}%
        </span>
      )}

      {u.week && (
        <span class={`status-strip-pill usage-${usageLevel(u.week.pct)}`} title={meterTitle("Week", u.week)}>
          wk {u.week.pct}%
        </span>
      )}

      {hasPlan && (
        <span class={`status-strip-pill plan-${planMode}`}>
          <Map />
          {planMode}
        </span>
      )}

      {goalActive && (
        <span class="status-strip-pill goal" title={s.goalObjective || "Goal active"}>
          <Target />
          {s.goalVerifying ? "goal · verifying…" : `goal${s.goalIteration ? ` ${s.goalIteration}` : ""}`}
        </span>
      )}

      {hasTasks && (
        <span class="status-strip-pill tasks">
          <ClipboardList />
          {done}/{total}
          {done === total && total > 0 && " ✓"}
        </span>
      )}

      {task && <span class="status-strip-task">{task}</span>}
      {spend && <span class="status-strip-spend">today <b>{spend}</b></span>}
    </div>
  );
}
