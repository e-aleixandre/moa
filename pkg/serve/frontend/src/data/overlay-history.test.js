// overlay-history.test.js — run with `bun test`.
//
// bun's default test environment has no DOM (no `window`/`history`), so these
// tests install a minimal fake history/window on globalThis for the duration
// of each test and tear it down afterwards — that also exercises the
// "history absent" path by simply not installing it for the last block.
import { test, expect, beforeEach, afterEach } from 'bun:test';
import { openOverlay, closeOverlay, collapseForNavigation, __resetOverlayHistoryForTests } from './overlay-history.js';

function installFakeHistory() {
  const entries = [{ state: null }];
  let cursor = 0;
  const listeners = new Set();
  const history = {
    get state() { return entries[cursor].state; },
    pushState(state) {
      entries.splice(cursor + 1);
      entries.push({ state });
      cursor++;
    },
    back() {
      if (cursor === 0) return;
      cursor--;
      const event = { type: 'popstate' };
      // Fire asynchronously-ish is unnecessary here — real browsers dispatch
      // popstate synchronously-ish too for same-document navigations in
      // practice for our purposes; keep the test deterministic.
      listeners.forEach((fn) => fn(event));
    },
  };
  const win = {
    history,
    addEventListener(type, fn) { if (type === 'popstate') listeners.add(fn); },
    removeEventListener(type, fn) { if (type === 'popstate') listeners.delete(fn); },
  };
  globalThis.window = win;
  globalThis.history = history;
  return { history, listenerCount: () => listeners.size };
}

function uninstallFakeHistory() {
  delete globalThis.window;
  delete globalThis.history;
}

beforeEach(() => {
  __resetOverlayHistoryForTests();
});

afterEach(() => {
  __resetOverlayHistoryForTests();
  uninstallFakeHistory();
});

test('openOverlay pushes a single guard history entry', () => {
  const { history } = installFakeHistory();
  openOverlay('sheet-a', () => {});
  expect(history.state).toEqual({ moaOverlay: true });
});

test('a second overlay does not push another history entry (single guard for the stack)', () => {
  const { history } = installFakeHistory();
  let pushes = 0;
  const originalPush = history.pushState.bind(history);
  history.pushState = (...args) => { pushes++; return originalPush(...args); };
  openOverlay('sheet-a', () => {});
  openOverlay('sheet-b', () => {});
  expect(pushes).toBe(1);
});

test('back gesture (popstate) closes the top overlay with fromPop=true and issues no extra history.back()', () => {
  const { history } = installFakeHistory();
  const calls = [];
  openOverlay('sheet-a', (fromPop) => calls.push(fromPop));

  const backSpy = { count: 0 };
  const originalBack = history.back.bind(history);
  history.back = (...args) => { backSpy.count++; return originalBack(...args); };

  // Simulate the actual browser back button/gesture.
  originalBack();

  expect(calls).toEqual([true]);
  // The simulated user-triggered back is the one call; the listener must not
  // call history.back() again on top of it.
  expect(backSpy.count).toBe(0);
});

test('closing via UI (close()) calls history.back() exactly once to consume the entry', () => {
  const { history } = installFakeHistory();
  const close = openOverlay('sheet-a', () => {});

  let backCalls = 0;
  const originalBack = history.back.bind(history);
  history.back = (...args) => { backCalls++; return originalBack(...args); };

  close();

  expect(backCalls).toBe(1);
});

test('close() is idempotent — calling it twice only backs once', () => {
  const { history } = installFakeHistory();
  const close = openOverlay('sheet-a', () => {});

  let backCalls = 0;
  const originalBack = history.back.bind(history);
  history.back = (...args) => { backCalls++; return originalBack(...args); };

  close();
  close();

  expect(backCalls).toBe(1);
});

test('stacking: two overlays open, popstate closes only the top one', () => {
  installFakeHistory();
  const calls = [];
  openOverlay('sheet-a', (fromPop) => calls.push(['a', fromPop]));
  openOverlay('sheet-b', (fromPop) => calls.push(['b', fromPop]));

  history.back();

  expect(calls).toEqual([['b', true]]);
});

test('stacking: closing the bottom overlay out of order does not call history.back (guard still owns the next pop)', () => {
  installFakeHistory();
  const calls = [];
  const closeA = openOverlay('sheet-a', (fromPop) => calls.push(['a', fromPop]));
  openOverlay('sheet-b', (fromPop) => calls.push(['b', fromPop]));

  let backCalls = 0;
  const originalBack = history.back.bind(history);
  history.back = (...args) => { backCalls++; return originalBack(...args); };

  closeA(); // sheet-a is not on top — closing it must not consume the guard
  expect(backCalls).toBe(0);

  // sheet-b, still the top of the stack, is the one that resolves the next pop.
  history.back();
  expect(calls).toEqual([['b', true]]);
});

test('stacking: closing the top overlay via UI leaves the guard for the overlay below', () => {
  const { history } = installFakeHistory();
  const calls = [];
  openOverlay('sheet-a', (fromPop) => calls.push(['a', fromPop]));
  const closeB = openOverlay('sheet-b', (fromPop) => calls.push(['b', fromPop]));

  let backCalls = 0;
  const originalBack = history.back.bind(history);
  history.back = (...args) => { backCalls++; return originalBack(...args); };

  closeB();
  // B was not the final overlay, so its UI close must not consume A's guard.
  expect(backCalls).toBe(0);

  history.back();
  expect(calls).toEqual([['a', true]]);
  expect(history.state).toBeNull();
});

test('out-of-order close then back lands on the pre-overlay state, not a stranded entry (the bug fix)', () => {
  const { history } = installFakeHistory();
  // Baseline app state is the single entry the fake history starts with.
  const closeA = openOverlay('sheet-a', () => {});
  openOverlay('sheet-b', () => {});
  // Programmatically dismiss the bottom overlay while the top stays open.
  closeA();
  // Now close the top via back gesture: the guard is consumed, stack empties.
  history.back();
  // History must be back at the pre-overlay baseline (state null), with no
  // orphaned guard entry left in the middle that a further back would hit.
  expect(history.state).toBeNull();
});

test('back-closes-all: two overlays, two back gestures, guard re-pushed between them', () => {
  const { history } = installFakeHistory();
  const calls = [];
  openOverlay('sheet-a', (fromPop) => calls.push(['a', fromPop]));
  openOverlay('sheet-b', (fromPop) => calls.push(['b', fromPop]));
  history.back(); // closes b, re-pushes guard for a
  expect(history.state).toEqual({ moaOverlay: true });
  history.back(); // closes a, stack empties, no re-push
  expect(history.state).toBeNull();
  expect(calls).toEqual([['b', true], ['a', true]]);
});

test('back then UI-close consumes the re-pushed guard for the remaining overlay', () => {
  const { history, listenerCount } = installFakeHistory();
  const calls = [];
  const closeA = openOverlay('sheet-a', (fromPop) => calls.push(['a', fromPop]));
  openOverlay('sheet-b', (fromPop) => calls.push(['b', fromPop]));

  history.back(); // closes b and re-pushes the single guard for a
  let backCalls = 0;
  const originalBack = history.back.bind(history);
  history.back = (...args) => { backCalls++; return originalBack(...args); };

  closeA();

  expect(backCalls).toBe(1);
  expect(history.state).toBeNull();
  expect(listenerCount()).toBe(0);
  expect(calls).toEqual([['b', true]]);
});

test('the global popstate listener is removed once the stack empties', () => {
  const { listenerCount } = installFakeHistory();
  const close = openOverlay('sheet-a', () => {});
  expect(listenerCount()).toBe(1);
  close();
  expect(listenerCount()).toBe(0);
});

test('the global popstate listener is removed after back closes the final overlay', () => {
  const { history, listenerCount } = installFakeHistory();
  openOverlay('sheet-a', () => {});
  expect(listenerCount()).toBe(1);

  history.back();

  expect(listenerCount()).toBe(0);
});

test('tolerates a missing history/window (SSR/jsdom-less tests): open/close never throw', () => {
  uninstallFakeHistory();
  const calls = [];
  const close = openOverlay('sheet-a', (fromPop) => calls.push(fromPop));
  expect(() => close()).not.toThrow();
  expect(calls).toEqual([false]);
});

test('closeOverlay(id) called for an id not on the stack is a no-op', () => {
  installFakeHistory();
  expect(() => closeOverlay('never-opened')).not.toThrow();
});

test('collapseForNavigation closes every open overlay and reports a guard was active', () => {
  const { listenerCount } = installFakeHistory();
  const calls = [];
  openOverlay('sheet-a', (fromPop) => calls.push(['a', fromPop]));
  openOverlay('sheet-b', (fromPop) => calls.push(['b', fromPop]));
  const hadGuard = collapseForNavigation();
  expect(hadGuard).toBe(true);
  // Both closed with fromPop=true (history-driven: they must NOT call back()).
  expect(calls).toEqual([['a', true], ['b', true]]);
  // Stack drained + listener removed so no stranded guard survives the nav.
  expect(listenerCount()).toBe(0);
});

test('collapseForNavigation is a no-op returning false when nothing is open', () => {
  installFakeHistory();
  expect(collapseForNavigation()).toBe(false);
});
