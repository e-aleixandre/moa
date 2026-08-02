// stale-build.test.js — run with `bun test`.

import { test, expect, afterEach } from 'bun:test';
import { isStale, reloadPlan } from './stale-build.js';

test('isStale needs both ids: an unknown one never means stale', () => {
  expect(isStale('aaa', 'bbb')).toBe(true);
  expect(isStale('aaa', 'aaa')).toBe(false);
  expect(isStale('aaa', '')).toBe(false);
  expect(isStale('', 'bbb')).toBe(false);
  expect(isStale('aaa', undefined)).toBe(false);
});

test('a superseded bundle reloads once', () => {
  expect(reloadPlan({ current: 'aaa', served: 'bbb' })).toBe('reload');
});

test('a failed transition remains guarded in storage', () => {
  expect(reloadPlan({ current: 'aaa', served: 'bbb', attempted: 'aaa->bbb' })).toBe('give-up');
});

test('the URL guards a failed transition when storage is unavailable', () => {
  expect(reloadPlan({ current: 'aaa', served: 'bbb', urlAttempt: 'aaa->bbb' })).toBe('give-up');
});

test('a reload that landed on the new bundle clears the markers', () => {
  expect(reloadPlan({ current: 'bbb', served: 'bbb', attempted: 'aaa->bbb', urlAttempt: 'aaa->bbb' })).toBe('clear');
});

test('a further deploy after a failed attempt is a fresh reload', () => {
  expect(reloadPlan({ current: 'aaa', served: 'ccc', attempted: 'aaa->bbb', urlAttempt: 'aaa->bbb' })).toBe('reload');
});

test('a URL attempt for another source bundle does not suppress a reload', () => {
  expect(reloadPlan({ current: 'ccc', served: 'bbb', urlAttempt: 'aaa->bbb' })).toBe('reload');
});

test('an up-to-date or unknown page with no attempt does nothing', () => {
  expect(reloadPlan({ current: 'aaa', served: 'aaa' })).toBe('none');
  expect(reloadPlan({ current: 'aaa', served: '' })).toBe('none');
});

// --- adoptBuild against fake browser globals ---

const realStorage = globalThis.sessionStorage;
const realLocation = globalThis.location;
const realHistory = globalThis.history;

function installFakeEnv({ href = 'https://moa.test/', stored = null, storageThrows = false } = {}) {
  const parsed = new URL(href);
  const state = { stored, replaced: null, historyURL: null };
  const fail = () => {
    if (storageThrows) throw new Error('storage unavailable');
  };
  globalThis.sessionStorage = {
    getItem: () => { fail(); return state.stored; },
    setItem: (_, v) => { fail(); state.stored = v; },
    removeItem: () => { fail(); state.stored = null; },
  };
  globalThis.location = {
    href,
    pathname: parsed.pathname,
    search: parsed.search,
    hash: parsed.hash,
    replace: (u) => { state.replaced = u; },
  };
  globalThis.history = {
    state: { preserved: true },
    replaceState: (s, _t, u) => { state.historyState = s; state.historyURL = u; },
  };
  return state;
}

function restore(name, saved) {
  if (saved === undefined) delete globalThis[name];
  else globalThis[name] = saved;
}

afterEach(() => {
  restore('sessionStorage', realStorage);
  restore('location', realLocation);
  restore('history', realHistory);
  delete globalThis.__MOA_BUILD_ID__;
});

test('adoptBuild navigates immediately to the versioned shell', async () => {
  globalThis.__MOA_BUILD_ID__ = 'aaa';
  const env = installFakeEnv();
  const { adoptBuild } = await import('./stale-build.js?reload');

  expect(adoptBuild({ build_id: 'bbb' })).toBe('reload');
  expect(env.stored).toBe('aaa->bbb');
  expect(new URL(env.replaced).searchParams.get('__build')).toBe('aaa->bbb');
});

test('a concurrent response cannot consume a pending navigation', async () => {
  globalThis.__MOA_BUILD_ID__ = 'aaa';
  const env = installFakeEnv();
  const { adoptBuild } = await import('./stale-build.js?pending');

  let warned = 0;
  expect(adoptBuild({ build_id: 'bbb' })).toBe('reload');
  expect(adoptBuild({ build_id: 'bbb' }, { onStale: () => { warned++; } })).toBe('pending');
  expect(new URL(env.replaced).searchParams.get('__build')).toBe('aaa->bbb');
  expect(env.stored).toBe('aaa->bbb');
  expect(warned).toBe(0);
});

test('a failed transition warns once and remains guarded', async () => {
  globalThis.__MOA_BUILD_ID__ = 'aaa';
  const env = installFakeEnv({ href: 'https://moa.test/?__build=aaa-%3Ebbb', stored: 'aaa->bbb' });
  const { adoptBuild } = await import('./stale-build.js?giveup');

  let warned = 0;
  expect(adoptBuild({ build_id: 'bbb' }, { onStale: () => { warned++; } })).toBe('give-up');
  expect(adoptBuild({ build_id: 'bbb' }, { onStale: () => { warned++; } })).toBe('give-up');
  expect(warned).toBe(1);
  expect(env.replaced).toBe(null);
  expect(env.stored).toBe('aaa->bbb');
  expect(env.historyURL).toBe(null);
});

test('the URL prevents a loop when sessionStorage is unavailable', async () => {
  globalThis.__MOA_BUILD_ID__ = 'aaa';
  const env = installFakeEnv({ href: 'https://moa.test/?__build=aaa-%3Ebbb', storageThrows: true });
  const { adoptBuild } = await import('./stale-build.js?no-storage');

  expect(adoptBuild({ build_id: 'bbb' })).toBe('give-up');
  expect(env.replaced).toBe(null);
});

test('adoptBuild strips markers once the new bundle boots', async () => {
  globalThis.__MOA_BUILD_ID__ = 'bbb';
  const env = installFakeEnv({ href: 'https://moa.test/?view=grid&__build=aaa-%3Ebbb#tail', stored: 'aaa->bbb' });
  const { adoptBuild } = await import('./stale-build.js?clear');

  expect(adoptBuild({ build_id: 'bbb' })).toBe('clear');
  expect(env.stored).toBe(null);
  expect(env.historyURL).toBe('/?view=grid#tail');
  expect(env.historyState).toEqual({ preserved: true });
});

test('adoptBuild leaves the page alone when the server reports no build id', async () => {
  globalThis.__MOA_BUILD_ID__ = 'aaa';
  const env = installFakeEnv();
  const { adoptBuild } = await import('./stale-build.js?unknown');

  expect(adoptBuild({ current: 'v0.23.0' })).toBe('none');
  expect(env.replaced).toBe(null);
});
