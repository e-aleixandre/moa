import { RotateCcw } from "lucide-preact";
import { StateDot } from "../../../primitives/index.js";
import { ModelPill } from "../../../components/index.js";
import "./MobileHeader.css";

// MobileHeader — session header for the mobile conversation screen.
// Top row: state dot + title + ModelPill (glyph). Mono subrow
// with path · ctx · rewind. Below, a grab handle + "pull for sessions" which
// is actually an accessible button (onOpenSessions) — the drag gesture
// arrives in 4B; here it's just the tap shortcut.
export function MobileHeader({
  state = "idle",
  title,
  model,
  level,
  path,
  ctx,
  onOpenSessions,
}) {
  return (
    <header class="mhead">
      <div class="mhead-row">
        <StateDot state={state} size={9} />
        <span class="mhead-title">{title}</span>
        <ModelPill
          model={model}
          level={level}
          variant="glyph"
          accent="lavender"
        />
      </div>
      <div class="mhead-sub">
        <span class="mhead-meta">
          {path} · ctx {ctx}% ·{" "}
        </span>
        <span class="mhead-rewind">
          <RotateCcw size={11} aria-hidden="true" /> rewind
        </span>
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
