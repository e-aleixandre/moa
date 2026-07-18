import { sanitizeHtml } from "../../util/sanitize.js";
import "./AssistantDocument.css";

// AssistantDocument — "document" flow of the agent's work: paragraphs,
// lists, section headings, inline code. Doesn't impose internal structure: the
// consumer composes the child blocks (paragraphs, ActivityLedger, CodeBlock,
// DiffBlock…) as children, or passes already-rendered `html` (markdown).
// `streaming` paints the blinking cursor at the end of the document, as a
// sibling after the content — same in `html` mode and children mode.
export function AssistantDocument({
  children,
  html,
  streaming = false,
  className = "",
  ...rest
}) {
  const streamClass = streaming ? " is-streaming" : "";
  if (html != null) {
    return (
      <div class={`doc ${streamClass} ${className}`.trim()} {...rest}>
        <div
          class="doc-html"
          dangerouslySetInnerHTML={{ __html: sanitizeHtml(html) }}
        />
        {streaming && <span class="doc-cursor" aria-hidden="true" />}
      </div>
    );
  }
  return (
    <div class={`doc ${streamClass} ${className}`.trim()} {...rest}>
      {children}
      {streaming && (
        <span class="doc-cursor" aria-hidden="true" />
      )}
    </div>
  );
}

