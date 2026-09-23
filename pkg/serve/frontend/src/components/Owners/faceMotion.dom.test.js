// faceMotion.dom.test.js — the DOM glue's one global listener.
//
// The pointer is watched by a single window-level pointermove listener that
// must exist only while a visible face with `follow` does. The identity
// picker is the case that matters: its preview follows the pointer, and
// closing the New owner / Edit owner sheet unmounts it — the listener must
// go with it rather than live for the rest of the session.

import { afterEach, beforeEach, expect, test } from "bun:test";
import { __resetFaceMotionForTests, faceMotion } from "./faceMotion.js";

let listeners;
let observed;
let ioCallback;
let docListeners;

function install({ fine = true } = {}) {
  listeners = new Map();
  observed = new Set();
  docListeners = new Map();
  const add = (map) => (type, fn) => {
    if (!map.has(type)) map.set(type, new Set());
    map.get(type).add(fn);
  };
  const remove = (map) => (type, fn) => map.get(type)?.delete(fn);
  globalThis.window = {
    matchMedia: (q) => ({ matches: q.includes("pointer: fine") ? fine : false, addEventListener() {} }),
    setTimeout: () => 1,
    clearTimeout() {},
    addEventListener: add(listeners),
    removeEventListener: remove(listeners),
  };
  globalThis.document = { hidden: false, addEventListener: add(docListeners) };
  globalThis.IntersectionObserver = class {
    constructor(cb) { ioCallback = cb; }
    observe(el) { observed.add(el); }
    unobserve(el) { observed.delete(el); }
  };
  __resetFaceMotionForTests();
}

function show(el, visible = true) {
  ioCallback([{ target: el, isIntersecting: visible }]);
}

const moveListeners = () => listeners.get("pointermove")?.size || 0;

beforeEach(() => install());
afterEach(() => {
  __resetFaceMotionForTests();
  delete globalThis.window;
  delete globalThis.document;
  delete globalThis.IntersectionObserver;
});

const face = (follow) => ({ el: { getBoundingClientRect: () => ({}) }, seedKey: "moa", follow, apply() {} });

test("the pointer listener exists only while a visible follow face does", () => {
  const motion = faceMotion();
  const plain = face(false);
  const offPlain = motion.register(plain);
  show(plain.el);
  expect(moveListeners()).toBe(0);

  const preview = face(true);
  const offPreview = motion.register(preview);
  // Mounted but not yet on screen: nothing to follow with.
  expect(moveListeners()).toBe(0);
  show(preview.el);
  expect(moveListeners()).toBe(1);

  // Closing the picker unmounts the preview.
  offPreview();
  expect(moveListeners()).toBe(0);
  expect(observed.has(preview.el)).toBe(false);
  offPlain();
});

test("scrolling the follow face away or hiding the tab drops the listener", () => {
  const motion = faceMotion();
  const preview = face(true);
  const off = motion.register(preview);
  show(preview.el);
  expect(moveListeners()).toBe(1);
  show(preview.el, false);
  expect(moveListeners()).toBe(0);
  show(preview.el);
  expect(moveListeners()).toBe(1);

  document.hidden = true;
  for (const fn of docListeners.get("visibilitychange")) fn();
  expect(moveListeners()).toBe(0);
  document.hidden = false;
  for (const fn of docListeners.get("visibilitychange")) fn();
  expect(moveListeners()).toBe(1);
  off();
  expect(moveListeners()).toBe(0);
});

test("reduced motion and touch-only pointers never listen", () => {
  const motion = faceMotion();
  const preview = face(true);
  motion.register(preview);
  show(preview.el);
  motion.setForcedReduced(true);
  expect(moveListeners()).toBe(0);
  motion.setForcedReduced(false);
  expect(moveListeners()).toBe(1);

  install({ fine: false });
  const touch = faceMotion();
  const other = face(true);
  touch.register(other);
  show(other.el);
  expect(moveListeners()).toBe(0);
});
