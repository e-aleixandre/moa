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
  // 'grid' = pane grid, or a gallery key ('catalog'|'live'|'subagent'|'mobile').
  // Seeded from the URL so a deep-link / reload lands on the right screen; the
  // router (data/router.js) flips it in place (pushState, no reload) for the
  // conversation ⇄ grid hop.
  view: (() => {
    try {
      if (typeof location === 'undefined') return null;
      return new URLSearchParams(location.search).get('view') || null;
    } catch (_) { return null; }
  })(),

  activeSession: null,

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

  // Generic and mobile modal sheets keep this balanced while their scrim is
  // visually present. The palette and drawer have explicit state above.
  conversationObscuringOverlayCount: 0,
};

let listeners = new Set();
let conversationVisibilityObserver = null;

export const store = {
  get() { return state; },
  subscribe(fn) {
    listeners.add(fn);
    return () => listeners.delete(fn);
  },
};

export function setState(patch) {
  const next = typeof patch === 'function' ? patch(state) : patch;
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
  // A selection can become genuinely presented only after a leave animation
  // releases its drawer/sheet registration. Keep this at the store boundary
  // so every surface which restores the parent conversation feeds the same
  // attention observer (rather than relying on each close button to remember
  // to do so).
  if (conversationVisibilityObserver && (
      conversationVisibilityKey(previous) !== conversationVisibilityKey(state) ||
      parentVisibilityChanged(previous, state))) {
    conversationVisibilityObserver();
  }
}

function parentVisibilityChanged(previous, next) {
  const ids = new Set([...Object.keys(previous.sessions), ...Object.keys(next.sessions)]);
  for (const id of ids) {
    // Initial roster insertion is not a presentation transition. The
    // authoritative init supplies its own proof after that row commits; this
    // bridge is specifically for an already-known surface becoming visible
    // after selection, an overlay leave animation, or a detail exit.
    if (previous.sessions[id] &&
        !isParentConversationVisible(previous, id) && isParentConversationVisible(next, id)) return true;
  }
  return false;
}

// tile-actions owns the connection and acknowledgement side effects. The
// store owns all visibility transitions, including animation-delayed overlay
// releases, so it exposes this single bridge rather than importing that layer.
export function observeConversationVisibility(fn) {
  conversationVisibilityObserver = fn;
  return () => {
    if (conversationVisibilityObserver === fn) conversationVisibilityObserver = null;
  };
}

export function updateSession(id, patch) {
  const sess = state.sessions[id];
  if (!sess) return;
  setState({
    sessions: { ...state.sessions, [id]: { ...sess, ...patch } },
  });
}

// A closing mobile sheet remains registered through its leave animation, so a
// fading scrim cannot race an acknowledgement.
export function registerConversationObscuringOverlay() {
  let released = false;
  setState((current) => ({
    conversationObscuringOverlayCount: current.conversationObscuringOverlayCount + 1,
  }));
  return () => {
    if (released) return;
    released = true;
    setState((current) => ({
      conversationObscuringOverlayCount: Math.max(0, current.conversationObscuringOverlayCount - 1),
    }));
  };
}

// Only surfaces that intercept the whole conversation belong here: the global
// palette, mobile session drawer, and modal sheets. Header/status popovers and
// menus leave a prompt visible and intentionally do not suppress a receipt.
export function conversationVisibilityKey(s) {
  return `${s.paletteOpen ? 1 : 0}:${s.isMobile && s.drawerOpen ? 1 : 0}:${s.conversationObscuringOverlayCount || 0}`;
}

// --- Derived selectors ---

export function visibleSessionIds(s) {
  if (s.isMobile) {
    return s.activeSession ? [s.activeSession] : [];
  }
  return allSessionIds(s.tileTree);
}

// A parent session can retain its tile/mobile selection while one of its
// detail views replaces the conversation. Attention addressed to the parent
// is only presented when the parent conversation surface itself is showing.
export function isParentConversationVisible(s, sessionId) {
  const session = s.sessions[sessionId];
  return !!session && visibleSessionIds(s).includes(sessionId) &&
    !session.viewingSubagent && !session.viewingBashJob &&
    conversationVisibilityKey(s) === '0:0:0';
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
