import { expect, test } from "bun:test";
import { chainPan } from "./zoom.js";
import { createScrollChain, MAX_PENDING, setViewIfChanged } from "./scroll-chain.js";

// The chain is the only bookkeeping the zoomed scroll has: which packet an
// answer belongs to, and whether that answer still belongs to what the user is
// doing now. Everything it protects is a race, so it is worth testing alone.

const request = { dx: -150, dy: 0, scale: 2 };

test("an answer is applied once and never twice", () => {
  const chain = createScrollChain();
  const id = chain.request(request);

  expect(chain.resolve(id, { dx: -100, dy: 0 })).toEqual(request);
  expect(chain.resolve(id, { dx: -100, dy: 0 })).toBeNull();
  expect(chain.pendingCount).toBe(0);
});

test("an unknown or non-numeric answer moves nothing", () => {
  const chain = createScrollChain();
  const id = chain.request(request);

  expect(chain.resolve("s99", { dx: 0, dy: 0 })).toBeNull();
  expect(chain.resolve(id, { dx: Number.NaN, dy: 0 })).toBeNull();
  expect(chain.resolve(id, { dx: -1, dy: 0 })).toBeNull(); // already spent above
});

test("a pinch, a reset or a new frame drops the answers still in flight", () => {
  const chain = createScrollChain();
  const first = chain.request(request);
  const second = chain.request(request);

  chain.invalidate();

  expect(chain.resolve(first, { dx: 0, dy: 0 })).toBeNull();
  expect(chain.resolve(second, { dx: 0, dy: 0 })).toBeNull();
  expect(chain.pendingCount).toBe(0);
});

// The finger leaving is not an invalidation: the last packet it produced is the
// same movement and its answer still belongs to that gesture.
test("an answer arriving after the touch ended still completes it", () => {
  const chain = createScrollChain();
  const id = chain.request(request);

  expect(chain.resolve(id, { dx: -20, dy: 0 })).toEqual(request);
});

test("a new one-finger gesture drops an answer retained for the ordinary prior touch end", () => {
  const chain = createScrollChain();
  const held = chain.request(request);

  // Ordinary touchend leaves this receipt valid; the following touchstart does not.
  chain.invalidate();

  expect(chain.resolve(held, { dx: 0, dy: 0 })).toBeNull();
});

test("answers are resolved in the order the requests left, and identities are distinct", () => {
  const chain = createScrollChain();
  const first = chain.request({ dx: -10, dy: 0, scale: 2 });
  const second = chain.request({ dx: -20, dy: 0, scale: 2 });

  expect(first).not.toBe(second);
  expect(chain.pendingCount).toBe(2);
  expect(chain.resolve(first, { dx: -10, dy: 0 })).toEqual({ dx: -10, dy: 0, scale: 2 });
  expect(chain.resolve(second, { dx: -20, dy: 0 })).toEqual({ dx: -20, dy: 0, scale: 2 });
});

test("an app that stops answering retains only recent receipts", () => {
  const chain = createScrollChain();
  const ids = Array.from({ length: MAX_PENDING + 2 }, () => chain.request(request));

  expect(chain.pendingCount).toBe(MAX_PENDING);
  expect(chain.resolve(ids[0], { dx: 0, dy: 0 })).toBeNull();
  expect(chain.resolve(ids.at(-1), { dx: 0, dy: 0 })).toEqual(request);
  expect(chain.resolve(ids.at(-1), { dx: 0, dy: 0 })).toBeNull();
});

test("fully consumed and fully clamped replies do not call the view setter", () => {
  const frame = { base: 1, w: 390, h: 800 };
  const stage = { w: 390, h: 800 };
  const view = { zoom: 2, x: 0, y: 0 };
  let calls = 0;
  const setView = () => { calls++; };

  const consumed = chainPan(view, { dx: 0, dy: -150, scale: 2 }, { dx: 0, dy: -150 }, frame, stage);
  const clamped = chainPan(view, { dx: 0, dy: -150, scale: 2 }, { dx: 0, dy: 0 }, frame, stage);

  expect(setViewIfChanged(setView, view, consumed)).toBe(false);
  expect(setViewIfChanged(setView, view, clamped)).toBe(false);
  expect(calls).toBe(0);
});
