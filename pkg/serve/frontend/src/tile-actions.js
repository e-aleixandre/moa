// tile-actions.js — tile tree manipulation and visibility management

import { syncConnections } from './api.js';
import { store, setState, visibleSessionIds } from './store.js';
import {
  allTileIds, allSessionIds, findTile, tileCount,
  splitTileNode, removeTileNode, setTileSession, swapSessions,
  clearSession, setRatioAtPath, presetTree, splitRoot,
} from './tileTree.js';

export function applyPreset(presetId) {
  const state = store.get();
  const currentSessions = allSessionIds(state.tileTree);
  const tree = presetTree(presetId);
  const newTileIds = allTileIds(tree);
  let result = tree;
  for (let i = 0; i < Math.min(currentSessions.length, newTileIds.length); i++) {
    if (currentSessions[i]) {
      result = setTileSession(result, newTileIds[i], currentSessions[i]);
    }
  }
  const focused = newTileIds[0] || 1;
  setState({ tileTree: result, focusedTile: focused });
  autoFillTiles();
  afterVisibilityChange();
}

export function splitTile(tileId, direction) {
  const state = store.get();
  const tree = splitTileNode(state.tileTree, tileId, direction);
  setState({ tileTree: tree });
  const ids = allTileIds(tree);
  const oldIds = allTileIds(state.tileTree);
  const newId = ids.find(id => !oldIds.includes(id));
  if (newId) setState({ focusedTile: newId });
  autoFillTiles();
  afterVisibilityChange();
}

export function addPane(direction) {
  const state = store.get();
  const oldIds = allTileIds(state.tileTree);
  const tree = splitRoot(state.tileTree, direction);
  setState({ tileTree: tree });
  const ids = allTileIds(tree);
  const newId = ids.find(id => !oldIds.includes(id));
  if (newId) setState({ focusedTile: newId });
  autoFillTiles();
  afterVisibilityChange();
}

export function closeTile(tileId) {
  const state = store.get();
  if (tileCount(state.tileTree) <= 1) return;
  const tree = removeTileNode(state.tileTree, tileId);
  const ids = allTileIds(tree);
  const focused = ids.includes(state.focusedTile) ? state.focusedTile : ids[0];
  setState({ tileTree: tree, focusedTile: focused });
  afterVisibilityChange();
}

export function assignToTile(tileId, sessionId) {
  const state = store.get();
  const targetTile = findTile(state.tileTree, tileId);
  const targetSession = targetTile ? targetTile.sessionId : null;

  let sourceTileId = null;
  for (const tid of allTileIds(state.tileTree)) {
    const t = findTile(state.tileTree, tid);
    if (t && t.sessionId === sessionId) { sourceTileId = tid; break; }
  }

  let tree = state.tileTree;
  if (sourceTileId && targetSession && sourceTileId !== tileId) {
    tree = swapSessions(tree, sourceTileId, tileId);
  } else {
    tree = clearSession(tree, sessionId);
    tree = setTileSession(tree, tileId, sessionId);
  }
  setState({ tileTree: tree, focusedTile: tileId });
  afterVisibilityChange();
}

export function focusTile(tileId) {
  const state = store.get();
  const ids = allTileIds(state.tileTree);
  if (!ids.includes(tileId)) return;
  setState({ focusedTile: tileId });
  requestAnimationFrame(() => {
    const tile = document.querySelector(`[data-tile-id="${tileId}"]`);
    if (tile) {
      const ta = tile.querySelector('textarea');
      if (ta) ta.focus();
    }
  });
}

export function focusTileByIndex(idx) {
  const state = store.get();
  const ids = allTileIds(state.tileTree);
  if (idx >= 0 && idx < ids.length) focusTile(ids[idx]);
}

export function swapTiles(id1, id2) {
  const state = store.get();
  if (id1 === id2) return;
  const tree = swapSessions(state.tileTree, id1, id2);
  setState({ tileTree: tree, focusedTile: id2 });
  afterVisibilityChange();
}

export function resizeSplit(path, ratio) {
  const state = store.get();
  const tree = setRatioAtPath(state.tileTree, path, ratio);
  setState({ tileTree: tree });
}

export function setActiveSession(id) {
  setState({ activeSession: id });
  afterVisibilityChange();
}

export function toggleSound() {
  const state = store.get();
  setState({ soundEnabled: !state.soundEnabled });
}

export function setMobile(isMobile) { setState({ isMobile }); }

export function autoFillTiles() {
  const state = store.get();
  const assigned = new Set(allSessionIds(state.tileTree));
  const available = Object.values(state.sessions)
    .filter(s => s.state !== 'saved' && !assigned.has(s.id))
    .sort((a, b) => (b.updated || 0) - (a.updated || 0));

  if (available.length === 0) return;

  let tree = state.tileTree;
  let changed = false;
  for (const tileId of allTileIds(tree)) {
    if (available.length === 0) break;
    const tile = findTile(tree, tileId);
    if (tile && !tile.sessionId) {
      tree = setTileSession(tree, tileId, available.shift().id);
      changed = true;
    }
  }
  if (changed) {
    setState({ tileTree: tree });
    afterVisibilityChange();
  }
}

export function autoSelectMobile() {
  const state = store.get();
  if (state.activeSession && state.sessions[state.activeSession]) return;
  const active = Object.values(state.sessions)
    .filter(s => s.state !== 'saved')
    .sort((a, b) => (b.updated || 0) - (a.updated || 0));
  if (active.length > 0) {
    setState({ activeSession: active[0].id });
    afterVisibilityChange();
  }
}

// Lazy import to break the circular dependency: session-actions → tile-actions
// → session-actions (via resumeSession). We load it dynamically on first use.
let _resumeSession = null;
async function getResumeSession() {
  if (!_resumeSession) {
    const mod = await import('./session-actions.js');
    _resumeSession = mod.resumeSession;
  }
  return _resumeSession;
}

const resumingIds = new Set();

export function afterVisibilityChange() {
  const state = store.get();
  const visible = visibleSessionIds(state);

  for (const id of visible) {
    const sess = state.sessions[id];
    if (sess?.state === 'saved' && !resumingIds.has(id)) {
      resumingIds.add(id);
      getResumeSession().then(resume =>
        resume(id)
          .catch(e => console.error('Auto-resume failed for', id, e))
          .finally(() => resumingIds.delete(id))
      );
    }
  }

  const connectable = visible.filter(id => state.sessions[id]?.state !== 'saved');
  syncConnections(connectable);
}
