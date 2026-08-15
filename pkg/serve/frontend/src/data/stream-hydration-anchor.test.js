import { test, expect } from 'bun:test';
import { captureHydrationAnchor, restoreHydrationAnchor } from './stream-hydration-anchor.js';

function anchor(id, top, bottom) {
  return {
    dataset: { streamAnchor: id },
    getBoundingClientRect: () => ({ top, bottom }),
  };
}

function scroller(nodes, scrollTop = 0) {
  return {
    scrollTop,
    querySelectorAll: () => nodes,
    getBoundingClientRect: () => ({ top: 100 }),
  };
}

test('a scrolled-up reader keeps their surviving block at the same viewport offset after init', () => {
  const before = scroller([anchor('kept', 120, 190)], 50);
  const snapshot = captureHydrationAnchor(before, 's1', true);
  const after = scroller([anchor('kept', 150, 220)], 50);

  expect(restoreHydrationAnchor(after, snapshot, 's1', false, false)).toBe(true);
  expect(after.scrollTop).toBe(80);
});

test('a removed anchor retains the reader absolute position instead of following the refreshed tail', () => {
  const previous = { sessionId: 's1', historyPending: true, id: 'removed', offset: 20, scrollTop: 50 };
  const after = scroller([anchor('new', 150, 220)], 300);

  restoreHydrationAnchor(after, previous, 's1', false, false);
  expect(after.scrollTop).toBe(50);
});

test('pinned readers are left for the normal follow-to-bottom path', () => {
  const previous = { sessionId: 's1', historyPending: true, id: 'kept', offset: 20, scrollTop: 50 };
  const after = scroller([anchor('kept', 150, 220)], 50);

  expect(restoreHydrationAnchor(after, previous, 's1', false, true)).toBe(false);
  expect(after.scrollTop).toBe(50);
});
