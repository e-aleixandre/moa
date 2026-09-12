import { formatShortcut } from "../../data/util/shortcut.js";
import "./ChatHead.css";

// ChatHead — the conversation's name, the crumb that opens this session's
// dossier. Markup and CSS are the catalogue's (catalog/zones-lab.jsx `Desktop`
// `.zl-desk-head` / `.zl-crumb` / `.zl-desk-act`, zones-lab.css the same
// block), MOVED here rather than imitated: the class names travelled with the
// rules, so the head IS the accepted design instead of a translation of it.
// The catalogue imports this component now, which is what makes one
// definition rather than two.
//
// Rewind lives on each user message. Model and permissions live on the status
// strip. What is NOT the catalogue's is everything the prototype never had,
// grafted on top: the real session title and path, the crumb as a toggle for
// the session panel (rename / archive / delete live there), Artifacts, live
// preview, and the door back to the grid. The prototype drew preview and
// split as inert icons; production wires them. The prototype's spacer is lab
// scaffolding and stayed in the host — actions sit on the right with
// margin-left: auto instead.

function PreviewIcon() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true">
      <rect x="2" y="3" width="12" height="10" rx="2" fill="none" stroke="currentColor" stroke-width="1.5" />
      <path d="M2 6.5h12" stroke="currentColor" stroke-width="1.5" />
    </svg>
  );
}

function SplitIcon() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true">
      <rect x="2" y="3" width="12" height="10" rx="2" fill="none" stroke="currentColor" stroke-width="1.5" />
      <path d="M8 3v10" stroke="currentColor" stroke-width="1.5" />
    </svg>
  );
}

export function ChatHead({
  title = "ws race fix",
  path = "~/dev/moa/main",
  panelOpen = false,
  onTitleClick,
  onGridToggle,
  onPreviewToggle,
  previewOpen = false,
  // headExtra — extra head actions (the Artifacts entry), rendered as a
  // neighbour of the existing ones rather than as a second bar.
  headExtra,
}) {
  const Crumb = onTitleClick ? "button" : "div";
  const hasActions = !!(headExtra || onPreviewToggle || onGridToggle);
  return (
    <header class="zl-desk-head">
      <Crumb
        type={onTitleClick ? "button" : undefined}
        class="zl-crumb"
        onClick={onTitleClick}
        aria-haspopup={onTitleClick ? "dialog" : undefined}
        aria-expanded={onTitleClick ? panelOpen : undefined}
      >
        <span class="zl-crumb-title">{title}</span>
        {path && <span class="zl-crumb-path zl-data">{path}</span>}
      </Crumb>

      {hasActions && (
        <div class="zl-desk-acts">
          {headExtra}
          {onPreviewToggle && (
            <button
              type="button"
              class={`zl-desk-act${previewOpen ? " is-on" : ""}`}
              data-preview-trigger="true"
              onClick={onPreviewToggle}
              aria-label="Live preview"
              aria-pressed={previewOpen || undefined}
              title="Live preview — look at your dev server and point the agent at an element"
            >
              <PreviewIcon />
            </button>
          )}
          {onGridToggle && (
            <button
              type="button"
              class="zl-desk-act"
              onClick={onGridToggle}
              aria-label="Back to the grid"
              title={`Back to the grid — this session stays in pane 1 (${formatShortcut("G", { mod: true })})`}
            >
              <SplitIcon />
            </button>
          )}
        </div>
      )}
    </header>
  );
}
