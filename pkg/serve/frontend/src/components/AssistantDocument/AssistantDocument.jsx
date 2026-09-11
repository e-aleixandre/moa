import { sanitizeHtml } from "../../util/sanitize.js";
import "./AssistantDocument.css";

// AssistantDocument — a turn of assistant work. Markup and CSS are the
// catalogue's (catalog/zones-lab.jsx the `.zl-turn` / `.zl-prose` block),
// MOVED here rather than imitated. The turn has no frame: the canvas IS the
// page, and only objects on it (ledger, artifact) get a tone. The catalogue
// imports these components now, which is what makes one definition rather
// than two.
//
// `Prose` is the reading text inside a turn. Production feeds it sanitized
// markdown (renderMarkdown / renderMarkdownWithCaret); the catalogue's
// streaming lab feeds it burst spans. Same class, two sources.
//
// What is NOT the catalogue's is everything markdown can emit that the lab
// never drew by hand: fenced code, tables, headings other than h3, links,
// quotations, rules. Those keep the catalogue's tonal ladder rather than
// the old mauve/sky decoration.
//
// The caret is the catalogue's: a 2px block in the text colour, steady while
// words arrive, blinking only when idle (thinking, or waiting on the next
// delta). It is NOT green — green means running, and a write-head is not a
// status dot.

export function Prose({
  streaming = false,
  done = false,
  live = false,
  className = "",
  children,
  html,
  ...rest
}) {
  const busy = streaming && !done;
  const cls = `zl-prose${busy ? " is-streaming" : ""}${done ? " is-done" : ""}${live ? " is-live" : ""}${className ? ` ${className}` : ""}`;
  if (html != null) {
    return (
      <div
        class={cls}
        aria-busy={busy || undefined}
        dangerouslySetInnerHTML={{ __html: html }}
        {...rest}
      />
    );
  }
  return (
    <div class={cls} aria-busy={busy || undefined} {...rest}>
      {children}
    </div>
  );
}

export function AssistantDocument({
  children,
  html,
  streaming = false,
  className = "",
  ...rest
}) {
  if (html != null) {
    return (
      <div class={`zl-turn${className ? ` ${className}` : ""}`} {...rest}>
        <Prose html={sanitizeHtml(html)} streaming={streaming} live={streaming} />
        {streaming && <span class="zl-caret is-idle" aria-hidden="true" />}
      </div>
    );
  }
  return (
    <div class={`zl-turn${className ? ` ${className}` : ""}`} {...rest}>
      {children}
      {streaming && <span class="zl-caret is-idle" aria-hidden="true" />}
    </div>
  );
}
