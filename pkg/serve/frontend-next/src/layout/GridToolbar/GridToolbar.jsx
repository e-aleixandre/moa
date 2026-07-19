import { SquareSplitHorizontal, SquareSplitVertical, Bell } from "lucide-preact";
import { PRESETS } from "../../data/layoutPresets.js";
import { formatShortcut } from "../../data/util/shortcut.js";
import "./GridToolbar.css";

// GridToolbar — top bar of the grid view: occupies the same slot as
// ChatHead in the conversation view. Layout presets + splits + hint +
// attention lamp (on the right).
//
// 5G — connected to the REAL layout presets (data/layoutPresets.js). Each mini
// preview is drawn with the preset's own `miniStyle`/`cells` (CSS grid), and a
// click calls onPresetSelect(preset.id) → applyPreset. The active preset is
// highlighted when `activePreset` matches its id (the container detects a match
// by comparing the current tree's shape to presetTree(id); when nothing matches
// — a freeform layout — no preset is highlighted).
export function GridToolbar({
  paneCount = 3,
  activePreset = null,
  onPresetSelect,
  onSplitRight,
  onSplitDown,
  needsYouCount = 0,
  onAttentionClick,
}) {
  return (
    <header class="grid-toolbar">
      <span class="gt-label">
        Layout <b>· {paneCount} pane{paneCount === 1 ? "" : "s"}</b>
      </span>

      <div class="presets" role="group" aria-label="Layout presets">
        {PRESETS.map((p) => {
          const on = activePreset === p.id;
          return (
            <button
              type="button"
              key={p.id}
              class={`preset${on ? " on" : ""}`}
              aria-pressed={on}
              title={p.label}
              onClick={() => onPresetSelect?.(p.id)}
            >
              <span class="preset-mini" style={p.miniStyle}>
                {p.cells.map((cell, i) => (
                  <i key={i} style={cell} />
                ))}
              </span>
            </button>
          );
        })}
      </div>

      <button type="button" class="gt-btn" onClick={onSplitRight} title="Split right">
        <SquareSplitHorizontal size={13} aria-hidden="true" /> split
      </button>
      <button type="button" class="gt-btn" onClick={onSplitDown} title="Split down">
        <SquareSplitVertical size={13} aria-hidden="true" /> split
      </button>

      <span class="gt-hint">
        {formatShortcut("1–9", { mod: true })} focus a pane · ⤢ maximizes into conversation view
      </span>

      <div class="gt-right">
        {needsYouCount > 0 && (
          <button type="button" class="attn-lamp" onClick={onAttentionClick}>
            <Bell size={13} aria-hidden="true" />
            <span class="n">{needsYouCount}</span> needs you
          </button>
        )}
      </div>
    </header>
  );
}
