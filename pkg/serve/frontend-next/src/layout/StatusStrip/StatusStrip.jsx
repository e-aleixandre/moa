import "./StatusStrip.css";
import { ClipboardList, Map, Flame, Target, Gauge } from "lucide-preact";
import { fmtTokens } from "../../data/util/format.js";
import { fmtReset } from "../../data/util/usage-pills.js";
import { statusStripModel } from "../../data/util/status-strip-model.js";
import { PermissionControl } from "../../components/PermissionControl/PermissionControl.jsx";

// StatusStrip — mono strip under the composer: the app's bottom telemetry line,
// mirroring the TUI statusline. This is the TWO-LEVEL redesign (TELEMETRY-
// SETTINGS-REDESIGN spec §2/§5): the line is glance + the door to the Usage
// panel, NOT the whole accounting dump ported in 5O.
//
// Level 1 (this line): the context ring, per-run tokens (desktop full only),
// the permission chip, the current task, and the session cost — plus the modes
// that are currently ACTIVE (plan/goal/tasks) and ALERTS/PROMOTIONS (🔥 on-extra
// when active; 5h/wk chips only once they climb to the promotion threshold).
// Level 2 (the full accounting: cost breakdown, tokens, detailed context, plan
// windows, extra) lives in the UsagePanel, opened by tapping the cost segment.
//
// The DECISIONS about what is level 1 vs level 2 live in the pure
// statusStripModel (data/util/status-strip-model.js); this component only
// RENDERS the model it returns. The connected container (ConversationScreen)
// passes the full `session` and the global `usage` snapshot (from /api/usage);
// `onOpenUsage` (optional) turns the cost segment into the Usage panel trigger;
// `showTokens` (default true) is set false by compact densities (pane/mobile),
// where tokens drop to level 2.
export function StatusStrip({
  ctxPercent,
  tokensUp,
  tokensDown,
  task,
  spend,
  session,
  usage,
  onOpenUsage,
  onPermChange,
  permBusy = false,
  showTokens = true,
}) {
  const hasCtx = typeof ctxPercent === "number" && ctxPercent >= 0;
  const hasTokens = tokensUp != null && tokensDown != null;
  const ringStyle = hasCtx
    ? { background: `conic-gradient(var(--teal) 0 ${ctxPercent}%, var(--surface0) ${ctxPercent}% 100%)` }
    : undefined;

  const model = statusStripModel(session, usage);
  const { perm, modes, alerts } = model;
  const promoted = alerts.promoted;

  const hasSpend = !!spend;
  // The cost segment is the natural door to the Usage panel: it is the only
  // "money" datum on the line, so tapping it to see "more money" is self-
  // explanatory. When there is no cost yet but the panel is still reachable, a
  // discreet gauge affordance stands in so the panel can ALWAYS be opened.
  const costTrigger = !!onOpenUsage;

  return (
    <div class="status-strip">
      {hasCtx && (
        <span class="status-strip-ctx">
          <span class="status-strip-ring" style={ringStyle} aria-hidden="true" />
          ctx {ctxPercent}%
        </span>
      )}
      {showTokens && hasTokens && (
        <span class="status-strip-tokens">↑ {fmtTokens(tokensUp)} · ↓ {fmtTokens(tokensDown)} tok</span>
      )}

      {/* Permission chip — a control (subphase b): tap opens a 3-option menu
          (never cycles). onPermChange is optional so gallery/other consumers can
          render it read-only; without it, it's a plain badge. */}
      {onPermChange ? (
        <PermissionControl mode={perm.mode} disabled={permBusy} onChange={onPermChange} />
      ) : (
        <span class={`status-strip-pill perm-${perm.mode}`} title={`Permission mode: ${perm.mode}`}>
          {perm.mode.toUpperCase()}
        </span>
      )}

      {/* Active modes — only rendered when the model reports them (off modes
          are omitted upstream). */}
      {modes.planMode && (
        <span class={`status-strip-pill plan-${modes.planMode}`}>
          <Map />
          {modes.planMode}
        </span>
      )}

      {modes.goal && (
        <span class="status-strip-pill goal" title={modes.goal.objective || "Goal active"}>
          <Target />
          {modes.goal.verifying ? "goal · verifying…" : `goal${modes.goal.iteration ? ` ${modes.goal.iteration}` : ""}`}
        </span>
      )}

      {modes.tasks && (
        <span class="status-strip-pill tasks">
          <ClipboardList />
          {modes.tasks.done}/{modes.tasks.total}
          {modes.tasks.complete && " ✓"}
        </span>
      )}

      {/* Alerts / promotions. 🔥 on-extra only while active; each promoted plan
          window (5h/wk ≥ threshold) as a colored chip. */}
      {alerts.onExtra && (
        <span
          class="status-strip-pill session-overage"
          title="This session is being served from extra usage (pay-as-you-go)"
        >
          <Flame />
          on extra
        </span>
      )}

      {promoted.map((m) => (
        <span
          key={m.kind}
          class={`status-strip-pill usage-${m.level}`}
          title={`${m.label}: ${m.pct}%${m.resetsAt ? ` · resets in ${fmtReset(m.resetsAt)}` : ""}`}
        >
          {m.kind} {m.pct}%
        </span>
      ))}

      {task && <span class="status-strip-task">{task}</span>}

      {/* Cost segment — the Usage panel trigger when onOpenUsage is supplied.
          Falls back to plain text otherwise (galleries / other consumers). */}
      {hasSpend ? (
        costTrigger ? (
          <button
            type="button"
            class="status-strip-spend status-strip-spend-btn"
            onClick={onOpenUsage}
            aria-label="Show usage"
          >
            today <b>{spend}</b>
          </button>
        ) : (
          <span class="status-strip-spend">today <b>{spend}</b></span>
        )
      ) : (
        costTrigger && (
          <button
            type="button"
            class="status-strip-gauge"
            onClick={onOpenUsage}
            aria-label="Show usage"
            title="Show usage"
          >
            <Gauge />
          </button>
        )
      )}
    </div>
  );
}
