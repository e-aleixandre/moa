import { RotateCcw, Bell } from "lucide-preact";
import { StateDot } from "../../../primitives/index.js";
import { ModelPill } from "../../../components/index.js";
import { modelAccent } from "../../../data/selectors.js";
import "./MobileHeader.css";

// MobileHeader — session header for the mobile conversation screen.
// Top row: state dot + title + ModelPill (glyph) + notifications bell. Mono
// subrow with path · ctx · rewind. Below, a grab handle + "pull for sessions"
// which is actually an accessible button (onOpenSessions) — the drag gesture
// arrives in 4B; here it's just the tap shortcut.
export function MobileHeader({
  state = "idle",
  title,
  model,
  level,
  path,
  ctx,
  onOpenSessions,
  onRewind,
  rewindDisabled = false,
  onNotifications,
  notifPopover,
  notifAnchorRef,
}) {
  const hasCtx = typeof ctx === "number" && ctx >= 0;
  return (
    <header class="mhead">
      <div class="mhead-row">
        <StateDot state={state} size={9} />
        <span class="mhead-title">{title}</span>
        {model && (
          <ModelPill
            model={model}
            level={level}
            variant="glyph"
            accent={modelAccent(model)}
          />
        )}
        <div class="mhead-notif" ref={notifAnchorRef}>
          <button
            type="button"
            class="mhead-bell"
            aria-label="Notifications"
            onClick={onNotifications}
          >
            <Bell size={16} aria-hidden="true" />
          </button>
          {notifPopover}
        </div>
      </div>
      <div class="mhead-sub">
        <span class="mhead-meta">
          {path}
          {hasCtx ? ` · ctx ${ctx}%` : ""} ·{" "}
        </span>
        <button
          type="button"
          class="mhead-rewind"
          onClick={onRewind}
          disabled={rewindDisabled || !onRewind}
          aria-label="Rewind"
        >
          <RotateCcw size={11} aria-hidden="true" /> rewind
        </button>
      </div>
      <button
        type="button"
        class="mhead-grab"
        aria-label="Open sessions"
        onClick={onOpenSessions}
      >
        <span class="mhead-handle" aria-hidden="true" />
        <span class="mhead-hint">pull for sessions</span>
      </button>
    </header>
  );
}
