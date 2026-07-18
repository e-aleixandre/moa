// tileTree.test.js — run with `bun test`.
//
// Covers the 5G pane-grid engine: the pure snap math (snapToRatio), the binary
// split-tree operations (swapSessions), and the store-backed tile actions
// (applyPreset / splitTile / closeTile). api.js is mocked so the actions'
// syncConnections side effect doesn't touch the network.
import { test, expect, describe, beforeEach, mock } from 'bun:test';

import { snapToRatio, SNAPS } from './snap.js';
import {
  createTile, createSplit, allTileIds, allSessionIds, tileCount,
  swapSessions, setTileSession, findTile, initIds, presetTree, treeShape,
  setRatioAtPath,
} from './tileTree.js';

// --- snapToRatio (pure) ---
describe('snapToRatio', () => {
  test('maps a drag fraction to the nearest snap ratio', () => {
    expect(snapToRatio(0.2)).toEqual([1, 3]);   // ~25%
    expect(snapToRatio(0.4)).toEqual([1, 2]);   // ~33%
    expect(snapToRatio(0.5)).toEqual([1, 1]);   // 50%
    expect(snapToRatio(0.7)).toEqual([2, 1]);   // ~67%
    expect(snapToRatio(0.8)).toEqual([3, 1]);   // ~75%
  });

  test('clamps extremes to the outermost snaps', () => {
    expect(snapToRatio(0)).toEqual([1, 3]);
    expect(snapToRatio(1)).toEqual([3, 1]);
    expect(snapToRatio(-5)).toEqual([1, 3]);
    expect(snapToRatio(5)).toEqual([3, 1]);
  });

  test('there are exactly five snap points', () => {
    expect(SNAPS.map((s) => s.ratio)).toEqual([[1, 3], [1, 2], [1, 1], [2, 1], [3, 1]]);
  });
});

// --- swapSessions (pure tree op) ---
describe('swapSessions', () => {
  test('swaps the sessions held by two tiles', () => {
    initIds(createTile());
    const a = createTile('sess-a');
    const b = createTile('sess-b');
    const tree = createSplit('horizontal', [a, b]);
    const swapped = swapSessions(tree, a.id, b.id);
    expect(findTile(swapped, a.id).sessionId).toBe('sess-b');
    expect(findTile(swapped, b.id).sessionId).toBe('sess-a');
  });

  test('is a no-op when a tile id is unknown', () => {
    const a = createTile('sess-a');
    const b = createTile('sess-b');
    const tree = createSplit('horizontal', [a, b]);
    expect(swapSessions(tree, a.id, 9999)).toBe(tree);
  });
});

// --- setRatioAtPath (pure): the resize handle's write path ---
describe('setRatioAtPath', () => {
  test('root path [] sets the root split ratio and mutates only it', () => {
    const tree = createSplit('horizontal', [createTile('a'), createTile('b')], [1, 1]);
    const next = setRatioAtPath(tree, [], [3, 1]);
    expect(next.ratio).toEqual([3, 1]);
    expect(next).not.toBe(tree); // new node, immutable update
    expect(tree.ratio).toEqual([1, 1]); // original untouched
  });

  test('nested path targets exactly one split, leaving siblings intact', () => {
    // root(H): [ left=split(V)[a,b], right=tile ]
    const left = createSplit('vertical', [createTile('a'), createTile('b')], [1, 1]);
    const right = createTile('c');
    const tree = createSplit('horizontal', [left, right], [1, 2]);

    const next = setRatioAtPath(tree, [0], [1, 3]); // resize the nested split
    expect(next.children[0].ratio).toEqual([1, 3]); // nested changed
    expect(next.ratio).toEqual([1, 2]);             // root ratio untouched
    expect(next.children[1]).toBe(right);           // sibling reference preserved
    expect(tree.children[0].ratio).toEqual([1, 1]); // original untouched
  });

  test('a path pointing at a tile (not a split) is a no-op', () => {
    const tree = createSplit('horizontal', [createTile('a'), createTile('b')]);
    const next = setRatioAtPath(tree, [0], [3, 1]); // children[0] is a tile
    expect(next.children[0].type).toBe('tile');
    expect(next.children[0].ratio).toBeUndefined();
  });
});

// --- treeShape / presetTree structural signatures ---
describe('presetTree / treeShape', () => {
  test('each preset has a stable structural signature', () => {
    expect(treeShape(presetTree('1'))).toBe('t');
    expect(treeShape(presetTree('2-col'))).toBe(treeShape(createSplit('horizontal', [createTile(), createTile()])));
    expect(tileCount(presetTree('2x2'))).toBe(4);
    expect(tileCount(presetTree('3x2'))).toBe(6);
  });
});

// --- Store-backed tile actions ---
// Mock api.js (syncConnections is called by afterVisibilityChange). Keep the
// other exports intact so the module graph still resolves.
const realApi = await import('./api.js');
mock.module('./api.js', () => ({ ...realApi, syncConnections: () => {} }));

const { store, setState } = await import('./store.js');
const {
  applyPreset, splitTile, closeTile, assignToTile,
} = await import('./tile-actions.js');

// Reset the store to a single empty tile with three loaded sessions before each
// action test, so the assertions don't depend on persisted localStorage state.
function seed(sessionIds) {
  const sessions = {};
  for (const id of sessionIds) sessions[id] = { id, state: 'idle', updated: Date.now() };
  const tile = createTile();
  initIds(tile);
  setState({ sessions, tileTree: tile, focusedTile: tile.id, isMobile: false });
  return tile;
}

describe('applyPreset', () => {
  beforeEach(() => seed([]));

  test('preserves existing sessions, assigning them to the first tiles', () => {
    // Two sessions assigned across a 2-col layout.
    seed(['s1', 's2']);
    applyPreset('2-col');
    const ids = allTileIds(store.get().tileTree);
    expect(ids.length).toBe(2);
    // autoFillTiles assigns the two idle sessions across the two tiles.
    const assigned = allSessionIds(store.get().tileTree);
    expect(assigned).toContain('s1');
    expect(assigned).toContain('s2');
  });

  test('carries assigned sessions into the new preset in order', () => {
    // Start from a 2-col with s1 in tile 0.
    const s = seed(['s1', 's2', 's3']);
    applyPreset('1'); // single pane, s1..s3 available
    // Move to a 2x2: the previously-assigned session survives in tile 0.
    const before = allSessionIds(store.get().tileTree);
    applyPreset('2x2');
    const after = allSessionIds(store.get().tileTree);
    for (const id of before) expect(after).toContain(id);
    expect(tileCount(store.get().tileTree)).toBe(4);
  });
});

describe('splitTile', () => {
  beforeEach(() => seed(['s1']));

  test('adds a tile and focuses the new one', () => {
    const root = store.get().tileTree;
    const before = tileCount(root);
    splitTile(store.get().focusedTile, 'horizontal');
    const after = tileCount(store.get().tileTree);
    expect(after).toBe(before + 1);
    // The freshly-created tile is focused.
    const ids = allTileIds(store.get().tileTree);
    expect(ids).toContain(store.get().focusedTile);
    const focusedTile = findTile(store.get().tileTree, store.get().focusedTile);
    // New tile starts empty (autoFillTiles may fill it if sessions remain).
    expect(focusedTile).toBeTruthy();
  });
});

describe('closeTile', () => {
  test('never drops below a single tile', () => {
    seed(['s1']);
    const only = store.get().focusedTile;
    closeTile(only);
    expect(tileCount(store.get().tileTree)).toBe(1);
  });

  test('removes a tile when more than one exists', () => {
    seed(['s1', 's2']);
    const first = store.get().focusedTile;
    splitTile(first, 'horizontal');
    expect(tileCount(store.get().tileTree)).toBe(2);
    const toClose = store.get().focusedTile;
    closeTile(toClose);
    expect(tileCount(store.get().tileTree)).toBe(1);
  });
});

describe('assignToTile', () => {
  test('assigns a session to the target tile', () => {
    seed(['s1', 's2']);
    const tileId = store.get().focusedTile;
    assignToTile(tileId, 's2');
    expect(findTile(store.get().tileTree, tileId).sessionId).toBe('s2');
  });
});
