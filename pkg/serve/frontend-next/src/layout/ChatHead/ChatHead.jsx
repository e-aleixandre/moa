import { Rewind, Bell, MoreHorizontal } from "lucide-preact";
import { StateDot, Kbd, IconButton } from "../../primitives/index.js";
import { ModelPill } from "../../components/index.js";
import "./ChatHead.css";

// ChatHead — main column header: session breadcrumb
// (state + title + path) and actions (model, grid toggle, rewind,
// notifications, session settings).
export function ChatHead({
  title = "ws race fix",
  state = "running",
  path = "~/dev/moa/main",
  model = "sol",
  modelAccent = "lavender",
  thinkingLevel = "high",
  onTitleClick,
  onGridToggle,
  onRewind,
  onNotifications,
  onSessionSettings,
  onModelClick,
}) {
  return (
    <header class="chat-head">
      <div class="crumb">
        <StateDot state={state} size={9} />
        <button type="button" class="crumb-title" onClick={onTitleClick}>
          {title}
        </button>
        <span class="crumb-caret" aria-hidden="true">▾</span>
        <span class="crumb-path">{path}</span>
      </div>

      <div class="head-actions">
        <ModelPill
          model={model}
          accent={modelAccent}
          variant="bars"
          level={thinkingLevel}
          onClick={onModelClick}
        />

        <button type="button" class="grid-toggle" onClick={onGridToggle} title="Back to the grid — this session stays in pane 1">
          <span class="mini" aria-hidden="true"><i /><i /><i /></span>
          grid
          <Kbd>⌘G</Kbd>
        </button>

        <IconButton label="Rewind" onClick={onRewind}>
          <Rewind size={16} />
        </IconButton>
        <IconButton label="Notifications" onClick={onNotifications}>
          <Bell size={16} />
        </IconButton>
        <IconButton label="Session settings" onClick={onSessionSettings}>
          <MoreHorizontal size={16} />
        </IconButton>
      </div>
    </header>
  );
}
