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

test('a page loaded at the transcript top leaves the first existing block in place', () => {
  const f = fixture({ scrollTop: 0, anchorTop: 0 });
  const captured = capturePrependAnchor(f.el);
  // A one-pixel momentum adjustment during the request must not turn this
  // into a second, unrequested history load at the top.
  f.el.scrollTop = 1;
  f.moveAnchor(600); // the new page was inserted above the existing first block

  restorePrependAnchor(f.el, captured, false);

  expect(f.el.scrollTop).toBe(601);
  expect(f.node.getBoundingClientRect().top - f.el.getBoundingClientRect().top - f.el.scrollTop).toBe(-1);
});

test('the first durable block is captured even when it is just above the viewport', () => {
  const first = {
    dataset: { streamAnchor: 'first' },
    getBoundingClientRect: () => ({ top: -20, bottom: -1 }),
  };
  const visible = {
    dataset: { streamAnchor: 'visible' },
    getBoundingClientRect: () => ({ top: 20, bottom: 60 }),
  };
  const el = {
    scrollTop: 20,
    getBoundingClientRect: () => ({ top: 0 }),
    querySelectorAll: () => [first, visible],
  };

  expect(capturePrependAnchor(el).id).toBe('first');
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

test('a small momentum adjustment still receives the prepend correction', () => {
  const f = fixture();
  const captured = capturePrependAnchor(f.el);
  f.el.scrollTop = 510;
  f.moveAnchor(1300);

  restorePrependAnchor(f.el, captured, false);

  expect(f.el.scrollTop).toBe(1110);
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
