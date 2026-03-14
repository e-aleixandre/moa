// store.js — immutable snapshot store with pub/sub

import {
  initIds, allTileIds, allSessionIds, tileCount,
} from './tileTree.js';

const STORAGE_KEY = 'moa-ui-state';

function loadPersistedState() {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (raw) return JSON.parse(raw);
  } catch (_) { /* ignore */ }
  return {};
}

function persistState(s) {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify({
      tileTree: s.tileTree,
      focusedTile: s.focusedTile,
      soundEnabled: s.soundEnabled,
    }));
  } catch (_) { /* ignore */ }
}

const persisted = loadPersistedState();

// Migrate from old format or restore tree
import { createTile } from './tileTree.js';
let initialTree;
if (persisted.tileTree) {
  initialTree = persisted.tileTree;
  initIds(initialTree);
} else {
  initialTree = createTile();
}

const initialIds = allTileIds(initialTree);
const initialFocused = initialIds.includes(persisted.focusedTile)
  ? persisted.focusedTile
  : initialIds[0] || 1;

let state = {
  sessions: {},

  tileTree: initialTree,
  focusedTile: initialFocused,
  soundEnabled: persisted.soundEnabled || false,

  isMobile: false,

  activeSession: null,
};

let listeners = new Set();

export const store = {
  get() { return state; },
  subscribe(fn) {
    listeners.add(fn);
    return () => listeners.delete(fn);
  },
};

export function setState(patch) {
  const next = typeof patch === 'function' ? patch(state) : patch;
  state = { ...state, ...next };
  persistState(state);
  listeners.forEach(fn => fn(state));
}

export function updateSession(id, patch) {
  const sess = state.sessions[id];
  if (!sess) return;
  setState({
    sessions: { ...state.sessions, [id]: { ...sess, ...patch } },
  });
}

// --- Derived selectors ---

export function visibleSessionIds(s) {
  if (s.isMobile) {
    return s.activeSession ? [s.activeSession] : [];
  }
  return allSessionIds(s.tileTree);
}

export function isSessionInTile(s, sessionId) {
  return allSessionIds(s.tileTree).includes(sessionId);
}

export function sessionsByGroup(s) {
  const groups = {};
  for (const sess of Object.values(s.sessions)) {
    const key = sess.cwd || 'Unknown';
    if (!groups[key]) groups[key] = [];
    groups[key].push(sess);
  }
  for (const arr of Object.values(groups)) {
    arr.sort((a, b) => (b.updated || 0) - (a.updated || 0));
  }
  return groups;
}

export function attentionCount(s) {
  return Object.values(s.sessions).filter(
    sess => sess.state === 'permission' || sess.state === 'error'
  ).length;
}

export function getTileCount() {
  return tileCount(state.tileTree);
}
