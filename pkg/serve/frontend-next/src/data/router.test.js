// router.test.js — run with `bun test`.
//
// bun's default test env has no DOM. These tests install a minimal fake
// window/history/location so the router can push/replace state and fire
// popstate deterministically. They verify the core promise of the module:
// navigate() flips the store's `view` WITHOUT a full-page reload (no
// location.href assignment), keeps the URL in sync via pushState, and a Back
// gesture (popstate) re-derives the view from the URL.

import { test, expect, beforeEach, afterEach } from 'bun:test';
import { navigate, viewFromLocation, bindRouter, __resetRouterForTests } from './router.js';
import { openOverlay, __resetOverlayHistoryForTests } from './overlay-history.js';
import { store, setState } from './store.js';

let popListeners;
let reloads;

function installFakeEnv(initialSearch = '') {
  popListeners = new Set();
  reloads = 0;
  const loc = {
    pathname: '/next/',
    search: initialSearch,
    // A real navigation would set href; we count assignments so a test can
    // assert the router NEVER reloads.
    set href(_v) { reloads++; },
    get href() { return '/next/' + loc.search; },
  };
  const history = {
    state: null,
    lastMethod: null,
    pushState(state, _title, url) {
      this.lastMethod = 'push';
      this.state = state;
      loc.search = url && url.startsWith('?') ? url : '';
    },
    replaceState(state, _title, url) {
      this.lastMethod = 'replace';
      this.state = state;
      loc.search = url && url.startsWith('?') ? url : '';
    },
  };
  const win = {
    history,
    location: loc,
    addEventListener(type, fn) { if (type === 'popstate') popListeners.add(fn); },
    removeEventListener(type, fn) { if (type === 'popstate') popListeners.delete(fn); },
  };
  globalThis.window = win;
  globalThis.history = history;
  globalThis.location = loc;
}

function firePopstate() {
  popListeners.forEach((fn) => fn({ type: 'popstate' }));
}

function uninstallFakeEnv() {
  delete globalThis.window;
  delete globalThis.history;
  delete globalThis.location;
}

beforeEach(() => {
  __resetRouterForTests();
  __resetOverlayHistoryForTests();
  setState({ view: null });
});

afterEach(() => {
  __resetRouterForTests();
  __resetOverlayHistoryForTests();
  uninstallFakeEnv();
});

test('viewFromLocation reads ?view=', () => {
  installFakeEnv('?view=grid');
  expect(viewFromLocation()).toBe('grid');
});

test('viewFromLocation is null on the bare conversation URL', () => {
  installFakeEnv('');
  expect(viewFromLocation()).toBe(null);
});

test('navigate to grid flips the store view without reloading', () => {
  installFakeEnv('');
  navigate('grid');
  expect(store.get().view).toBe('grid');
  expect(location.search).toBe('?view=grid');
  expect(reloads).toBe(0);
});

test('navigate to null returns to the conversation view (no ?view=)', () => {
  installFakeEnv('?view=grid');
  setState({ view: 'grid' });
  navigate(null);
  expect(store.get().view).toBe(null);
  expect(location.search).toBe('');
  expect(reloads).toBe(0);
});

test('navigate pushes a history entry tagged with the view', () => {
  installFakeEnv('');
  navigate('grid');
  expect(history.state).toEqual({ moaView: 'grid' });
});

test('a Back gesture re-derives the view from the URL', () => {
  installFakeEnv('');
  bindRouter();
  navigate('grid');
  expect(store.get().view).toBe('grid');
  // Simulate the browser popping back to the pre-navigate URL.
  location.search = '';
  firePopstate();
  expect(store.get().view).toBe(null);
});

test('a popstate with the URL unchanged is a no-op (overlay guard pop)', () => {
  installFakeEnv('?view=grid');
  setState({ view: 'grid' });
  bindRouter();
  let writes = 0;
  const unsub = store.subscribe(() => { writes++; });
  // URL still says grid (an overlay-history guard pop leaves it untouched).
  firePopstate();
  unsub();
  expect(store.get().view).toBe('grid');
  expect(writes).toBe(0);
});

test('navigating with an overlay open closes it and overwrites its guard (replaceState)', () => {
  installFakeEnv('');
  // An open Sheet pushes a single history guard on top of the conversation URL.
  let closedWith = null;
  openOverlay('sheet-a', (fromPop) => { closedWith = fromPop; });
  navigate('grid');
  // The overlay was closed as a history-driven close (fromPop=true → it must
  // not fire its own back()).
  expect(closedWith).toBe(true);
  // The router overwrote the guard entry instead of pushing a new one, so
  // history stays [conversation, grid] with no stranded guard.
  expect(history.lastMethod).toBe('replace');
  expect(store.get().view).toBe('grid');
  expect(reloads).toBe(0);
});

test('navigating with no overlay open pushes a new entry', () => {
  installFakeEnv('');
  navigate('grid');
  expect(history.lastMethod).toBe('push');
});
