import { expect, test } from 'bun:test';
import { capturePrependAnchor, restorePrependAnchor } from './stream-prepend-anchor.js';

function fixture({ scrollTop = 500, anchorTop = 700, height = 2000 } = {}) {
  const el = {
    scrollTop, scrollHeight: height,
    getBoundingClientRect: () => ({ top: 0 }),
    querySelectorAll: () => [node],
  };
  const node = {
    dataset: { streamAnchor: 'reader' },
    getBoundingClientRect: () => ({ top: anchorTop, bottom: anchorTop + 40 }),
  };
  return { el, node, moveAnchor: top => { anchorTop = top; } };
}

test('element anchor preserves the reader across a prepend', () => {
  const f = fixture();
  const captured = capturePrependAnchor(f.el);
  f.moveAnchor(1300); // 600px page inserted above the visible block
  restorePrependAnchor(f.el, captured, false);
  expect(f.el.scrollTop).toBe(1100);
});

test('tail growth concurrent with a prepend never enters the anchor correction', () => {
  const f = fixture();
  const captured = capturePrependAnchor(f.el);
  f.el.scrollHeight += 40; // live delta at the bottom: the audit counterexample
  f.moveAnchor(1300);
  restorePrependAnchor(f.el, captured, false);
  expect(f.el.scrollTop).toBe(1100);
  // The rejected height-based formula would produce 1140 here:
  expect(500 + (2640 - 2000)).toBe(1140);
});

test('a user flick after capture wins over a pending prepend restore', () => {
  const f = fixture();
  const captured = capturePrependAnchor(f.el);
  f.el.scrollTop = 420;
  f.moveAnchor(1300);
  expect(restorePrependAnchor(f.el, captured, false)).toBeNull();
  expect(f.el.scrollTop).toBe(420);
});

test('stick-to-bottom has precedence', () => {
  const f = fixture();
  const captured = capturePrependAnchor(f.el);
  f.moveAnchor(1300);
  expect(restorePrependAnchor(f.el, captured, true)).toBeNull();
  expect(f.el.scrollTop).toBe(500);
});

test('a removed anchor explicitly retains the captured scroll position', () => {
  const f = fixture();
  const captured = capturePrependAnchor(f.el);
  f.el.querySelectorAll = () => [];
  f.el.scrollTop = 900; // browser layout moved it while the block was replaced
  expect(restorePrependAnchor(f.el, captured, false)).toBeNull();
  expect(f.el.scrollTop).toBe(500);
});

test('a viewport without a durable block still captures a scroll fallback', () => {
  const f = fixture();
  f.el.querySelectorAll = () => [];
  const captured = capturePrependAnchor(f.el);
  expect(captured).toMatchObject({ id: '', scrollTop: 500 });
  f.el.scrollTop = captured.scrollTop;
  restorePrependAnchor(f.el, captured, false);
  expect(f.el.scrollTop).toBe(500);
});
