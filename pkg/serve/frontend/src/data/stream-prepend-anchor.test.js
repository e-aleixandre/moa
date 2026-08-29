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

test('the leading truncation marker is never used as the anchor', () => {
  // Reproduces the reported symptom: "Older messages…" is re-rendered above
  // every incoming page, so anchoring to it leaves the reader pinned at the
  // top and the page lands below them.
  let markerTop = 50;
  let seamTop = 87;
  let firstTop = 300;
  const marker = {
    dataset: { streamAnchor: 'sys-truncated' },
    getBoundingClientRect: () => ({ top: markerTop, bottom: markerTop + 17 }),
  };
  const seam = {
    dataset: { streamAnchor: 'doc-seam-0' },
    getBoundingClientRect: () => ({ top: seamTop, bottom: seamTop + 200 }),
  };
  const firstMessage = {
    dataset: { streamAnchor: 'doc-bb5c52cd8b94b338-0' },
    getBoundingClientRect: () => ({ top: firstTop, bottom: firstTop + 1079 }),
  };
  const el = {
    scrollTop: 0,
    getBoundingClientRect: () => ({ top: 0 }),
    querySelectorAll: () => [marker, seam, firstMessage],
  };

  const captured = capturePrependAnchor(el);
  expect(captured.id).toBe('doc-bb5c52cd8b94b338-0');

  // The page is inserted above: every surviving block shifts down by its height.
  firstTop += 2369;
  restorePrependAnchor(el, captured, false);

  // The reader keeps the message they were on; the page went above them.
  expect(el.scrollTop).toBe(2369);
  expect(firstMessage.getBoundingClientRect().top - el.scrollTop).toBe(captured.offset);
});

test('the block after the seam is captured, since the seam block is re-keyed', () => {
  // Verified against a real 16k-message session: on a prepend the first
  // history block is the only one whose id does not survive, because the
  // incoming page continues that same document and projection re-keys it to
  // the earlier message it now starts from. Anchoring to it loses the anchor.
  const seam = {
    dataset: { streamAnchor: 'seam' },
    getBoundingClientRect: () => ({ top: -20, bottom: -1 }),
  };
  const visible = {
    dataset: { streamAnchor: 'visible' },
    getBoundingClientRect: () => ({ top: 20, bottom: 60 }),
  };
  const el = {
    scrollTop: 20,
    getBoundingClientRect: () => ({ top: 0 }),
    querySelectorAll: () => [seam, visible],
  };

  expect(capturePrependAnchor(el).id).toBe('visible');
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

// Reproduces the reported symptom: "I scroll to the top, older messages load,
// and sometimes they appear BELOW me — I stay at the top and everything I was
// reading ends up underneath."
//
// A restored subagent card carries no message ID, so its block is keyed by
// position (`doc-3`). Prepending a page shifts every index, that ID names a
// different block, and the captured anchor is gone — the restore path then
// falls back to the raw scrollTop, which near the top means "stay at the top"
// while the whole page lands above the reader's content.
test('a positional block ID is never chosen as the scroll anchor', () => {
  const { unstableBlockIDs } = require('./stream-model.js');
  unstableBlockIDs.clear();
  unstableBlockIDs.add('doc-3');

  const seam = {
    dataset: { streamAnchor: 'doc-seam-0' },
    getBoundingClientRect: () => ({ top: 80, bottom: 280 }),
  };
  const subagentCard = {
    dataset: { streamAnchor: 'doc-3' },
    getBoundingClientRect: () => ({ top: 300, bottom: 420 }),
  };
  const durable = {
    dataset: { streamAnchor: 'doc-bb5c52cd8b94b338-0' },
    getBoundingClientRect: () => ({ top: 440, bottom: 900 }),
  };
  const el = {
    scrollTop: 12,
    getBoundingClientRect: () => ({ top: 0 }),
    querySelectorAll: () => [seam, subagentCard, durable],
  };

  const snapshot = capturePrependAnchor(el);
  expect(snapshot.id).toBe('doc-bb5c52cd8b94b338-0');

  // After the prepend the positional ID names another block entirely; only a
  // durable ID still resolves, which is what keeps the reader in place.
  const moved = {
    dataset: { streamAnchor: 'doc-bb5c52cd8b94b338-0' },
    getBoundingClientRect: () => ({ top: 8440, bottom: 8900 }),
  };
  const after = {
    scrollTop: 12,
    getBoundingClientRect: () => ({ top: 0 }),
    querySelectorAll: () => [moved],
  };
  expect(restorePrependAnchor(after, snapshot, false)).toBe(moved);
  expect(after.scrollTop).toBe(8012);
});
