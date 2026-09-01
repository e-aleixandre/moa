import { useState, useEffect } from "preact/hooks";
import { GridToolbar } from "../GridToolbar/GridToolbar.jsx";
import { PaneGrid } from "../PaneGrid/PaneGrid.jsx";
import { store } from "../../data/store.js";
import { allTileIds, allSessionIds, findTile, tileCount, treeShape, presetTree } from "../../data/tileTree.js";
import { PRESETS } from "../../data/layoutPresets.js";
import { applyPreset, addPane, focusTileByIndex, focusTile } from "../../data/tile-actions.js";
import { hasBlockingOverlay } from "../../data/overlays.js";
import { isPaneFocusShortcut } from "../../data/app-layout.js";
import "./PaneGridScreen.css";

// PaneGridScreen — the grid column. Spine lives in DesktopShell; this owns
// the toolbar, the tiles, and the pane-focus shortcuts.

function matchPreset(tree) {
  const shape = treeShape(tree);
  for (const p of PRESETS) {
    if (treeShape(presetTree(p.id)) === shape) return p.id;
  }
  return null;
}

export function PaneGridScreen() {
  const [state, setState] = useState(store.get());
  useEffect(() => store.subscribe(setState), []);

  const paneCount = tileCount(state.tileTree);
  const activePreset = matchPreset(state.tileTree);

  const assigned = new Set(allSessionIds(state.tileTree));
  const needsYou = Object.values(state.sessions).filter(
    (s) => assigned.has(s.id) && (s.state === "permission" || s.state === "error")
  );

  const onAttentionClick = () => {
    if (needsYou.length === 0) return;
    const target = needsYou[0];
    for (const tileId of allTileIds(state.tileTree)) {
      const t = findTile(state.tileTree, tileId);
      if (t && t.sessionId === target.id) { focusTile(tileId); return; }
    }
  };

  useEffect(() => {
    const onKey = (e) => {
      if (!isPaneFocusShortcut(e, store.get(), hasBlockingOverlay())) return;
      e.preventDefault();
      focusTileByIndex(parseInt(e.key, 10) - 1);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  return (
    <main class="pane-grid-main">
      <GridToolbar
        paneCount={paneCount}
        activePreset={activePreset}
        needsYouCount={needsYou.length}
        onAttentionClick={onAttentionClick}
        onPresetSelect={(id) => applyPreset(id)}
        onSplitRight={() => addPane("horizontal")}
        onSplitDown={() => addPane("vertical")}
      />
      <PaneGrid state={state} />
    </main>
  );
}
