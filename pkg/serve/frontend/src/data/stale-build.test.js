// stale-build.test.js — run with `bun test`.
//
// The pure decision (reloadPlan / isStale) is tested directly; adoptBuild is
// exercised against a minimal fake sessionStorage/location so the loop guard —
// the part that decides whether a reload is worth attempting again — is covered
// without a DOM.

import { test, expect, afterEach } from 'bun:test';
import { isStale, reloadPlan } from './stale-build.js';

test('isStale needs both ids: an unknown one never means stale', () => {
  expect(isStale('aaa', 'bbb')).toBe(true);
  expect(isStale('aaa', 'aaa')).toBe(false);
  // Old server (no build_id) or an unstamped bundle: cannot tell, so leave it.
  expect(isStale('aaa', '')).toBe(false);
  expect(isStale('', 'bbb')).toBe(false);
  expect(isStale('aaa', undefined)).toBe(false);
});

test('a superseded bundle reloads once', () => {
  expect(reloadPlan({ current: 'aaa', served: 'bbb', attempted: '' })).toBe('reload');
});

test('a reload that came back on the same stale bundle gives up instead of looping', () => {
  // The mark says we already reloaded for bbb, yet the page still runs aaa:
  // the cache won, and reloading again would spin forever.
  expect(reloadPlan({ current: 'aaa', served: 'bbb', attempted: 'bbb' })).toBe('give-up');
});

test('a reload that landed on the new bundle clears the mark', () => {
  expect(reloadPlan({ current: 'bbb', served: 'bbb', attempted: 'bbb' })).toBe('clear');
});

test('a further deploy after a failed attempt is a fresh reload, not a give-up', () => {
  // Attempted bbb, still on aaa, but the server now serves ccc: that is a new
  // build, and it deserves its own attempt.
  expect(reloadPlan({ current: 'aaa', served: 'ccc', attempted: 'bbb' })).toBe('reload');
});

test('an up-to-date page with no attempt behind it does nothing', () => {
  expect(reloadPlan({ current: 'aaa', served: 'aaa', attempted: '' })).toBe('none');
  expect(reloadPlan({ current: 'aaa', served: '', attempted: '' })).toBe('none');
});

// --- adoptBuild against fake browser globals ---

const realStorage = globalThis.sessionStorage;
const realLocation = globalThis.location;
const realHistory = globalThis.history;

function installFakeEnv({ href = 'https://moa.test/', stored = null } = {}) {
  const state = { stored, replaced: null, historyURL: null };
  globalThis.sessionStorage = {
    getItem: () => state.stored,
    setItem: (_, v) => { state.stored = v; },
    removeItem: () => { state.stored = null; },
  };
  globalThis.location = { href, pathname: '/', search: '', hash: '', replace: (u) => { state.replaced = u; } };
  globalThis.history = { replaceState: (_s, _t, u) => { state.historyURL = u; } };
  return state;
}

// Delete what bun's environment did not define instead of assigning undefined:
// another suite installs its own fake `location`, and a leftover own property
// set to undefined would shadow it.
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

test('adoptBuild reloads onto a URL the cache has no entry for', async () => {
  globalThis.__MOA_BUILD_ID__ = 'aaa';
  const env = installFakeEnv();
  const { adoptBuild } = await import('./stale-build.js?reload');

  expect(adoptBuild({ build_id: 'bbb' })).toBe('reload');
  expect(env.stored).toBe('bbb'); // marked, so a failed attempt is detectable
  // A plain reload may be served from cache — the very failure being worked
  // around — so the new URL must differ.
  await Bun.sleep(0); // the reload awaits the cache purge before navigating
  expect(env.replaced).toContain('__build=bbb');
});

test('adoptBuild warns instead of looping when the reload came back stale', async () => {
  globalThis.__MOA_BUILD_ID__ = 'aaa';
  const env = installFakeEnv({ stored: 'bbb' });
  const { adoptBuild } = await import('./stale-build.js?giveup');

  let warned = 0;
  expect(adoptBuild({ build_id: 'bbb' }, { onStale: () => { warned++; } })).toBe('give-up');
  expect(warned).toBe(1);
  expect(env.replaced).toBe(null); // no second navigation
  expect(env.stored).toBe(null);   // guard spent
});

test('adoptBuild strips the one-shot marker once the new bundle boots', async () => {
  globalThis.__MOA_BUILD_ID__ = 'bbb';
  const env = installFakeEnv({ href: 'https://moa.test/?__build=bbb', stored: 'bbb' });
  const { adoptBuild } = await import('./stale-build.js?clear');

  expect(adoptBuild({ build_id: 'bbb' })).toBe('clear');
  expect(env.stored).toBe(null);
  expect(env.historyURL).not.toContain('__build');
});

test('adoptBuild leaves the page alone when the server reports no build id', async () => {
  globalThis.__MOA_BUILD_ID__ = 'aaa';
  const env = installFakeEnv();
  const { adoptBuild } = await import('./stale-build.js?unknown');

  expect(adoptBuild({ current: 'v0.22.0' })).toBe('none');
  expect(env.replaced).toBe(null);
});
