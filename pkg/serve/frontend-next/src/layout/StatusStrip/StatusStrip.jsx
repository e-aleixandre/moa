import "./StatusStrip.css";

// StatusStrip — tira mono bajo el composer: anillo de contexto, tokens,
// tarea en curso y gasto de hoy.
export function StatusStrip({
  ctxPercent = 62,
  tokensUp = "41.2k",
  tokensDown = "8.7k",
  task = "running full test suite",
  spend = "$1.84",
}) {
  const ringStyle = {
    background: `conic-gradient(var(--teal) 0 ${ctxPercent}%, var(--surface0) ${ctxPercent}% 100%)`,
  };
  return (
    <div class="status-strip">
      <span class="status-strip-ctx">
        <span class="status-strip-ring" style={ringStyle} aria-hidden="true" />
        ctx {ctxPercent}%
      </span>
      <span>↑ {tokensUp} · ↓ {tokensDown} tok</span>
      <span class="status-strip-task">{task}</span>
      <span class="status-strip-spend">today <b>{spend}</b></span>
    </div>
  );
}
