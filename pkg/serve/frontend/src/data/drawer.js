// drawer.js — mobile SessionDrawer open/step controller.
//
// The drawer is the ONE place a phone manages sessions: pick one, create one,
// close/reopen/delete. Its open state lives in the STORE (drawerOpen /
// drawerStep) rather than inside MobileConversationScreen, because other
// surfaces have to be able to send you there — the empty state's buttons and
// the command palette's "New session…" both open the drawer instead of
// standing up a second create flow (the palette's own create step stays for
// desktop, where ⌘K is its home).
//
// Same shape as palette.js on purpose: thin helpers over setState so callers
// never poke the raw field names.

import { setState } from './store.js';
import { SIDEBAR_MODES } from './util/project-sessions.js';

// openDrawer opens the drawer on a given screen: 'list' (the sessions) or
// 'new' (choose a project and create).
export function openDrawer(step = 'list') {
  setState({ drawerOpen: true, drawerStep: step });
}

export function closeDrawer() {
  setState({ drawerOpen: false });
}

// setDrawerStep switches screen while the drawer stays open (the + button and
// the back arrow inside it).
export function setDrawerStep(step) {
  setState({ drawerStep: step });
}

// setGroupByProject remembers whether the drawer lists sessions by recency or
// grouped by working directory. Persisted by the store.
//
// The name is historical and stays because the key is persisted. The UI says
// "by folder", which is what this actually does: it groups by cwd, including
// directories that are no repository at all. Memory has its own, wider notion
// of a project — one repository, all of its worktrees — and calling both
// "project" would promise that sessions in two worktrees group together when
// they deliberately do not.
export function setGroupByProject(on) {
  setSidebarMode(on ? 'project' : 'recent');
}

// setSidebarMode picks which of the three lists the column shows: the sessions
// by recency, the sessions by folder, or the project owners. Persisted by the
// store — a way of LOOKING at your work is chosen and kept.
//
// groupByProject is written alongside it and kept: the grouping code reads it,
// and the key is persisted, so dropping it would silently reset the preference
// of anyone who set it before Owners existed.
export function setSidebarMode(mode) {
  const next = SIDEBAR_MODES.includes(mode) ? mode : 'recent';
  setState({ sidebarMode: next, groupByProject: next === 'project' });
}

export function setDrawerProjectCollapsed(key, collapsed) {
  setState((state) => ({
    drawerCollapsed: { ...state.drawerCollapsed, [key]: !!collapsed },
  }));
}
