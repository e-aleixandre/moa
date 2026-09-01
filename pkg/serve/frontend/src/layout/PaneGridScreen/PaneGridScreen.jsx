import { useEffect } from "preact/hooks";
import { GridToolbar } from "../GridToolbar/GridToolbar.jsx";
import { PaneGrid } from "../PaneGrid/PaneGrid.jsx";
import { store } from "../../data/store.js";
import { useStore } from "../../hooks/useStore.js";
import { allTileIds, findTile } from "../../data/tileTree.js";
import { applyPreset, addPane, focusTileByIndex, focusTile } from "../../data/tile-actions.js";
import { selectGridToolbar } from "./toolbar.js";
import "./PaneGridScreen.css";

// PaneGridScreen — the grid column. Spine lives in DesktopShell; this owns
// the toolbar, the tiles, and the pane-focus shortcuts.

export function PaneGridScreen() {
  const toolbar = useStore(selectGridToolbar);

  const onAttentionClick = () => {
    if (!toolbar.firstNeedsYouId) return;
    const tree = store.get().tileTree;
    for (const tileId of allTileIds(tree)) {
      const t = findTile(tree, tileId);
      if (t && t.sessionId === toolbar.firstNeedsYouId) { focusTile(tileId); return; }
    }
  };

  useEffect(() => {
    const onKey = (e) => {
      if (e.key < "1" || e.key > "9") return;
      if (!(e.metaKey || e.ctrlKey || e.altKey)) return;
      e.preventDefault();
      focusTileByIndex(parseInt(e.key, 10) - 1);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  return (
    <main class="pane-grid-main">
      <GridToolbar
        paneCount={toolbar.paneCount}
        activePreset={toolbar.activePreset}
        needsYouCount={toolbar.needsYouCount}
        onAttentionClick={onAttentionClick}
        onPresetSelect={(id) => applyPreset(id)}
        onSplitRight={() => addPane("horizontal")}
        onSplitDown={() => addPane("vertical")}
      />
      <PaneGrid />
    </main>
  );
}
