import { afterEach, beforeEach, expect, test } from 'bun:test';
import { disablePush, enablePush, getPushState, refreshPushState, watchNativeVisibleSession } from './push-client.js';

import { setState, store, updateSession } from './store.js';
import { registerOverlay } from './overlays.js';
import { openSessionPanel, closeSessionPanel } from './session-panel.js';
import { beginHistoryHydration, finishHistoryHydration } from './history-hydration.js';
import { setActiveSession } from './tile-actions.js';

let before;
beforeEach(() => { before = store.get(); });
afterEach(() => { delete globalThis.window; closeSessionPanel(); setState(before); });

function install(native) { globalThis.window = { MoaNativePush: native }; }

test('native bridge replaces Web Push for enable, disable and state', async () => {
  const calls = [];
  install({
    status: async () => ({ enabled: false, permission: 'not_determined' }),
    enable: async () => { calls.push('enable'); return { enabled: true, permission: 'granted' }; },
    disable: async () => { calls.push('disable'); return { enabled: false, permission: 'granted' }; },
  });
  await refreshPushState();
  expect(getPushState()).toBe('default');
  await enablePush();
  expect(getPushState()).toBe('subscribed');
  await disablePush();
  expect(getPushState()).toBe('default');
  expect(calls).toEqual(['enable', 'disable']);
});

test('a denied permission is reported as denied', async () => {
  install({ enable: async () => ({}), status: async () => ({ enabled: false, permission: 'denied' }) });
  await refreshPushState();
  expect(getPushState()).toBe('denied');
});

const session = (id, hydrated = true) => ({ id, state: 'idle', messages: [], subagents: {}, bashJobs: {}, historyHydrated: hydrated });

// A phone showing conversation `a`, with `b` available to switch to.
function watch(extra = {}) {
  const sent = [];
  install({ enable: async () => ({}), setVisibleSession: async (id) => { sent.push(id); } });
  setState({
    isMobile: true, view: null, inboxOpen: false, drawerOpen: false, sessionsLoaded: true, activeSession: 'a',
    sessions: { a: session('a'), b: session('b') }, ...extra,
  });
  const doc = { visibilityState: 'visible', addEventListener() {}, removeEventListener() {} };
  const stop = watchNativeVisibleSession({ doc });
  return { sent, stop, doc };
}

test('only a loaded, visible conversation is reported; hidden pages report null', () => {
  const { sent, stop } = watch();
  expect(sent).toEqual(['a']);
  setState({ inboxOpen: true });
  setState({ inboxOpen: false });
  expect(sent).toEqual(['a', null, 'a']);
  stop();
});

test('a conversation whose history has not landed is not reported', () => {
  const { sent, stop } = watch({ sessionsLoaded: false, sessions: { a: session('a', false), b: session('b') } });
  setState({ sessionsLoaded: true });
  expect(sent).toEqual([null]);
  updateSession('a', { historyHydrated: true });
  expect(sent).toEqual([null, 'a']);
  stop();
});

test('switching to an unhydrated session releases the suppression', () => {
  const { sent, stop } = watch({ sessions: { a: session('a'), b: session('b', false) } });
  setActiveSession('b');
  expect(sent).toEqual(['a', null]);
  stop();
});

test('a failed or stale re-init releases the suppression', () => {
  const { sent, stop } = watch();
  beginHistoryHydration('a');
  finishHistoryHydration('a', { stale: true, shown: false });
  expect(sent).toEqual(['a', null]);
  stop();
});

test('the session panel, the preview and registered overlays cover the conversation', () => {
  const { sent, stop } = watch();
  openSessionPanel('a', 'usage');
  closeSessionPanel();
  updateSession('a', { previewOpen: true });
  updateSession('a', { previewOpen: false });
  const release = registerOverlay('global-settings');
  release();
  expect(sent).toEqual(['a', null, 'a', null, 'a', null, 'a']);
  stop();
});

test('the drawer covers the conversation', () => {
  const { sent, stop } = watch();
  setState({ drawerOpen: true });
  setState({ drawerOpen: false });
  expect(sent).toEqual(['a', null, 'a']);
  stop();
});
