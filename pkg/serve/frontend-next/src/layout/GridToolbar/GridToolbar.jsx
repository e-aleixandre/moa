import { SquareSplitHorizontal, SquareSplitVertical, Bell } from "lucide-preact";
import "./GridToolbar.css";

// PRESETS — each preset draws a mini layout with divs (`i` = tile). The
// active preset is highlighted in peach. They're real buttons (not just decorative)
// so a keyboard/screen-reader user can change the layout.
const PRESETS = [
  { key: "p1", label: "1 pane", tiles: 1 },
  { key: "p2", label: "2 panes", tiles: 2 },
  { key: "p3", label: "3 panes", tiles: 3 },
  { key: "p4", label: "4 panes", tiles: 4 },
];

// GridToolbar — top bar of the grid view: occupies the same slot as
// ChatHead in the conversation view. Layout presets + splits + hint +
// attention lamp (on the right).
export function GridToolbar({
  paneCount = 3,
  preset = "p3",
  onPresetSelect,
  onSplitRight,
  onSplitDown,
  needsYouCount = 1,
  onAttentionClick,
}) {
  return (
    <header class="grid-toolbar">
      <span class="gt-label">
        Layout <b>· {paneCount} panes</b>
      </span>

      <div class="presets" role="group" aria-label="Layout presets">
        {PRESETS.map((p) => (
          <button
            type="button"
            key={p.key}
            class={`preset ${p.key}${preset === p.key ? " on" : ""}`}
            aria-pressed={preset === p.key}
            title={p.label}
            onClick={() => onPresetSelect?.(p.key)}
          >
            {Array.from({ length: p.tiles }).map((_, i) => (
              <i key={i} />
            ))}
          </button>
        ))}
      </div>

      <button type="button" class="gt-btn" onClick={onSplitRight} title="Split right">
        <SquareSplitHorizontal size={13} aria-hidden="true" /> split
      </button>
      <button type="button" class="gt-btn" onClick={onSplitDown} title="Split down">
        <SquareSplitVertical size={13} aria-hidden="true" /> split
      </button>

      <span class="gt-hint">
        ⏎ on a session opens it here · ⤢ maximizes into conversation view
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
