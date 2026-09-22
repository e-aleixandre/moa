// overlay-layers.test.js — run with `bun test`.
//
// Two things to pin. First the ordering contract the artifacts drawer depends
// on (its capture-phase Escape must defer to anything above it). Second the
// INVERSION that replaced data/overlay-history.js: registering a layer must not
// touch the History API at all — no pushState, no back(), no popstate listener
// — because a browser back/forward gesture has no meaning in the installed app
// and the old guard entry left a forward gesture pointing at an inert entry.

import { test, expect, beforeEach, afterEach } from 'bun:test';
import { pushLayer, isTopLayer, sheetLayer, takeKey, __resetOverlayLayersForTests } from './overlay-layers.js';

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

// ── One sheet at a time ─────────────────────────────────────────────────────
// A sheet component as the rule sees it: what it has been told about being
// covered, and the stack handle it drives from open/close/unmount.
function sheet(id) {
  const seen = { id, covered: false, calls: 0 };
  seen.layer = sheetLayer(id, (covered) => { seen.covered = covered; seen.calls++; });
  return seen;
}

test('a sheet asked for over another covers it instead of stacking on it', () => {
  const panel = sheet('panel');
  panel.layer.open();
  const timeline = sheet('timeline');
  timeline.layer.open();
  expect(panel.covered).toBe(true);
  expect(timeline.covered).toBe(false);
});

test('closing the sheet on top shows the covered one again, and the keys are its own again', () => {
  const panel = sheet('panel');
  panel.layer.open();
  const timeline = sheet('timeline');
  timeline.layer.open();
  timeline.layer.close();
  expect(panel.covered).toBe(false);
  expect(isTopLayer('panel')).toBe(true);
});

test('one Escape closes only the visible sheet, whichever handler hears it first', () => {
  for (const order of [['timeline', 'panel'], ['panel', 'timeline']]) {
    __resetOverlayLayersForTests();
    const sheets = { panel: sheet('panel'), timeline: sheet('timeline') };
    sheets.panel.layer.open();
    sheets.timeline.layer.open();
    const escape = { key: 'Escape' };
    const closed = [];
    for (const id of order) {
      if (!takeKey(id, escape)) continue;
      closed.push(id);
      sheets[id].layer.close(); // what its Escape does
    }
    expect(closed).toEqual(['timeline']);
    expect(sheets.panel.covered).toBe(false);
  }
});

test('a hand-off never shows both: the sheet leaving is hidden once the next one opens', () => {
  const confirm = sheet('confirm');
  confirm.layer.open();
  confirm.layer.close(); // still painting its exit
  expect(confirm.covered).toBe(false);
  const timeline = sheet('timeline');
  timeline.layer.open();
  expect(confirm.covered).toBe(true);
  expect(timeline.covered).toBe(false);
});

test('with no sheet open, a sheet on its way out keeps painting its exit', () => {
  const a = sheet('a');
  a.layer.open();
  a.layer.close();
  expect(a.covered).toBe(false);
  expect(isTopLayer('a')).toBe(false);
});

test('only the newest of three is visible; the others come back in order', () => {
  const [a, b, c] = ['a', 'b', 'c'].map(sheet);
  a.layer.open(); b.layer.open(); c.layer.open();
  expect([a.covered, b.covered, c.covered]).toEqual([true, true, false]);
  c.layer.close();
  expect([a.covered, b.covered]).toEqual([true, false]);
  b.layer.close();
  expect(a.covered).toBe(false);
});

test('reopening a covered sheet brings it back on top and covers the other', () => {
  const a = sheet('a');
  const b = sheet('b');
  a.layer.open();
  b.layer.open();
  a.layer.open();
  expect(a.covered).toBe(false);
  expect(b.covered).toBe(true);
  expect(isTopLayer('a')).toBe(true);
});

test('a full-screen layer neither hides nor is hidden: a sheet over it is the only sheet', () => {
  pushLayer('live-preview');
  const message = sheet('message');
  message.layer.open();
  expect(message.covered).toBe(false);
  expect(isTopLayer('message')).toBe(true);
  message.layer.close();
  expect(isTopLayer('live-preview')).toBe(true);
});

test('a covered sheet that goes away leaves quietly and the stack keeps its rule', () => {
  const a = sheet('a');
  const b = sheet('b');
  a.layer.open();
  b.layer.open();
  const calls = a.calls;
  a.layer.release(); // unmounted while covered
  expect(a.calls).toBe(calls);
  expect(b.covered).toBe(false);
  expect(isTopLayer('b')).toBe(true);
});
