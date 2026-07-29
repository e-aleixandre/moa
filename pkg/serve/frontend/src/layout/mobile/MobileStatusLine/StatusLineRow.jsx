import { ModelPill, TokenFlow } from "../../../components/index.js";
import { Plug } from "lucide-preact";
import "./MobileStatusLine.css";

// StatusLineRow — the face of the mobile status line: context ring + cost,
// model + thinking, permission mode, and the token heartbeat, in that order.
//
// It exists because the line is worn by two screens that must not drift apart:
// the conversation (MobileStatusLine, whose segments are doors onto sheets) and
// the subagent (MobileSubagentView, where the same segments are read-only
// because nothing about a child is set from inside the fork). Sharing the
// markup — not just the stylesheet — is what makes "the same line" true by
// construction instead of by discipline.
//
// Interactivity is per segment: pass a handler and that segment is a button;
// omit it and it renders as a span with the identical class, so the two screens
// are pixel-identical and only the affordance differs. A segment whose datum is
// missing (unknown context, no spend yet, no tokens moved) is omitted rather
// than shown hollow.
export function StatusLineRow({
  contextPercent,
  contextLabel,
  cost,
  spendLevel = "normal",
  model,
  modelAccent = "lavender",
  modelLabel,
  thinking = "off",
  perm,
  mcp,
  onMcp,
  mcpOpen,
  tokensUp = 0,
  tokensDown = 0,
  onContext,
  onModel,
  onPerm,
  contextOpen,
  modelOpen,
  permOpen,
  children,
}) {
  const hasCtx = typeof contextPercent === "number" && contextPercent >= 0;
  const ringPct = hasCtx ? Math.min(100, Math.max(0, contextPercent)) : 0;
  const hasTokens = tokensUp > 0 || tokensDown > 0;

  // The ring and the cost share a segment but not a fate: an unknown window
  // (a model with no published context size, a subagent finished before the
  // reading existed) must not take the spend down with it — that is money
  // actually spent, and hiding it because of an unrelated unknown is the kind
  // of small lie a telemetry line can't afford.
  const hasSegment = hasCtx || !!cost;
  // The label has to describe what is actually drawn, not what the segment is
  // usually for.
  const segmentLabel = hasCtx ? contextLabel : `Estimated spend ~${cost}`;
  const ctxInner = (
    <>
      {hasCtx && (
        <>
          <svg class="msl-ring" viewBox="0 0 36 36" aria-hidden="true">
            <circle class="t" cx="18" cy="18" r="15.5" pathLength="100" />
            <circle
              class="f"
              cx="18"
              cy="18"
              r="15.5"
              pathLength="100"
              stroke-dasharray={`${ringPct} ${100 - ringPct}`}
            />
          </svg>
          <span class="msl-ctx-pct">{contextPercent}%</span>
        </>
      )}
      {cost && (
        <span class={`msl-cost spend-${spendLevel}`} aria-hidden="true">
          ~{cost}
        </span>
      )}
    </>
  );

  return (
    <div class="mstatusline">
      {hasSegment &&
        (onContext ? (
          <button
            type="button"
            class="msl-ctx"
            onClick={onContext}
            aria-haspopup="dialog"
            aria-expanded={contextOpen}
            aria-label={segmentLabel}
          >
            {ctxInner}
          </button>
        ) : (
          <span class="msl-ctx" aria-label={segmentLabel}>
            {ctxInner}
          </span>
        ))}

      {hasSegment && model && <span class="msl-div" aria-hidden="true" />}

      {model && (
        <ModelPill
          model={model}
          accent={modelAccent}
          variant="bars"
          level={thinking}
          readOnly={!onModel}
          onClick={onModel}
          aria-haspopup={onModel ? "dialog" : undefined}
          aria-expanded={onModel ? modelOpen : undefined}
          aria-label={modelLabel}
        />
      )}

      {model && perm && <span class="msl-div" aria-hidden="true" />}

      {perm &&
        (onPerm ? (
          <button
            type="button"
            class="msl-perm"
            onClick={onPerm}
            aria-haspopup="dialog"
            aria-expanded={permOpen}
            aria-label={`Permission mode: ${perm.toUpperCase()} — change`}
          >
            <span class={`perm-chip perm-${perm}`} aria-hidden="true">
              {perm}
            </span>
          </button>
        ) : (
          <span class="msl-perm" aria-label={`Permission mode: ${perm.toUpperCase()}`}>
            <span class={`perm-chip perm-${perm}`} aria-hidden="true">
              {perm}
            </span>
          </span>
        ))}

      <span class="msl-spacer" aria-hidden="true" />

      {/* MCP health — present only when the session has servers. A plug + count
          normally; the alert variant (any server failed/exited) turns red and is
          what makes a crashed Playwright glanceable without opening anything. */}
      {mcp && mcp.total > 0 && (() => {
        const unhealthy = mcp.unhealthy > 0;
        const disabledNote = mcp.disabled > 0 ? `, ${mcp.disabled} disabled` : "";
        const label = unhealthy
          ? `MCP: ${mcp.unhealthy} of ${mcp.total} need attention${disabledNote} — open MCP servers`
          : `MCP: ${mcp.total} server${mcp.total === 1 ? "" : "s"}${disabledNote} — open MCP servers`;
        const inner = (
          <span class={`msl-mcp-chip${unhealthy ? " msl-mcp-bad" : ""}`} aria-hidden="true">
            <Plug />
            {unhealthy ? `${mcp.unhealthy}!` : mcp.total}
          </span>
        );
        return onMcp ? (
          <button
            type="button"
            class="msl-mcp"
            onClick={onMcp}
            aria-haspopup="dialog"
            aria-expanded={mcpOpen}
            aria-label={label}
          >
            {inner}
          </button>
        ) : (
          <span class="msl-mcp" aria-label={label}>
            {inner}
          </span>
        );
      })()}

      {hasTokens && (
        <span class="msl-tokens">
          <TokenFlow up={tokensUp} down={tokensDown} variant="compact" />
        </span>
      )}

      {/* Sheets and other non-visual siblings the owning screen wants inside
          the same node. */}
      {children}
    </div>
  );
}
