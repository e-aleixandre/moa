import { Kbd } from "../../primitives/index.js";
import { formatShortcut } from "../../data/util/shortcut.js";
import "./ChatHead.css";

// ChatHead — the conversation's name. Rewind lives on each user message.
// Model and permissions live on the status strip.
export function ChatHead({
  title = "ws race fix",
  path = "~/dev/moa/main",
  onTitleClick,
  onGridToggle,
  settingsPopover,
  settingsAnchorRef,
}) {
  const Title = onTitleClick ? "button" : "span";
  return (
    <header class="chat-head">
      <div class="crumb" ref={settingsAnchorRef}>
        <Title type={onTitleClick ? "button" : undefined} class="crumb-title" onClick={onTitleClick}>
          {title}
        </Title>
        {onTitleClick && <span class="crumb-caret" aria-hidden="true">▾</span>}
        {path && <span class="crumb-path">{path}</span>}
        {settingsPopover}
      </div>

      {onGridToggle && (
        <div class="head-actions">
          <button type="button" class="grid-toggle" onClick={onGridToggle} title="Back to the grid — this session stays in pane 1">
            <span class="mini" aria-hidden="true"><i /><i /><i /></span>
            grid
            <Kbd>{formatShortcut("G", { mod: true })}</Kbd>
          </button>
        </div>
      )}
    </header>
  );
}
