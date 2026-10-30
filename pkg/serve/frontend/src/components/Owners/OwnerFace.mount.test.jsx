// OwnerFace.mount.test.jsx — mounts the real component (real preact, real
// scheduler) on a minimal fake DOM, because the bug lives in the component's
// effect: the scheduler writes the gaze behind the renderer's back, and a
// state change whose rendered style is unchanged must still rewrite the rest
// pose. Calling syncPose directly cannot catch that effect being dropped.

import { afterAll, beforeAll, expect, mock, test } from "bun:test";
import { h, render } from "preact";
import { act } from "preact/test-utils";
import { combineGaze, poseVars } from "./OwnerFace.jsx";
import { eyeStateFor } from "./avatar-identity.js";
import { __resetFaceMotionForTests, facePersonality } from "./faceMotion.js";

class Node_ {
  constructor(name, nodeType) {
    this.localName = name; this.nodeName = name.toUpperCase(); this.nodeType = nodeType;
    this.childNodes = []; this.parentNode = null; this.attrs = {};
    this.classes = new Set();
    this.props = new Map();
    this.style = {
      setProperty: (k, v) => this.props.set(k, String(v)),
      removeProperty: (k) => this.props.delete(k),
    };
    this.classList = {
      toggle: (c, on) => void (on ? this.classes.add(c) : this.classes.delete(c)),
      remove: (...cs) => cs.forEach((c) => this.classes.delete(c)),
      add: (...cs) => cs.forEach((c) => this.classes.add(c)),
    };
  }
  get firstChild() { return this.childNodes[0] || null; }
  get nextSibling() { const s = this.parentNode?.childNodes; return s ? s[s.indexOf(this) + 1] || null : null; }
  appendChild(c) { return this.insertBefore(c, null); }
  insertBefore(c, ref) {
    c.parentNode?.removeChild(c);
    const i = ref ? this.childNodes.indexOf(ref) : -1;
    if (i < 0) this.childNodes.push(c); else this.childNodes.splice(i, 0, c);
    c.parentNode = this; return c;
  }
  removeChild(c) { this.childNodes.splice(this.childNodes.indexOf(c), 1); c.parentNode = null; return c; }
  remove() { this.parentNode?.removeChild(this); }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  removeAttribute(k) { delete this.attrs[k]; }
  addEventListener() {}
  removeEventListener() {}
  set data(v) { this._d = v; }
  get data() { return this._d; }
  set textContent(v) { this._d = v; }
}

let timers; let clock; let seq;
const saved = {};
const globals = ["window", "document", "performance", "IntersectionObserver"];

beforeAll(() => {
  for (const k of globals) saved[k] = Object.getOwnPropertyDescriptor(globalThis, k);
  timers = new Map(); clock = 0; seq = 0;
  // Without an observer every face counts as visible; another file may leave one behind.
  delete globalThis.IntersectionObserver;
  globalThis.document = {
    hidden: false,
    createElement: (n) => new Node_(n, 1),
    createElementNS: (_, n) => new Node_(n, 1),
    createTextNode: (t) => { const n = new Node_("#text", 3); n.data = t; return n; },
    addEventListener() {},
  };
  globalThis.window = {
    matchMedia: () => ({ matches: false, addEventListener() {} }),
    addEventListener() {},
    setTimeout: (fn, ms) => { const id = ++seq; timers.set(id, { fn, at: clock + ms }); return id; },
    clearTimeout: (id) => timers.delete(id),
  };
  Object.defineProperty(globalThis, "performance", { value: { now: () => clock }, configurable: true });
});
afterAll(() => {
  for (const k of globals) {
    if (saved[k]) Object.defineProperty(globalThis, k, saved[k]); else delete globalThis[k];
  }
});

const advance = (ms) => {
  const end = clock + ms;
  for (;;) {
    let next = null;
    for (const [id, t] of timers) if (t.at <= end && (!next || t.at < next[1].at)) next = [id, t];
    if (!next) break;
    timers.delete(next[0]); clock = next[1].at; next[1].fn();
  }
  clock = end;
};

// Several suites replace "preact/hooks" with a stub for the whole process
// (bun's mock.module does not reset between files). This test needs real
// effects, so it installs the real module for its duration and puts back
// whatever it found.
test("OwnerFace rewrites the rest pose when its state changes", async () => {
  const found = { ...(await import("preact/hooks")) };
  const real = await import("../../../node_modules/preact/hooks/dist/hooks.module.js");
  mock.module("preact/hooks", () => ({ ...real }));
  try {
    await body();
  } finally {
    mock.module("preact/hooks", () => found);
  }
});

async function body() {
  const { OwnerFace } = await import("./OwnerFace.jsx");
  const { faceMotion } = await import("./faceMotion.js");
  // The scheduler is a singleton bound to the window it first saw.
  __resetFaceMotionForTests();
  const root = new Node_("div", 1);
  const p = facePersonality("mount-key");
  const mount = (state) => act(() => render(h(OwnerFace, { seedKey: "mount-key", shape: "circle", state }), root));
  const face = () => root.firstChild;
  const expectRest = (state) => {
    const want = poseVars(p, "circle", ...combineGaze(eyeStateFor(state), 0, 0));
    for (const [k, v] of Object.entries(want)) expect(face().props.get(k)).toBe(String(v));
  };

  // A pointer-following face would be steered by the scheduler on demand; the
  // scripted gaze is enough: run the clock until a gaze has been applied.
  const lookAround = () => {
    const before = [...face().props.entries()].join();
    for (let i = 0; i < 400 && [...face().props.entries()].join() === before; i++) advance(250);
    expect([...face().props.entries()].join()).not.toBe(before);
  };

  await mount("idle");
  expect(faceMotion()).not.toBeNull();
  expectRest("idle");
  lookAround();
  const idleRest = poseVars(p, "circle", 0, 0);
  expect(face().props.get("--lx")).not.toBe(String(idleRest["--lx"]));

  // The transitions whose rendered style does not change (idle → asks, idle →
  // saved) are the ones only the effect's rewrite can fix: each starts from an
  // idle face that has just looked away.
  for (const state of ["asks", "saved"]) {
    await mount(state);
    expectRest(state);
    expect(face().props.get("--lx")).toBe(String(idleRest["--lx"]));
    await mount("idle");
    expectRest("idle");
    lookAround();
  }

  for (const state of ["working", "asks", "saved"]) {
    await mount(state);
    expectRest(state);
    // asks is pinned on you (scale 0): nothing to look around at.
    if (state === "working") lookAround();
  }
  expect(face().props.get("--lx")).toBe(String(idleRest["--lx"]));
  await act(() => render(null, root));
  __resetFaceMotionForTests();
}
