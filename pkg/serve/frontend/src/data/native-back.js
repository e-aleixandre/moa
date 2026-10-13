// native-back.js — what the Android Back button does inside moa.
//
// The native shell calls window.MoaHandleBack() on every back press and needs a
// synchronous answer: true = the page handled it, anything else = the shell
// minimizes the app. The order is the owner's rule: close the top overlay; else
// step back inside the app; else report nothing to do (the app is at its root).
//
// Nothing here is new machinery. Overlays already close on Escape (sheets via
// sheetEscape, which also walks a pushed page back first), so a synthetic Escape
// reuses that path. The history API is deliberately untouched (see
// no-history-navigation.test.js).

import { store, updateSession } from './store.js';
import { SHELL_NATIVE, detectShell } from './shell.js';
import { hasOpenLayer } from './overlay-layers.js';
import { hasBlockingOverlay } from './overlays.js';
import { closePalette } from './palette.js';
import { dismissShare } from './share.js';
import { closeDrawer } from './drawer.js';
import { closeInbox } from './events.js';
import { closeTasksView } from './tasks-view.js';
import { focusedSessionId } from './selectors.js';

function dispatchEscape() {
  const event = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true });
  document.dispatchEvent(event);
  return event;
}

// handleBack — true when something was closed or stepped back.
export function handleBack({
  getState = store.get,
  escape = dispatchEscape,
  overlayOpen = () => hasOpenLayer() || hasBlockingOverlay(),
  actions = {},
} = {}) {
  try {
    const act = { closePalette, dismissShare, closeDrawer, closeInbox, closeTasksView, updateSession, ...actions };
    const s = getState();

    // (a) transient overlays, topmost first
    if (s.share?.status === 'ready') { act.dismissShare(); return true; }
    if (s.paletteOpen) { act.closePalette(); return true; }
    if (overlayOpen()) { escape(); return true; }
    if (s.drawerOpen) { act.closeDrawer(); return true; }

    // (b) in-app back: pushed full-screen views
    if (s.inboxOpen) { act.closeInbox(); return true; }
    if (s.view === 'tasks') {
      // The mobile tasks view keeps its own page stack and answers Escape by
      // popping it (and cancelling the event); only a bare list leaves.
      if (!escape()?.defaultPrevented) act.closeTasksView();
      return true;
    }
    const id = focusedSessionId(s);
    const session = id ? s.sessions?.[id] : null;
    if (session?.viewingSubagent) { act.updateSession(id, { viewingSubagent: null }); return true; }
    if (session?.viewingBashJob) { act.updateSession(id, { viewingBashJob: null }); return true; }

    // (c) root
    return false;
  } catch (_) {
    return false;
  }
}

// installNativeBack exposes handleBack as window.MoaHandleBack, in the native
// shell only: a browser or PWA has no Back button that needs this.
export function installNativeBack(target = globalThis, shell = detectShell(target)) {
  if (!target || shell !== SHELL_NATIVE) return () => {};
  target.MoaHandleBack = () => handleBack();
  const installed = target.MoaHandleBack;
  return () => {
    if (target.MoaHandleBack === installed) delete target.MoaHandleBack;
  };
}
