import { useCallback, useMemo } from 'preact/hooks';
import { resizeSplit } from '../state.js';
import { allTileIds } from '../tileTree.js';
import { Tile } from './Tile.jsx';

// Snap points for resize handle: percentage → fr ratio
const SNAPS = [
  { pct: 0.25, ratio: [1, 3] },
  { pct: 0.333, ratio: [1, 2] },
  { pct: 0.5, ratio: [1, 1] },
  { pct: 0.667, ratio: [2, 1] },
  { pct: 0.75, ratio: [3, 1] },
];

function snapToRatio(pct) {
  let best = SNAPS[2];
  let minDist = Infinity;
  for (const sp of SNAPS) {
    const d = Math.abs(pct - sp.pct);
    if (d < minDist) { minDist = d; best = sp; }
  }
  return best.ratio;
}

function ResizeHandle({ path, direction }) {
  const isH = direction === 'horizontal';

  const onPointerDown = useCallback((e) => {
    e.preventDefault();
    const handle = e.currentTarget;
    const parent = handle.parentElement;
    const rect = parent.getBoundingClientRect();

    handle.setPointerCapture(e.pointerId);
    handle.classList.add('active');
    document.body.style.cursor = isH ? 'col-resize' : 'row-resize';

    const onPointerMove = (e) => {
      const pos = isH ? e.clientX - rect.left : e.clientY - rect.top;
      const total = isH ? rect.width : rect.height;
      const pct = Math.max(0.15, Math.min(0.85, pos / total));
      resizeSplit([...path], snapToRatio(pct));
    };

    const onPointerUp = () => {
      handle.classList.remove('active');
      document.body.style.cursor = '';
      handle.removeEventListener('pointermove', onPointerMove);
      handle.removeEventListener('pointerup', onPointerUp);
    };

    handle.addEventListener('pointermove', onPointerMove);
    handle.addEventListener('pointerup', onPointerUp);
  }, [path, isH]);

  return (
    <div
      class={`resize-handle ${isH ? 'resize-h' : 'resize-v'}`}
      onPointerDown={onPointerDown}
    />
  );
}

function TileNode({ node, state, path, tileIndexMap }) {
  if (node.type === 'tile') {
    const session = node.sessionId ? state.sessions[node.sessionId] : null;
    return (
      <Tile
        tileId={node.id}
        tileIndex={tileIndexMap.get(node.id) ?? 0}
        sessionId={node.sessionId}
        session={session}
        isFocused={state.focusedTile === node.id}
      />
    );
  }

  // Split node
  const isH = node.direction === 'horizontal';
  const [a, b] = node.children;
  const [ra, rb] = node.ratio;

  return (
    <div class={`split ${isH ? 'split-h' : 'split-v'}`}>
      <div class="split-pane" style={{ flex: ra }}>
        <TileNode node={a} state={state} path={[...path, 0]} tileIndexMap={tileIndexMap} />
      </div>
      <ResizeHandle path={path} direction={node.direction} />
      <div class="split-pane" style={{ flex: rb }}>
        <TileNode node={b} state={state} path={[...path, 1]} tileIndexMap={tileIndexMap} />
      </div>
    </div>
  );
}

export function TileTree({ state }) {
  // Build tileId → DFS index map for numbering
  const tileIndexMap = useMemo(() => {
    const ids = allTileIds(state.tileTree);
    const m = new Map();
    ids.forEach((id, i) => m.set(id, i));
    return m;
  }, [state.tileTree]);

  return (
    <div class="tile-tree">
      <TileNode node={state.tileTree} state={state} path={[]} tileIndexMap={tileIndexMap} />
    </div>
  );
}
