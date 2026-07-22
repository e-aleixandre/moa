import { Rewind, Maximize2, X, GripHorizontal, Columns2, Rows2 } from "lucide-preact";
import { StateDot, IconButton, ThinkingMeter } from "../../primitives/index.js";
import { formatShortcut } from "../../data/util/shortcut.js";
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
//
// 5G — the pane is now the leaf of the REAL tile tree. All the wiring props
// below are OPTIONAL and retro-compatible: without them the component renders
// exactly as the galleries expect (the mock `p-input` and no drag/split/focus
// affordances). The connected grid (PaneGrid) supplies them:
//   tileNumber   — the pane's DFS index+1 badge (⌘+N target).
//   onSplitRight/onSplitDown — split this pane horizontally/vertically.
//   onFocus      — click-to-focus this pane (the leaf owns the caret logic).
//   canClose     — whether the close button is shown (never below 1 tile).
//   draggable + onDragStart / touchDrag / onDragOver/Leave/Drop — HTML5 + touch
//                  drag-and-drop of sessions between panes and from the Spine.
//   dragOver     — highlight while a drag hovers this pane.
//   empty        — render the "Drag a session here" dropzone instead of body.
//   composer     — the REAL Composer node, replacing the mock `p-input`.
//   blocking     — a slot (McpBanner/permission/ask_user) above the composer.
//   paneRef / dataTileId — the section element ref + data-tile-id (focusTile
//                  queries the tile's textarea by this attribute).
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
  // --- 5G connected props (all optional) ---
  tileNumber,
  onSplitRight,
  onSplitDown,
  onFocus,
  canClose = true,
  draggable = false,
  onDragStart,
  touchDrag,
  onDragOver,
  onDragLeave,
  onDrop,
  dragOver = false,
  attention = false,
  empty = false,
  composer,
  status,
  dock,
  blocking,
  paneRef,
  dataTileId,
  bodyLive = false,
}) {
  const classes = [
    "pane",
    variant === "tall" ? "p-tall" : "",
    focused ? "focused" : "",
    dragOver ? "drag-over" : "",
    attention ? `attention ${state === "error" ? "errored" : "blocked"}` : "",
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

  const dragProps = draggable
    ? { draggable: true, onDragStart, ...(touchDrag || {}) }
    : {};

  return (
    <section
      ref={paneRef}
      data-tile-id={dataTileId}
      class={classes}
      aria-label={`${title || "Empty"}, ${stateText}`}
      onClick={onFocus}
      onDragOver={onDragOver}
      onDragLeave={onDragLeave}
      onDrop={onDrop}
    >
      {focused && <span class="focus-tag">FOCUS</span>}

      <div class={draggable ? "p-head p-head-drag" : "p-head"} {...dragProps}>
        {draggable && <GripHorizontal size={13} class="p-grip" aria-hidden="true" />}
        {tileNumber != null && (
          <span class="p-badge" title={formatShortcut(String(tileNumber), { mod: true })}>{tileNumber}</span>
        )}
        <StateDot state={state} size={9} label={stateText} />
        <button
          type="button"
          class="p-title"
          style={titleTone ? { color: `var(--${titleTone})` } : undefined}
          onClick={onTitleClick}
        >
          {title || "Empty"}
        </button>
        {path && <span class="p-path">{path}</span>}
        {model && (
          <span class="p-model">
            {model} <ThinkingMeter variant="glyph" level={thinkingLevel} label={`Thinking: ${thinkingLevel}`} />
          </span>
        )}

        <div class="p-tools">
          <IconButton label="Rewind" onClick={onRewind}>
            <Rewind size={15} />
          </IconButton>
          {onSplitRight && (
            <IconButton label="Split right" onClick={onSplitRight}>
              <Columns2 size={15} />
            </IconButton>
          )}
          {onSplitDown && (
            <IconButton label="Split down" onClick={onSplitDown}>
              <Rows2 size={15} />
            </IconButton>
          )}
          <div class="p-max-wrap">
            <IconButton label="Maximize into conversation view" onClick={onMaximize}>
              <Maximize2 size={15} />
            </IconButton>
            <span class="p-max-tip" aria-hidden="true">→ conversation view</span>
          </div>
          {canClose && (
            <IconButton label="Close pane" variant="ghost" className="p-close" onClick={onClose}>
              <X size={15} />
            </IconButton>
          )}
        </div>
      </div>

      <div class={bodyLive ? "p-body p-body-live" : "p-body"}>{children}</div>

      {footer && <div class="p-foot">{footer}</div>}

      {blocking && <div class="p-blocking">{blocking}</div>}

      {dock && <div class="p-dock">{dock}</div>}

      {/* Connected pane: the REAL Composer replaces the mock input. The mock
          `p-input` survives only for the galleries (no `composer` prop). */}
      {composer
        ? composer
        : !hideComposer && (
            <div class="p-input">
              <span class="p-input-text">Message moa…</span>
              <span class="send" aria-hidden="true">↑</span>
          </div>
        )}

      {status && <div class="p-status">{status}</div>}
    </section>
  );
}
