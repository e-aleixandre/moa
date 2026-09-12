import "./StatusStrip.css";
import { statusItemPriority, statusStripModel } from "../../data/util/status-strip-model.js";
import { activityPhase } from "../../data/util/activity.js";
import { TokenFlow } from "../../components/index.js";

// StatusStrip — the line under the composer. Markup and CSS are the
// catalogue's (catalog/zones-lab.jsx:1472 `StatusLine`, zones-lab.css:611 the
// `.zl-status` block), MOVED here rather than imitated: the classes travelled
// with the rules, so the line IS the accepted design instead of a translation
// of it. The catalogue imports this component now, which is what makes one
// definition rather than two.
//
// Three tiers, three places, in the catalogue's order: SETTINGS left (model,
// permissions, fast — what you glance at while typing and what changes the
// answer), EVENTS in the middle (goal, tasks, mcp, on-extra — only while they
// exist), GAUGES right (context+spend, the door to Usage; and the per-run
// tokens). The line is its own container, so it degrades by its OWN width and
// the same component behaves in the desktop column, a pane and the phone dock.
//
// What is NOT the catalogue's is everything the prototype never had, grafted on
// top: real sessions, accessible names, the model popover, the permission menu
// and the MCP panel anchored by their hosts, the busy lock, and the house rule
// that a missing datum hides its segment rather than drawing a zero.
//
// Every door on the line opens OVER the line (model, permissions, mcp); the one
// exception is the gauges, which open the session's panel on its Usage page,
// because that reading has a home in the dossier and the others do not.
//
// The five data the imitation carried (and their order) are gone with it: the
// line now carries the nine the catalogue declares, and sheds them by the
// priority declared in data/util/status-strip-model.js.

// The catalogue's meter: four bars lit from the left in the accent. Production
// has five thinking LEVELS ("off" lights none), and its stable selector
// position is what decides how many are lit — not the provider's effort word.
const THINK_POSITIONS = ["off", "low", "medium", "high", "xhigh"];

function ThinkMeter({ level }) {
  const n = THINK_POSITIONS.indexOf(level);
  return (
    <span class="zl-think" aria-hidden="true">
      {[1, 2, 3, 4].map((k) => <i class={k <= n ? "" : "is-off"} key={k} />)}
    </span>
  );
}

// The context ring: accent while there is room, state colour as it fills. An
// SVG arc, not a conic-gradient: the catalogue's ring animates its dasharray,
// and a 2.4px stroke reads at 14px where a masked gradient goes muddy.
export function CtxRing({ pct }) {
  const r = 6.5;
  const c = 2 * Math.PI * r;
  const tone = pct >= 90 ? "is-hot" : pct >= 70 ? "is-warm" : "";
  return (
    <svg class={`zl-ring ${tone}`} viewBox="0 0 16 16" aria-hidden="true">
      <circle cx="8" cy="8" r={r} class="zl-ring-track" />
      <circle
        cx="8" cy="8" r={r} class="zl-ring-arc"
        stroke-dasharray={`${(c * pct) / 100} ${c}`}
        transform="rotate(-90 8 8)"
      />
    </svg>
  );
}

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
  mcpOpen,
  onPerm,
  permOpen,
  permBusy = false,
  permPopover,
  permAnchorRef,
  showTokens = true,
  modelName,
  thinking = "off",
  thinkingPosition = thinking,
  onModel,
  modelOpen,
  modelPopover,
  modelAnchorRef,
  children,
}) {
  const hasCtx = typeof ctxPercent === "number" && ctxPercent >= 0;
  // `(a || b) && <div>` would paint a bare 0 for a run that has moved nothing.
  // The tally is a heartbeat: absent until it beats.
  const hasTokens = (tokensUp || 0) > 0 || (tokensDown || 0) > 0;

  const strip = statusStripModel(session, usage);
  const { perm, modes, alerts } = strip;

  const hasSpend = !!spend;
  const phase = activityPhase(session);
  // taskLive overrides the session-derived liveness for a strip whose task
  // belongs to something other than the main run — a subagent's, whose activity
  // can be live while the parent sits idle (an async child), or the reverse.
  const workIsLive = taskLive == null ? phase === "working" || phase === "thinking" : taskLive;
  // The gauges are the door to Usage whether or not there is a cost yet: the
  // ring alone opens it, so a fresh session can still reach the panel.
  const usageTrigger = !!onOpenUsage;
  // The meter draws the stable selector position. An explicit null means the
  // catalog has not answered yet, and drawing "off" would state an effort the
  // session does not have.
  const meterPosition = thinkingPosition === null ? null : (thinkingPosition || thinking);

  const mcp = session?.mcp;
  const mcpUnhealthy = !!(mcp && mcp.unhealthy > 0);
  const mcpLabel = mcp
    ? (mcpUnhealthy
        ? `MCP: ${mcp.unhealthy} of ${mcp.total} need attention`
        : `MCP: ${mcp.total} server${mcp.total === 1 ? "" : "s"} ready`)
      + (mcp.disabled > 0 ? `, ${mcp.disabled} disabled` : "")
    : "";

  return (
    <div class={`zl-status${compact ? " is-compact" : ""}`}>
      {/* tier 1 — settings */}
      <div class="zl-st-group is-settings">
        {task && <span class={`zl-st zl-st-task${workIsLive ? " is-live" : ""}`}>{task}</span>}

        {modelName && (
          <span class="zl-st-anchor" ref={modelAnchorRef}>
            <button
              type="button"
              class={`zl-st zl-st-model zl-${statusItemPriority("model")}${modelOpen ? " is-open" : ""}`}
              onClick={onModel}
              disabled={!onModel}
              aria-expanded={onModel ? !!modelOpen : undefined}
              aria-haspopup={onModel ? "dialog" : undefined}
              aria-label={`Model & thinking: ${modelName}, ${thinking}`}
            >
              <span class="zl-st-word zl-st-model-name">{modelName}</span>
              {meterPosition !== null && <ThinkMeter level={meterPosition} />}
            </button>
            {modelPopover}
          </span>
        )}

        {/* Permission mode is the one setting you must never misread, so it is
            the one setting in a state colour. A tap OPENS the choice; it never
            cycles — a stray touch must not be able to drop a session into
            YOLO. Locked while the agent is running, like the rest of the
            session settings. */}
        <span class="zl-st-anchor" ref={permAnchorRef}>
          {onPerm ? (
            <button
              type="button"
              class={`zl-st zl-st-perm zl-${statusItemPriority("perm")} is-${perm.mode}${permOpen ? " is-open" : ""}`}
              onClick={onPerm}
              disabled={permBusy}
              aria-haspopup="dialog"
              aria-expanded={!!permOpen}
              aria-label={permBusy
                ? `Permission mode: ${perm.mode} (locked while the agent is running)`
                : `Permission mode: ${perm.mode}`}
            >
              <span class="zl-st-word">{perm.mode}</span>
            </button>
          ) : (
            <span
              class={`zl-st zl-st-perm zl-${statusItemPriority("perm")} is-${perm.mode}`}
              title={`Permission mode: ${perm.mode}`}
            >
              <span class="zl-st-word">{perm.mode}</span>
            </span>
          )}
          {permPopover}
        </span>

        {session?.fast && (
          <span class={`zl-st zl-st-fast zl-${statusItemPriority("fast")}`} title="Fast mode: billed at a premium rate">
            <svg class="zl-st-ico" viewBox="0 0 16 16" aria-hidden="true"><path d="M9 1.5L3.5 9h4l-.5 5.5L12.5 7h-4z" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round" /></svg>
            <span class="zl-st-word">fast</span>
          </span>
        )}
      </div>

      {/* tier 3 — events, only while they exist */}
      <div class="zl-st-group is-events">
        {!compact && modes.goal && (
          <span
            class={`zl-st zl-st-ev zl-${statusItemPriority("goal")}`}
            title={modes.goal.objective || "Goal active"}
          >
            <svg class="zl-st-ico" viewBox="0 0 16 16" aria-hidden="true"><circle cx="8" cy="8" r="5.5" fill="none" stroke="currentColor" stroke-width="1.5" /><circle cx="8" cy="8" r="1.8" fill="currentColor" /></svg>
            <span class="zl-st-word">goal</span>
            {modes.goal.verifying
              ? <span class="zl-data">✓?</span>
              : !!modes.goal.iteration && <span class="zl-data">{modes.goal.iteration}</span>}
          </span>
        )}

        {!compact && modes.tasks && (
          <span
            class={`zl-st zl-st-ev zl-${statusItemPriority("tasks")}`}
            title={`Tasks: ${modes.tasks.done} of ${modes.tasks.total} done`}
          >
            <svg class="zl-st-ico" viewBox="0 0 16 16" aria-hidden="true"><path d="M3 4.5l1.5 1.5 3-3M3 10.5l1.5 1.5 3-3M9 5h4M9 11h4" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" /></svg>
            <span class="zl-st-word">tasks</span>
            <span class="zl-data">{modes.tasks.done}/{modes.tasks.total}</span>
          </span>
        )}

        {/* MCP takes its priority from its STATE, not its type: healthy it
            drops early, unhealthy it stays and keeps its number. */}
        {mcp && mcp.total > 0 && (() => {
          const cls = `zl-st zl-st-ev zl-st-mcp zl-${statusItemPriority("mcp", mcpUnhealthy ? "unhealthy" : "healthy")}${mcpUnhealthy ? " is-alarm-red" : ""}`;
          const body = (
            <>
              <svg class="zl-st-ico" viewBox="0 0 16 16" aria-hidden="true"><path d="M5 2v3M11 2v3M3.5 5h9v3a4.5 4.5 0 0 1-9 0zM8 12.5V15" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" /></svg>
              <span class="zl-st-word">mcp</span>
              <span class="zl-data">{mcpUnhealthy ? `${mcp.unhealthy}/${mcp.total}` : mcp.total}</span>
            </>
          );
          return onOpenMcp ? (
            <button
              type="button"
              class={`${cls}${mcpOpen ? " is-open" : ""}`}
              onClick={onOpenMcp}
              aria-haspopup="dialog"
              aria-expanded={!!mcpOpen}
              aria-label={`${mcpLabel} — open MCP servers`}
              title={mcpLabel}
            >
              {body}
            </button>
          ) : (
            <span class={cls} title={mcpLabel}>{body}</span>
          );
        })()}

        {alerts.onExtra && (
          <span
            class={`zl-st zl-st-ev zl-${statusItemPriority("extra")} is-alarm-yellow`}
            title="This session is being served from extra usage (pay-as-you-go)"
          >
            <svg class="zl-st-ico" viewBox="0 0 16 16" aria-hidden="true"><path d="M8 1.5c.5 3-3 4-3 8a3 3 0 0 0 6 0c0-1.5-.6-2.5-1.2-3.2-.3 1.2-1 1.7-1.3 1.7C9 6 9.5 3.5 8 1.5z" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round" /></svg>
            <span class="zl-st-word">extra</span>
          </span>
        )}
      </div>

      {/* tier 2 — gauges. One button (ctx + spend: the door to Usage), one
          reading. Every datum hides itself when it is missing, so the group is
          only rendered while it has something to say. */}
      <div class="zl-st-group is-gauges">
        {(hasCtx || hasSpend || usageTrigger) && (() => {
          const body = (
            <>
              {hasCtx && <CtxRing pct={ctxPercent} />}
              {hasCtx && <span class={`zl-data zl-num zl-${statusItemPriority("context")}`}>{ctxPercent}<span class="zl-unit">%</span></span>}
              {hasCtx && hasSpend && <span class={`zl-st-sep zl-${statusItemPriority("spend")}`} aria-hidden="true" />}
              {hasSpend && (
                <span class={`zl-data zl-num zl-st-spend spend-${strip.spendLevel || "normal"} zl-${statusItemPriority("spend")}`}>
                  {spend}
                </span>
              )}
            </>
          );
          const label = [
            hasCtx ? `Context ${ctxPercent}% used` : null,
            hasSpend ? `${spend} spent` : null,
          ].filter(Boolean).join(", ");
          return usageTrigger ? (
            <button type="button" class="zl-st zl-st-ctx zl-p1" onClick={onOpenUsage} aria-label={`${label} — show usage`}>
              {body}
            </button>
          ) : (
            <span class="zl-st zl-st-ctx zl-p1" title={label}>{body}</span>
          );
        })()}

        {showTokens && hasTokens && (
          <span class={`zl-st zl-st-tok zl-data zl-${statusItemPriority("tokens")}`} title="Tokens this run">
            <TokenFlow up={tokensUp} down={tokensDown} />
          </span>
        )}
      </div>
      {children}
    </div>
  );
}
