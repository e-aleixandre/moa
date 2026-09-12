import { Dot } from "../../components/SessionRow/SessionRow.jsx";
import { formatShortcut } from "../../data/util/shortcut.js";
import "./Pane.css";

// Pane — a single grid panel. Markup and CSS are the catalogue's
// (catalog/zones-lab.jsx `Pane`, zones-lab.css the `.zl-pane*` block), MOVED
// here rather than imitated: the class names travelled with the rules, so the
// pane IS the accepted design instead of a translation of it. The catalogue
// imports this component now, which is what makes one definition rather than
// two.
//
// What is NOT the catalogue's is everything the prototype never had, grafted
// on top: drag between panes, split down / maximize / close, the empty
// dropzone, a live Stream in the body, blocking cards above the composer,
// and the real Composer / LiveBar / StatusStrip the connected grid supplies.
//
// The catalogue's own head: state dot, title + path as one button, a ⌘N
// chip, then the two actions the prototype drew (preview, split). Focus is
// told by the head — title in t1, a raised head tone — not by a ring, a
// shadow or a "FOCUS" tag.

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

function SplitDownIcon() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true">
      <rect x="2" y="3" width="12" height="10" rx="2" fill="none" stroke="currentColor" stroke-width="1.5" />
      <path d="M2 8h12" stroke="currentColor" stroke-width="1.5" />
    </svg>
  );
}

function MaximizeIcon() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true">
      <path d="M5.5 3.5H3.5v2M10.5 3.5h2v2M5.5 12.5H3.5v-2M10.5 12.5h2v-2" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" />
    </svg>
  );
}

function CloseIcon() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true">
      <path d="M4.5 4.5l7 7M11.5 4.5l-7 7" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" />
    </svg>
  );
}

function GripIcon() {
  return (
    <svg class="zl-pane-grip" viewBox="0 0 16 16" aria-hidden="true">
      <path d="M5.5 4h5M5.5 8h5M5.5 12h5" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" />
    </svg>
  );
}

const STATE_LABEL = {
  running: "running",
  permission: "requires permission",
  error: "error",
  saved: "saved",
  idle: "idle",
  unseen: "new result",
};

export function Pane({
  title,
  path,
  state = "idle",
  focused = false,
  variant = "normal",
  titleTone,
  onTitleClick,
  onMaximize,
  onClose,
  children,
  footer,
  hideComposer = false,
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
  overlay,
  onPreviewToggle,
  previewOpen = false,
  headExtra,
}) {
  const stateText = STATE_LABEL[state] ?? state;
  const classes = [
    "zl-pane",
    focused ? "is-focus" : "",
    variant === "tall" ? "is-tall" : "",
    dragOver ? "is-drag" : "",
    empty ? "is-empty" : "",
    attention ? "is-attention" : "",
  ].filter(Boolean).join(" ");

  const ariaName = tileNumber != null
    ? `Pane ${tileNumber}: ${title || "Empty"}`
    : (title || "Empty");

  const dragProps = draggable
    ? { draggable: true, onDragStart, ...(touchDrag || {}) }
    : {};

  const showDock = !hideComposer && (composer || dock || status);

  return (
    <section
      ref={paneRef}
      data-tile-id={dataTileId}
      class={classes}
      aria-label={`${ariaName}, ${stateText}`}
      onClick={onFocus}
      onDragOver={onDragOver}
      onDragLeave={onDragLeave}
      onDrop={onDrop}
    >
      <div class={draggable ? "zl-pane-head is-drag" : "zl-pane-head"} {...dragProps}>
        {draggable && <GripIcon />}
        <Dot state={state} />
        <button
          type="button"
          class="zl-pane-title"
          style={titleTone ? { color: `var(--${titleTone})` } : undefined}
          onClick={onTitleClick}
        >
          <span class="zl-pane-t">{title || "Empty"}</span>
          {path && <span class="zl-pane-path zl-data">{path}</span>}
        </button>
        <div class="zl-pane-tools">
          {tileNumber != null && (
            <kbd class="zl-kbd zl-data" title={`Focus with ${formatShortcut(String(tileNumber), { mod: true })}`}>
              ⌘{tileNumber}
            </kbd>
          )}
          {headExtra}
          <button
            type="button"
            class={`zl-desk-act${previewOpen ? " is-on" : ""}`}
            aria-label="Live preview"
            aria-pressed={previewOpen || undefined}
            onClick={onPreviewToggle}
          >
            <PreviewIcon />
          </button>
          <button
            type="button"
            class="zl-desk-act"
            aria-label="Split right"
            onClick={onSplitRight}
          >
            <SplitIcon />
          </button>
          {onSplitDown && (
            <button type="button" class="zl-desk-act" aria-label="Split down" onClick={onSplitDown}>
              <SplitDownIcon />
            </button>
          )}
          {onMaximize && (
            <button
              type="button"
              class="zl-desk-act"
              aria-label="Maximize into conversation view"
              title="→ conversation view"
              onClick={onMaximize}
            >
              <MaximizeIcon />
            </button>
          )}
          {canClose && onClose && (
            <button type="button" class="zl-desk-act is-close" aria-label="Close pane" onClick={onClose}>
              <CloseIcon />
            </button>
          )}
        </div>
      </div>

      <div class="zl-pane-body">{children}</div>

      {footer && <div class="zl-pane-foot">{footer}</div>}
      {blocking && <div class="zl-pane-blocking">{blocking}</div>}

      {showDock && (
        <div class="zl-dock is-pane">
          {dock}
          {composer}
          {status}
        </div>
      )}
      {overlay}
    </section>
  );
}
