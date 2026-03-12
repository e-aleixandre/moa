// tileTree.js — Binary split tree for free-form tile layout.
//
// A tree is either a leaf (tile) or a split with two children.
// Tiles hold a sessionId. Splits hold a direction and ratio.

let nextId = 1;

export function createTile(sessionId = null) {
  return { type: 'tile', id: nextId++, sessionId };
}

export function createSplit(direction, children, ratio = [1, 1]) {
  return { type: 'split', direction, children, ratio };
}

export function initIds(tree) {
  nextId = maxId(tree) + 1;
}

function maxId(node) {
  if (!node) return 0;
  if (node.type === 'tile') return node.id || 0;
  return Math.max(...node.children.map(maxId));
}

// All tile IDs in visual order (DFS, left-to-right / top-to-bottom)
export function allTileIds(tree) {
  if (!tree) return [];
  if (tree.type === 'tile') return [tree.id];
  return tree.children.flatMap(allTileIds);
}

// All assigned session IDs
export function allSessionIds(tree) {
  if (!tree) return [];
  if (tree.type === 'tile') return tree.sessionId ? [tree.sessionId] : [];
  return tree.children.flatMap(allSessionIds);
}

export function findTile(tree, tileId) {
  if (!tree) return null;
  if (tree.type === 'tile') return tree.id === tileId ? tree : null;
  for (const c of tree.children) {
    const f = findTile(c, tileId);
    if (f) return f;
  }
  return null;
}

export function tileCount(tree) {
  if (!tree) return 0;
  if (tree.type === 'tile') return 1;
  return tree.children.reduce((n, c) => n + tileCount(c), 0);
}

// Split a tile into two. The original keeps its session; the new tile is empty.
export function splitTileNode(tree, tileId, direction) {
  if (!tree) return tree;
  if (tree.type === 'tile') {
    if (tree.id === tileId) {
      return createSplit(direction, [tree, createTile()]);
    }
    return tree;
  }
  const newChildren = tree.children.map(c => splitTileNode(c, tileId, direction));
  if (newChildren.every((c, i) => c === tree.children[i])) return tree;
  return { ...tree, children: newChildren };
}

// Remove a tile. Its sibling takes the full space.
export function removeTileNode(tree, tileId) {
  if (!tree || tree.type === 'tile') return tree;
  const [a, b] = tree.children;
  if (a.type === 'tile' && a.id === tileId) return b;
  if (b.type === 'tile' && b.id === tileId) return a;
  const newA = removeTileNode(a, tileId);
  const newB = removeTileNode(b, tileId);
  if (newA !== a || newB !== b) return { ...tree, children: [newA, newB] };
  return tree;
}

export function setTileSession(tree, tileId, sessionId) {
  if (!tree) return tree;
  if (tree.type === 'tile') {
    return tree.id === tileId ? { ...tree, sessionId } : tree;
  }
  const nc = tree.children.map(c => setTileSession(c, tileId, sessionId));
  if (nc.every((c, i) => c === tree.children[i])) return tree;
  return { ...tree, children: nc };
}

export function swapSessions(tree, id1, id2) {
  const t1 = findTile(tree, id1);
  const t2 = findTile(tree, id2);
  if (!t1 || !t2) return tree;
  let r = setTileSession(tree, id1, t2.sessionId);
  return setTileSession(r, id2, t1.sessionId);
}

// Remove a session from all tiles (e.g. after deleting it)
export function clearSession(tree, sessionId) {
  if (!tree) return tree;
  if (tree.type === 'tile') {
    return tree.sessionId === sessionId ? { ...tree, sessionId: null } : tree;
  }
  const nc = tree.children.map(c => clearSession(c, sessionId));
  if (nc.every((c, i) => c === tree.children[i])) return tree;
  return { ...tree, children: nc };
}

// Set ratio on a split identified by path from root.
// path = [] → root, [0] → root.children[0], [1,0] → root.children[1].children[0]
export function setRatioAtPath(tree, path, ratio) {
  if (path.length === 0) {
    return tree.type === 'split' ? { ...tree, ratio } : tree;
  }
  if (tree.type !== 'split') return tree;
  const [head, ...rest] = path;
  const children = [...tree.children];
  children[head] = setRatioAtPath(children[head], rest, ratio);
  return { ...tree, children };
}

// Generate preset trees (quick-start layouts)
export function presetTree(id) {
  switch (id) {
    case '1':
      return createTile();
    case '2-col':
      return createSplit('horizontal', [createTile(), createTile()]);
    case '2-row':
      return createSplit('vertical', [createTile(), createTile()]);
    case '2+1':
      return createSplit('horizontal', [
        createSplit('vertical', [createTile(), createTile()]),
        createTile(),
      ]);
    case '1+2':
      return createSplit('horizontal', [
        createTile(),
        createSplit('vertical', [createTile(), createTile()]),
      ]);
    case '3-col':
      return createSplit('horizontal', [
        createTile(),
        createSplit('horizontal', [createTile(), createTile()]),
      ], [1, 2]);
    case '2x2':
      return createSplit('vertical', [
        createSplit('horizontal', [createTile(), createTile()]),
        createSplit('horizontal', [createTile(), createTile()]),
      ]);
    case '3x2':
      return createSplit('vertical', [
        createSplit('horizontal', [
          createTile(),
          createSplit('horizontal', [createTile(), createTile()]),
        ], [1, 2]),
        createSplit('horizontal', [
          createTile(),
          createSplit('horizontal', [createTile(), createTile()]),
        ], [1, 2]),
      ]);
    default:
      return createTile();
  }
}
