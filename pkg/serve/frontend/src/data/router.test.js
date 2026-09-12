// router.test.js — run with `bun test`.
//
// bun's default test env has no DOM. These tests install a minimal fake
// window/history/location so the router can replace state deterministically.
// They verify the core promise of the module: navigate() flips the store's
// `view` WITHOUT a full-page reload (no location.href assignment), keeps the
// URL in sync so a RELOAD or a shared link lands on the same view, and — the
// inversion this module must honour — never adds a history entry, because a
// browser back/forward gesture has no meaning in the installed app.

import { test, expect, beforeEach, afterEach } from 'bun:test';
import { navigate, viewFromLocation } from './router.js';
import { store, setState } from './store.js';

let reloads;
let pushes;

function installFakeEnv(initialSearch = '') {
  reloads = 0;
  pushes = 0;
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
    // Counted, never expected: a pushState here would give the PWA a back
    // gesture that navigates inside the app.
    pushState(state, _title, url) {
      pushes++;
      this.lastMethod = 'push';
      this.state = state;
      loc.search = url && url.includes('?') ? url.slice(url.indexOf('?')) : '';
    },
    replaceState(state, _title, url) {
      this.lastMethod = 'replace';
      this.state = state;
      loc.search = url && url.includes('?') ? url.slice(url.indexOf('?')) : '';
    },
  };
  const win = {
    history,
    location: loc,
    addEventListener() {},
    removeEventListener() {},
  };
  globalThis.window = win;
  globalThis.history = history;
  globalThis.location = loc;
}

function uninstallFakeEnv() {
  delete globalThis.window;
  delete globalThis.history;
  delete globalThis.location;
}

beforeEach(() => {
  setState({ view: null });
});

afterEach(() => {
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

// The inversion that matters: no entry is created, in either direction.
test('navigate never pushes a history entry — it replaces', () => {
  installFakeEnv('');
  navigate('grid');
  expect(history.lastMethod).toBe('replace');
  navigate(null);
  expect(history.lastMethod).toBe('replace');
  expect(pushes).toBe(0);
});

test('the replaced entry still names the view, so a reload lands on it', () => {
  installFakeEnv('');
  navigate('grid');
  expect(history.state).toEqual({ moaView: 'grid' });
  // A fresh load reads the URL, not the state.
  expect(viewFromLocation()).toBe('grid');
});

test('navigate preserves unrelated query parameters', () => {
  installFakeEnv('?share=abc');
  navigate('grid');
  expect(location.search).toContain('share=abc');
  expect(location.search).toContain('view=grid');
  navigate(null);
  expect(location.search).toBe('?share=abc');
});
