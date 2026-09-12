import { PRESETS } from "../../data/layoutPresets.js";
import { formatShortcut } from "../../data/util/shortcut.js";
import "./GridToolbar.css";

// GridToolbar — the grid's top bar. Markup and CSS are the catalogue's
// (catalog/zones-lab.jsx `Grid` `.zl-grid-bar` / `.zl-grid-needs`,
// zones-lab.css the same block), MOVED here rather than imitated. The
// catalogue imports this component now, which is what makes one definition
// rather than two.
//
// What is NOT the catalogue's is everything the prototype never had, grafted
// on top: layout presets, split actions, and the shortcut hint. Those only
// mount when the connected screen supplies the callbacks, so a fixture that
// only needs "Layout · 3 panes" and "1 needs you" still draws the accepted
// bar.

function SplitRightIcon() {
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

export function GridToolbar({
  paneCount = 3,
  activePreset = null,
  onPresetSelect,
  onSplitRight,
  onSplitDown,
  needsYouCount = 0,
  onAttentionClick,
}) {
  const needs = needsYouCount > 0 && (
    onAttentionClick ? (
      <button type="button" class="zl-grid-needs" onClick={onAttentionClick}>
        <span class="zl-data">{needsYouCount}</span> needs you
      </button>
    ) : (
      <span class="zl-grid-needs"><span class="zl-data">{needsYouCount}</span> needs you</span>
    )
  );

  return (
    <header class="zl-grid-bar">
      <span class="zl-grid-bar-t">
        Layout · <span class="zl-data">{paneCount}</span> pane{paneCount === 1 ? "" : "s"}
      </span>

      {onPresetSelect && (
        <div class="zl-grid-presets" role="group" aria-label="Layout presets">
          {PRESETS.map((p) => {
            const on = activePreset === p.id;
            return (
              <button
                type="button"
                key={p.id}
                class={`zl-grid-preset${on ? " is-on" : ""}`}
                aria-pressed={on}
                title={p.label}
                onClick={() => onPresetSelect(p.id)}
              >
                <span class="zl-grid-preset-mini" style={p.miniStyle}>
                  {p.cells.map((cell, i) => (
                    <i key={i} style={cell} />
                  ))}
                </span>
              </button>
            );
          })}
        </div>
      )}

      {onSplitRight && (
        <button type="button" class="zl-desk-act" onClick={onSplitRight} title="Split right" aria-label="Split right">
          <SplitRightIcon />
        </button>
      )}
      {onSplitDown && (
        <button type="button" class="zl-desk-act" onClick={onSplitDown} title="Split down" aria-label="Split down">
          <SplitDownIcon />
        </button>
      )}

      {(onSplitRight || onSplitDown || onPresetSelect) && (
        <span class="zl-grid-hint">
          {formatShortcut("1–9", { mod: true })} focus a pane
        </span>
      )}

      {needs}
    </header>
  );
}
