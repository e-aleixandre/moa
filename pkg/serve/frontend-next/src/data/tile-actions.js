// tile-actions.js — tile tree manipulation and visibility management

import { syncConnections } from './api.js';
import { store, setState, updateSession, visibleSessionIds } from './store.js';
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

// focusTile marks a tile as focused. By default it also moves the keyboard
// caret into that tile's input (used by keyboard navigation, e.g. Ctrl+1/2).
//
// For pointer-driven selection pass { respectSelection: true }: the input is
// still focused (so keystrokes go to the tile you clicked, not the previously
// focused one) UNLESS the click produced/left an active text selection, in
// which case focusing is skipped so we don't collapse the selection.
export function focusTile(tileId, { focusInput = true, respectSelection = false } = {}) {
  const state = store.get();
  const ids = allTileIds(state.tileTree);
  if (!ids.includes(tileId)) return;
  setState({ focusedTile: tileId });
  if (!focusInput) return;
  if (respectSelection) {
    const sel = typeof window !== 'undefined' && window.getSelection && window.getSelection();
    if (sel && !sel.isCollapsed && String(sel).length > 0) return;
  }
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

// openSession brings a session into view regardless of layout — used by push
// notification routing (?session= on cold start, postMessage on warm focus).
// Returns false if the session isn't loaded so the caller can fall back.
export function openSession(id) {
  const state = store.get();
  if (!state.sessions[id]) return false;
  if (state.isMobile) setActiveSession(id);
  else assignToTile(state.focusedTile, id);
  return true;
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

// releaseStaleSaved drops any session that is currently `saved` (closed) from
// the places that decide what the conversation surface shows: desktop tiles and
// the mobile activeSession. Called once at bootstrap, BEFORE autoFillTiles /
// autoSelectMobile / afterVisibilityChange run, so a persisted focusedTile that
// restored a now-saved session doesn't render it as if open — nor get silently
// auto-resumed (a server-side side effect we must never trigger on page load).
// The user picks what to resume from the empty state; we never do it for them.
export function releaseStaleSaved() {
  const state = store.get();
  const patch = {};

  let tree = state.tileTree;
  let changed = false;
  for (const sid of allSessionIds(tree)) {
    const sess = state.sessions[sid];
    if (sess && sess.state === 'saved') {
      tree = clearSession(tree, sid);
      changed = true;
    }
  }
  if (changed) patch.tileTree = tree;

  const active = state.activeSession;
  if (active && state.sessions[active]?.state === 'saved') {
    patch.activeSession = null;
  }

  if (Object.keys(patch).length > 0) setState(patch);
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

// afterVisibilityChange auto-resumes a saved session that becomes visible — but
// ONLY after the first (bootstrap) pass. On bootstrap a persisted focusedTile /
// activeSession may point at a session that is now saved (closed); resuming it
// then would be a page-load side effect (server process, cost) and would show a
// closed session as if open. So the first pass RELEASES stale saved sessions
// instead (EMPTY-STATE-SPEC §1.2) and the user resumes intentionally from the
// empty state. Every later pass (an intentional open/tap/DnD makes a saved
// session visible) resumes as before.
let booted = false;

// Test-only: reset the bootstrap guard so a test can exercise the first-pass
// (release-not-resume) branch deterministically regardless of order.
export function __resetBootForTests() { booted = false; }

export function afterVisibilityChange() {
  let state = store.get();
  let visible = visibleSessionIds(state);

  // Clear the unread badge for sessions the user can now see (only when the
  // tab is actually in the foreground — a background visibility shuffle
  // shouldn't mark things read).
  if (typeof document === 'undefined' || !document.hidden) {
    for (const id of visible) {
      const sess = state.sessions[id];
      if (sess && sess.unseen) updateSession(id, { unseen: false });
    }
  }

  if (!booted) {
    booted = true;
    releaseStaleSaved();
    state = store.get();
    visible = visibleSessionIds(state);
    syncConnections(visible.filter(id => state.sessions[id]?.state !== 'saved'));
    return;
  }

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
