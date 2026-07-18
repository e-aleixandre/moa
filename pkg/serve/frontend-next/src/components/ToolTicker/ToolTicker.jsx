import "./ToolTicker.css";

// ToolTicker — mono lines that rotate vertically to convey activity
// without needing to be read closely (used inside a Pane when
// subagents are working but there's no room/need for a full
// ActivityLedger). It's its own component in components/ (not an extension of
// Pane) because the vertical cycle animation is a self-contained
// content piece, reusable outside the grid if needed.
//
// `lines`: array of { id?, tool, text } — at most 3 coexist on screen at
// once in the mockup (2s staggered delays over a 6s cycle). The
// delay is positional (depends on the index); `id` is just the stable key.
export function ToolTicker({ lines = [], className = "", ...rest }) {
  return (
    <div class={`tool-ticker ${className}`.trim()} aria-hidden="true" {...rest}>
      {lines.slice(0, 3).map((l, i) => (
        <div class="tk" key={l.id ?? `${l.tool}:${l.text}`} style={{ animationDelay: `${i * 2}s` }}>
          <b>{l.tool}</b> {l.text}
        </div>
      ))}
    </div>
  );
}
