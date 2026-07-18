import "./StatusStrip.css";

// StatusStrip — mono strip under the composer: context ring, tokens,
// current task and today's spend. Every segment is optional: the connected
// container (ConversationScreen) passes only the data it actually has, and any
// missing segment is hidden rather than shown with an invented value.
export function StatusStrip({
  ctxPercent,
  tokensUp,
  tokensDown,
  task,
  spend,
}) {
  const hasCtx = typeof ctxPercent === "number" && ctxPercent >= 0;
  const hasTokens = tokensUp != null && tokensDown != null;
  const ringStyle = hasCtx
    ? { background: `conic-gradient(var(--teal) 0 ${ctxPercent}%, var(--surface0) ${ctxPercent}% 100%)` }
    : undefined;
  return (
    <div class="status-strip">
      {hasCtx && (
        <span class="status-strip-ctx">
          <span class="status-strip-ring" style={ringStyle} aria-hidden="true" />
          ctx {ctxPercent}%
        </span>
      )}
      {hasTokens && <span>↑ {tokensUp} · ↓ {tokensDown} tok</span>}
      {task && <span class="status-strip-task">{task}</span>}
      {spend && <span class="status-strip-spend">today <b>{spend}</b></span>}
    </div>
  );
}
