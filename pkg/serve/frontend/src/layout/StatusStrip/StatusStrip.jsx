import "./StatusStrip.css";
import { ClipboardList, Flame, Target, Gauge, Plug, AlertTriangle } from "lucide-preact";
import { statusItemPriority, statusStripModel } from "../../data/util/status-strip-model.js";
import { PermissionControl } from "../../components/PermissionControl/PermissionControl.jsx";
import { ModelPill, TokenFlow } from "../../components/index.js";
import { activityPhase } from "../../data/util/activity.js";
import { tokenFlowVariant } from "./status-strip-view-model.js";

// StatusStrip — mono strip under the composer: the app's bottom telemetry line,
// mirroring the TUI statusline. This is the TWO-LEVEL redesign (TELEMETRY-
// SETTINGS-REDESIGN spec §2/§5): the line is glance + the door to the Usage
// panel, NOT the whole accounting dump.
//
// Level 1 (this line): the context ring + session cost (leading the line, same
// side as the mobile line), the model (same pill as mobile, here instead of
// in the header), per-run tokens, the permission chip, fast mode when it's on,
// and the modes that are currently ACTIVE (goal/tasks) plus the on-extra alert.
// The foreground run's ACTIVITY is not here: it lives in the LiveBar above the
// composer. Compact density (phone, grid pane) is the same line with less
// padding and without goal/tasks pills.
export function StatusStrip({
  ctxPercent,
  tokensUp,
  tokensDown,
  task,
  spend,
  session,
  usage,
  taskLive,
  compact = false,
  onOpenUsage,
  onOpenMcp,
  onPermChange,
  onPerm,
  permOpen,
  permBusy = false,
  showTokens = true,
  modelName,
  modelAccent = "lavender",
  thinking = "off",
  thinkingPosition = thinking,
  onModel,
  modelOpen,
  modelPopover,
  modelAnchorRef,
  children,
}) {
  const hasCtx = typeof ctxPercent === "number" && ctxPercent >= 0;
  const hasTokens = tokensUp != null && tokensDown != null;
  const ringStyle = hasCtx
    ? { background: `conic-gradient(var(--teal) 0 ${ctxPercent}%, var(--surface0) ${ctxPercent}% 100%)` }
    : undefined;

  const strip = statusStripModel(session, usage);
  const { perm, modes, alerts } = strip;

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
    <div class={`status-strip${compact ? " is-compact" : ""}`}>
      {/* The line reads in three tiers, in the catalogue's order: SETTINGS first
         (what you glance at while typing and what changes the answer), then the
         EVENTS that only exist while something is wrong, then the GAUGES at the
         right edge.

         It used to lead with the context ring, which put the least actionable
         number where the eye lands first and left the model — the thing you
         change — floating in the middle. Reading order is the hierarchy: the
         model and the permission mode are the two facts that decide what the
         next turn does, so they come first, on both densities. */}

      {/* Tier 1 — settings. */}
      {task && <span class={`status-strip-task work${workIsLive ? " is-live" : ""}`}>{task}</span>}

      {modelName && (
        <span class={`status-strip-model amb-${statusItemPriority("model")}`} ref={modelAnchorRef}>
          <ModelPill
            model={modelName}
            accent={modelAccent}
            variant="bars"
            level={thinking}
            thinkingPosition={thinkingPosition}
            readOnly={!onModel}
            onClick={onModel}
            aria-haspopup={onModel ? "dialog" : undefined}
            aria-expanded={onModel ? modelOpen : undefined}
            aria-label="Model & thinking"
          />
          {modelPopover}
        </span>
      )}

      {compact && modelName && (
        <span class="status-strip-div" aria-hidden="true" />
      )}

      {/* Permission chip — a control (subphase b): tap opens a 3-option menu
          (never cycles). onPermChange is optional so gallery/other consumers can
          render it read-only; without it, it's the same chip as a plain span, so
          the read-only line looks identical to the interactive one. */}
      {onPerm ? (
        <button
          type="button"
          class={`perm-chip perm-${perm.mode} amb-${statusItemPriority("perm")}`}
          onClick={onPerm}
          aria-haspopup="dialog"
          aria-expanded={permOpen}
          aria-label={`Permission mode: ${perm.mode}`}
        >
          {perm.mode}
        </button>
      ) : onPermChange ? (
        <PermissionControl mode={perm.mode} disabled={permBusy} onChange={onPermChange} />
      ) : (
        <span class={`perm-chip perm-${perm.mode} amb-${statusItemPriority("perm")}`} title={`Permission mode: ${perm.mode}`}>
          {perm.mode}
        </span>
      )}

      {/* Fast mode is session state, like the permission chip, so it reads as
          one: a word, not a glyph, and only while it's on. Same place and
          wording as the mobile line. */}
      {session?.fast && (
        <span class={`status-strip-fast amb-${statusItemPriority("fast")}`} aria-label="Fast mode on — this session is billed at a premium rate">
          fast
        </span>
      )}

      {/* Tier 3 — events, only while they exist. Active modes are only rendered
          when the model reports them (off modes are omitted upstream). */}
      {!compact && modes.goal && (
        <span class={`status-strip-pill goal amb-${statusItemPriority("goal")}`} title={modes.goal.objective || "Goal active"}>
          <Target />
          {modes.goal.verifying ? "goal · verifying…" : `goal${modes.goal.iteration ? ` ${modes.goal.iteration}` : ""}`}
        </span>
      )}

      {!compact && modes.tasks && (
        <span class={`status-strip-pill tasks amb-${statusItemPriority("tasks")}`}>
          <ClipboardList />
          {modes.tasks.done}/{modes.tasks.total}
          {modes.tasks.complete && " ✓"}
        </span>
      )}

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
            class={`status-strip-mcp status-strip-mcp-btn amb-${statusItemPriority("mcp", unhealthy ? "unhealthy" : "healthy")}${unhealthy ? " status-strip-mcp-bad" : ""}`}
            onClick={onOpenMcp}
            aria-label={`${label} — open MCP servers`}
            title={label}
          >
            {body}
          </button>
        ) : (
          <span
            class={`status-strip-mcp amb-${statusItemPriority("mcp", unhealthy ? "unhealthy" : "healthy")}${unhealthy ? " status-strip-mcp-bad" : ""}`}
            title={label}
          >
            {body}
          </span>
        );
      })()}

      {/* Alerts. 🔥 on-extra only while active. */}
      {alerts.onExtra && (
        <span
          class={`status-strip-pill session-overage amb-${statusItemPriority("extra")}`}
          title="This session is being served from extra usage (pay-as-you-go)"
        >
          <Flame />
          on extra
        </span>
      )}

      {/* Tier 2 — gauges, anchored to the right edge: one button (context +
          spend: the door to Usage) and one reading (the per-run tokens).
          margin-left lives on the group, not on one segment, so it stays
          right-aligned no matter which of them are present. */}
      <span class="status-strip-right">
        {(hasCtx || hasSpend || costTrigger) && (() => {
          const body = (
            <>
              {hasCtx && (
                <span class={`status-strip-ctx amb-${statusItemPriority("context")}`}>
                  <span class="status-strip-ring" style={ringStyle} aria-hidden="true" />
                  {compact ? `${ctxPercent}%` : `ctx ${ctxPercent}%`}
                </span>
              )}
              {hasSpend ? (
                <span class={`status-strip-spend spend-${strip.spendLevel || "normal"}`}>
                  <b>~{spend}</b>
                </span>
              ) : (
                costTrigger && (
                  <span class="status-strip-gauge" aria-hidden="true">
                    <Gauge />
                  </span>
                )
              )}
            </>
          );
          return costTrigger ? (
            <button
              type="button"
              class="status-strip-usage status-strip-usage-btn"
              onClick={onOpenUsage}
              aria-label="Show usage"
              title="Context and session cost"
            >
              {body}
            </button>
          ) : (
            <span class="status-strip-usage">{body}</span>
          );
        })()}
        {showTokens && hasTokens && (
          <span class={`status-strip-tokens amb-${statusItemPriority("tokens")}`}><TokenFlow up={tokensUp} down={tokensDown} variant={tokenFlowVariant(compact)} /></span>
        )}
      </span>
      {children}
    </div>
  );
}
