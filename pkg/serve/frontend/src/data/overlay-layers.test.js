// overlay-layers.test.js — run with `bun test`.
//
// Two things to pin. First the ordering contract the artifacts drawer depends
// on (its capture-phase Escape must defer to anything above it). Second the
// INVERSION that replaced data/overlay-history.js: registering a layer must not
// touch the History API at all — no pushState, no back(), no popstate listener
// — because a browser back/forward gesture has no meaning in the installed app
// and the old guard entry left a forward gesture pointing at an inert entry.

import { test, expect, beforeEach, afterEach } from 'bun:test';
import { pushLayer, isTopLayer, __resetOverlayLayersForTests } from './overlay-layers.js';

let calls;

function installSpyHistory() {
  calls = [];
  const history = {
    state: null,
    length: 1,
    pushState() { calls.push('pushState'); },
    replaceState() { calls.push('replaceState'); },
    back() { calls.push('back'); },
    forward() { calls.push('forward'); },
    go() { calls.push('go'); },
  };
  globalThis.window = {
    history,
    addEventListener(type) { calls.push(`addEventListener:${type}`); },
    removeEventListener(type) { calls.push(`removeEventListener:${type}`); },
  };
  globalThis.history = history;
}

function uninstallSpyHistory() {
  delete globalThis.window;
  delete globalThis.history;
}

beforeEach(() => {
  __resetOverlayLayersForTests();
});

afterEach(() => {
  __resetOverlayLayersForTests();
  uninstallSpyHistory();
});

test('the pushed layer is the top one', () => {
  pushLayer('artifacts');
  expect(isTopLayer('artifacts')).toBe(true);
});

test('a layer pushed on top takes ownership; popping it gives it back', () => {
  pushLayer('artifacts');
  const popSheet = pushLayer('sheet-1');
  expect(isTopLayer('artifacts')).toBe(false);
  expect(isTopLayer('sheet-1')).toBe(true);
  popSheet();
  expect(isTopLayer('artifacts')).toBe(true);
});

test('popping out of order removes only that layer', () => {
  const popBottom = pushLayer('artifacts');
  pushLayer('sheet-1');
  popBottom();
  expect(isTopLayer('sheet-1')).toBe(true);
  expect(isTopLayer('artifacts')).toBe(false);
});

test('the returned pop is idempotent', () => {
  const pop = pushLayer('artifacts');
  pushLayer('sheet-1');
  pop();
  pop();
  expect(isTopLayer('sheet-1')).toBe(true);
});

test('isTopLayer is false when nothing is open', () => {
  expect(isTopLayer('artifacts')).toBe(false);
});

test('registering and releasing layers never touches the History API', () => {
  installSpyHistory();
  const popA = pushLayer('artifacts');
  const popB = pushLayer('sheet-1');
  popB();
  popA();
  expect(calls).toEqual([]);
});

test('works with no window at all (SSR / DOM-less tests)', () => {
  expect(() => pushLayer('artifacts')()).not.toThrow();
});
