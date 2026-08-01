import "./StatusStrip.css";
import { ClipboardList, Map, Flame, Target, Gauge, Plug, AlertTriangle } from "lucide-preact";
import { statusStripModel } from "../../data/util/status-strip-model.js";
import { PermissionControl } from "../../components/PermissionControl/PermissionControl.jsx";
import { TokenFlow } from "../../components/TokenFlow/TokenFlow.jsx";
import { activityPhase } from "../../data/util/activity.js";

// StatusStrip — mono strip under the composer: the app's bottom telemetry line,
// mirroring the TUI statusline. This is the TWO-LEVEL redesign (TELEMETRY-
// SETTINGS-REDESIGN spec §2/§5): the line is glance + the door to the Usage
// panel, NOT the whole accounting dump.
//
// Level 1 (this line): the context ring + session cost (leading the line, same
// side as the mobile line), per-run tokens, the permission chip, and the modes
// that are currently ACTIVE (plan/goal/tasks) plus the on-extra alert. The
// foreground run's ACTIVITY is not here: it lives in the NowLine above the
// composer, in both densities. `task` survives for the consumers whose subject
// is NOT the focused run — the subagent strip (a child's activity, which can be
// live while the parent sits idle, hence `taskLive`), the panes and the catalog.
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
//
// Every datum arrives as a prop, and each control is a control only when its
// handler is passed — which is what lets the subagent view wear this same strip
// read-only, measuring the CHILD (its context, its tokens, its spend) instead
// of the session.
export function StatusStrip({
  ctxPercent,
  tokensUp,
  tokensDown,
  task,
  spend,
  session,
  usage,
  taskLive,
  onOpenUsage,
  onOpenMcp,
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

  const hasSpend = !!spend;
  const phase = activityPhase(session);
  // taskLive overrides the session-derived liveness for a strip whose task
  // belongs to something other than the main run — a subagent's, whose activity
  // can be live while the parent sits idle (an async child), or the reverse.
  const workIsLive = taskLive == null ? phase === "working" || phase === "thinking" : taskLive;
  // The cost segment is the natural door to the Usage panel: it is the only
  // "money" datum on the line, so tapping it to see "more money" is self-
  // explanatory. When there is no cost yet but the panel is still reachable, a
  // discreet gauge affordance stands in so the panel can ALWAYS be opened.
  const costTrigger = !!onOpenUsage;

  return (
    <div class="status-strip">
      {/* LEFT: context + cost lead the line, matching the mobile line so the same
          datum sits on the same side in both densities. They share a segment
          because the cost is the door to the same Usage panel the ring explains. */}
      {(hasCtx || hasSpend) && (
        <span class="status-strip-usage">
          {hasCtx && (
            <span class="status-strip-ctx">
              <span class="status-strip-ring" style={ringStyle} aria-hidden="true" />
              ctx {ctxPercent}%
            </span>
          )}

          {/* Cost segment — the Usage panel trigger when onOpenUsage is supplied.
              Falls back to plain text otherwise (galleries / other consumers). */}
          {hasSpend ? (
            costTrigger ? (
              <button
                type="button"
                class={`status-strip-spend status-strip-spend-btn spend-${model.spendLevel || "normal"}`}
                onClick={onOpenUsage}
                aria-label="Show usage"
                title="Estimated session cost"
              >
                {/* The tilde marks an estimated accumulated session cost. */}
                <b>~{spend}</b>
              </button>
            ) : (
              <span class={`status-strip-spend spend-${model.spendLevel || "normal"}`}>
                <b>~{spend}</b>
              </span>
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
        </span>
      )}

      {/* Then the activity, the permission chip and the currently-active modes. */}
      {task && <span class={`status-strip-task work${workIsLive ? " is-live" : ""}`}>{task}</span>}

      {/* Permission chip — a control (subphase b): tap opens a 3-option menu
          (never cycles). onPermChange is optional so gallery/other consumers can
          render it read-only; without it, it's the same chip as a plain span, so
          the read-only line looks identical to the interactive one. */}
      {onPermChange ? (
        <PermissionControl mode={perm.mode} disabled={permBusy} onChange={onPermChange} />
      ) : (
        <span class={`perm-chip perm-${perm.mode}`} title={`Permission mode: ${perm.mode}`}>
          {perm.mode}
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

      {/* Alerts. 🔥 on-extra only while active. */}
      {alerts.onExtra && (
        <span
          class="status-strip-pill session-overage"
          title="This session is being served from extra usage (pay-as-you-go)"
        >
          <Flame />
          on extra
        </span>
      )}

      {/* RIGHT: what is left of the run's telemetry, anchored to the right edge —
          MCP health and the per-run tokens. margin-left lives on the group (not
          one segment) so it stays right-aligned no matter which are present. */}
      <span class="status-strip-right">
        {session?.mcp && session.mcp.total > 0 && (() => {
          const unhealthy = session.mcp.unhealthy > 0;
          const disabledNote = session.mcp.disabled > 0 ? `, ${session.mcp.disabled} disabled` : "";
          const label = unhealthy
            ? `MCP: ${session.mcp.unhealthy} of ${session.mcp.total} need attention${disabledNote}`
            : `MCP: ${session.mcp.total} server${session.mcp.total === 1 ? "" : "s"}${disabledNote} ready`;
          const body = (
            <>
              {unhealthy ? <AlertTriangle /> : <Plug />}
              {unhealthy ? `${session.mcp.unhealthy}!` : `mcp ${session.mcp.total}`}
            </>
          );
          return onOpenMcp ? (
            <button
              type="button"
              class={`status-strip-mcp status-strip-mcp-btn${unhealthy ? " status-strip-mcp-bad" : ""}`}
              onClick={onOpenMcp}
              aria-label={`${label} — open MCP servers`}
              title={label}
            >
              {body}
            </button>
          ) : (
            <span
              class={`status-strip-mcp${unhealthy ? " status-strip-mcp-bad" : ""}`}
              title={label}
            >
              {body}
            </span>
          );
        })()}
        {showTokens && hasTokens && (
          <span class="status-strip-tokens"><TokenFlow up={tokensUp} down={tokensDown} variant="strip" /></span>
        )}
      </span>
    </div>
  );
}
