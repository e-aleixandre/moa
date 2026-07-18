import "./StreamingSkeleton.css";

// StreamingSkeleton — "incoming text" block with shimmer bars, for
// content that hasn't been emitted by the model yet. Lives in components/
// because it's a content piece inside the stream (like AssistantDocument
// with streaming=true, of which it's the visual complement: the paragraph that
// already arrived carries AssistantDocument's blinking cursor, and below goes this
// skeleton for what's still to come).
//
// `widths` defaults to reproducing the mockup (92%/78%/45%, decreasing).
const DEFAULT_WIDTHS = ["92%", "78%", "45%"];

export function StreamingSkeleton({ widths = DEFAULT_WIDTHS, className = "", ...rest }) {
  return (
    <div class={`skel-block ${className}`.trim()} {...rest}>
      {widths.map((w, i) => (
        <div key={i} class="skel-line" style={{ width: w }} />
      ))}
    </div>
  );
}

// TypingDots — three bouncing mauve dots, to signal that the assistant
// is still "thinking/writing" at the end of a paragraph (e.g. "…I'll draft
// the notes against it •••"). Small, independent component because it's
// used inline, unlike the StreamingSkeleton block.
export function TypingDots({ label = "typing", className = "", ...rest }) {
  return (
    <span class={`typing-dots ${className}`.trim()} role="img" aria-label={label} {...rest}>
      <i />
      <i />
      <i />
    </span>
  );
}
