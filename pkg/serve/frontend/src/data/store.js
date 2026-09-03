// store.js — immutable snapshot store with pub/sub

import {
  initIds, allTileIds, allSessionIds, tileCount,
} from './tileTree.js';
import { pruneDrawerCollapsed } from './util/project-sessions.js';

const STORAGE_KEY = 'moa-next-ui-state';

function loadPersistedState() {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (raw) {
      const value = JSON.parse(raw);
      if (!value || typeof value !== 'object' || Array.isArray(value)) return {};
      return {
        tileTree: value.tileTree && typeof value.tileTree === 'object' ? value.tileTree : undefined,
        focusedTile: typeof value.focusedTile === 'number' ? value.focusedTile : undefined,
        soundEnabled: typeof value.soundEnabled === 'boolean' ? value.soundEnabled : undefined,
        groupByProject: typeof value.groupByProject === 'boolean' ? value.groupByProject : undefined,
        drawerCollapsed: value.drawerCollapsed && typeof value.drawerCollapsed === 'object' && !Array.isArray(value.drawerCollapsed)
          ? Object.fromEntries(Object.entries(value.drawerCollapsed).filter(([, collapsed]) => typeof collapsed === 'boolean'))
          : undefined,
      };
    }
  } catch (_) { /* ignore */ }
  return {};
}

function persistState(s) {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify({
      tileTree: s.tileTree,
      focusedTile: s.focusedTile,
      soundEnabled: s.soundEnabled,
      groupByProject: s.groupByProject,
      drawerCollapsed: s.drawerCollapsed,
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

  // sessionsLoaded flips true after the first loadSessions() resolves, so the
  // conversation screen can distinguish LOADING (no fetch yet) from EMPTY (fetch
  // returned no visible session). Set by the App bootstrap.
  sessionsLoaded: false,

  usage: null, // global plan usage snapshot from /api/usage (null until first poll)

  tileTree: initialTree,
  focusedTile: initialFocused,
  soundEnabled: persisted.soundEnabled || false,

  // Overview: group the session list by working directory when true. Named
  // groupByProject before the UI settled on "folder"; the key is persisted, so
  // renaming it would silently drop the preference of anyone who set it.
  groupByProject: persisted.groupByProject || false,
  drawerCollapsed: persisted.drawerCollapsed || {},

  isMobile: false,

  // view — which screen is showing: null = single conversation (or mobile),
  // 'grid' = pane grid. Seeded from the URL.
  view: (() => {
    try {
      if (typeof location === 'undefined') return null;
      return new URLSearchParams(location.search).get('view') || null;
    } catch (_) { return null; }
  })(),

  activeSession: null,

  // wake-on-event: events from GET /api/events — pending AND settled, since
  // the inbox keeps its history. inboxOpen is the inbox surface's own state:
  // the mobile push and the desktop spine swap read the same flag, so a toast
  // can open the inbox without knowing which layout it is talking to.
  events: [],
  inboxOpen: false,

  // Command palette (⌘K). Lives in the store so the global mount in
  // app.jsx and the per-screen Spine buttons (onSearch/onNewSession) read and
  // write the same state rather than standing up a second pub/sub system.
  // paletteOpen toggles the overlay; paletteStep picks the initial step
  // ('search' | 'create') when it opens.
  paletteOpen: false,
  paletteStep: 'search',

  // Mobile session drawer. In the store for the same reason the palette is:
  // the empty state and the command palette both need to send the user into
  // it (see data/drawer.js). drawerStep is 'list' | 'new'.
  drawerOpen: false,
  drawerStep: 'list',
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
  if (!next || typeof next !== 'object') return;
  let changed = false;
  for (const key of Object.keys(next)) {
    if (!Object.is(state[key], next[key])) {
      changed = true;
      break;
    }
  }
  if (!changed) return;
  const previous = state;
  state = { ...state, ...next };
  if (state.sessions !== previous.sessions) {
    const drawerCollapsed = pruneDrawerCollapsed(state.drawerCollapsed, Object.values(state.sessions));
    if (Object.keys(drawerCollapsed).length !== Object.keys(state.drawerCollapsed).length) state = { ...state, drawerCollapsed };
  }
  if (
    state.tileTree !== previous.tileTree ||
    state.focusedTile !== previous.focusedTile ||
    state.soundEnabled !== previous.soundEnabled ||
    state.groupByProject !== previous.groupByProject ||
    state.drawerCollapsed !== previous.drawerCollapsed
  ) {
    persistState(state);
  }
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
