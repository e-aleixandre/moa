import { Rewind, Maximize2, X } from "lucide-preact";
import { StateDot, IconButton, ThinkingMeter } from "../../primitives/index.js";
import "./Pane.css";

// Pane — a single grid panel. Reuses StateDot/IconButton/
// ThinkingMeter from the primitives; the header reproduces ChatHead's
// compact pattern but denser (this lives in a grid, not in the main
// column).
//
// The mono model badge ("sol ▰▰▱▱" in the mockup) is built with
// ThinkingMeter variant="glyph" instead of literal text: this way the
// thinking level stays accessible/real instead of a static string, and the
// level logic already living in the primitive isn't duplicated.
//
// `footer` (Phase 3B) — optional slot for the pane's pulse ("● streaming",
// "waiting 0:42"…), like in the "grid alive" section of the live
// states mockup. `hideComposer` hides the fake input when the footer already
// visually closes the panel (avoids duplicating send affordances in the live
// panes of the demo gallery).
export function Pane({
  title,
  path,
  state = "idle",
  model,
  modelAccent = "lavender",
  thinkingLevel = "off",
  focused = false,
  variant = "normal",
  titleTone,
  onTitleClick,
  onRewind,
  onMaximize,
  onClose,
  children,
  footer,
  hideComposer = false,
}) {
  const classes = [
    "pane",
    variant === "tall" ? "p-tall" : "",
    focused ? "focused" : "",
  ]
    .filter(Boolean)
    .join(" ");

  // The pane's state (running/permission/error) is encoded via color and
  // animation on the StateDot; for screen readers we expose it as
  // text in the panel's and the dot's accessible names.
  const STATE_LABEL = {
    running: "running",
    permission: "requires permission",
    error: "error",
    saved: "saved",
    idle: "idle",
  };
  const stateText = STATE_LABEL[state] ?? state;

  return (
    <section class={classes} aria-label={`${title}, ${stateText}`}>
      {focused && <span class="focus-tag">FOCUS</span>}

      <div class="p-head">
        <StateDot state={state} size={9} label={stateText} />
        <button
          type="button"
          class="p-title"
          style={titleTone ? { color: `var(--${titleTone})` } : undefined}
          onClick={onTitleClick}
        >
          {title}
        </button>
        {path && <span class="p-path">{path}</span>}
        {model && (
          <span class="p-model">
            {model} <ThinkingMeter variant="glyph" level={thinkingLevel} label={`Thinking: ${thinkingLevel}`} />
          </span>
        )}

        <div class="p-tools">
          <IconButton label="Rewind" onClick={onRewind}>
            <Rewind size={13} />
          </IconButton>
          <div class="p-max-wrap">
            <IconButton label="Maximize into conversation view" onClick={onMaximize}>
              <Maximize2 size={12} />
            </IconButton>
            <span class="p-max-tip" aria-hidden="true">→ conversation view</span>
          </div>
          <IconButton label="Close pane" variant="ghost" className="p-close" onClick={onClose}>
            <X size={12} />
          </IconButton>
        </div>
      </div>

      <div class="p-body">{children}</div>

      {footer && <div class="p-foot">{footer}</div>}

      {!hideComposer && (
        <div class="p-input">
          <span class="p-input-text">Message moa…</span>
          <span class="send" aria-hidden="true">↑</span>
        </div>
      )}
    </section>
  );
}
