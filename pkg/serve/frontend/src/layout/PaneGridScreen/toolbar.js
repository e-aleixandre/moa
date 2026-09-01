import { allSessionIds, tileCount, treeShape, presetTree } from "../../data/tileTree.js";
import { PRESETS } from "../../data/layoutPresets.js";

function matchPreset(tree) {
  const shape = treeShape(tree);
  for (const p of PRESETS) {
    if (treeShape(presetTree(p.id)) === shape) return p.id;
  }
  return null;
}

// Roster the grid toolbar actually paints. A streaming token on a pane is not
// a toolbar change, so the previous object is returned (Object.is) and the
// screen around the panes does not re-render.
export function selectGridToolbar(state) {
  const assigned = new Set(allSessionIds(state.tileTree));
  let needsYouCount = 0;
  let firstNeedsYouId = null;
  for (const s of Object.values(state.sessions || {})) {
    if (assigned.has(s.id) && (s.state === "permission" || s.state === "error")) {
      if (!firstNeedsYouId) firstNeedsYouId = s.id;
      needsYouCount++;
    }
  }
  const next = {
    paneCount: tileCount(state.tileTree),
    activePreset: matchPreset(state.tileTree),
    needsYouCount,
    firstNeedsYouId,
  };
  const prev = selectGridToolbar._prev;
  if (
    prev
    && prev.paneCount === next.paneCount
    && prev.activePreset === next.activePreset
    && prev.needsYouCount === next.needsYouCount
    && prev.firstNeedsYouId === next.firstNeedsYouId
  ) return prev;
  selectGridToolbar._prev = next;
  return next;
}

export function __resetGridToolbarForTests() {
  selectGridToolbar._prev = null;
}
