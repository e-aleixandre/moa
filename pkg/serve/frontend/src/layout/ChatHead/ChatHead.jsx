import { Kbd } from "../../primitives/index.js";
import { AppWindow } from "lucide-preact";
import { formatShortcut } from "../../data/util/shortcut.js";
import "./ChatHead.css";

// ChatHead — the conversation's name. Rewind lives on each user message.
// Model and permissions live on the status strip.
export function ChatHead({
  title = "ws race fix",
  path = "~/dev/moa/main",
  onTitleClick,
  onGridToggle,
  onPreviewToggle,
  previewOpen = false,
  settingsPopover,
  settingsAnchorRef,
}) {
  const Title = onTitleClick ? "button" : "span";
  const actions = [
    onPreviewToggle && {
      id: "preview",
      icon: AppWindow,
      label: "preview",
      title: "Live preview — look at your dev server and point the agent at an element",
      active: previewOpen,
      onClick: onPreviewToggle,
    },
    onGridToggle && {
      id: "grid",
      label: "grid",
      title: "Back to the grid — this session stays in pane 1",
      onClick: onGridToggle,
      shortcut: <Kbd>{formatShortcut("G", { mod: true })}</Kbd>,
    },
  ].filter(Boolean);
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

      {actions.length > 0 && (
        <div class="head-actions">
          {actions.map((action) => {
            const Icon = action.icon;
            return <button type="button" key={action.id} data-preview-trigger={action.id === "preview" ? "true" : undefined} class={`${action.id === "preview" ? "head-action-icon" : "grid-toggle"}${action.active ? " is-on" : ""}`} onClick={action.onClick} aria-label={action.id === "preview" ? "Live preview" : undefined} aria-pressed={action.active || undefined} title={action.title}>
              {Icon ? <Icon size={14} aria-hidden="true" /> : <span class="mini" aria-hidden="true"><i /><i /><i /></span>}
              {action.id !== "preview" && action.label}
              {action.id !== "preview" && action.shortcut}
            </button>;
          })}
        </div>
      )}
    </header>
  );
}
