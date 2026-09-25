// aurora-wash.test.js — run with `bun test`.
//
// The composer's voice wash: a loop that follows the microphone's level, that
// must stop whenever nobody can see it, leave nothing running when the
// composer leaves the voice state, never touch the tracks it reads, and do
// nothing at all under reduced motion.
import { expect, test } from "bun:test";
import { startAuroraWash } from "./aurora-wash.js";
import { auroraActive } from "./ComposerAurora.jsx";

function classes() {
  const set = new Set();
  return {
    add: (c) => set.add(c),
    remove: (c) => set.delete(c),
    toggle: (c, on) => (on ? set.add(c) : set.delete(c)),
    contains: (c) => set.has(c),
  };
}

function harness({ amplitude = 0 } = {}) {
  const frames = new Map();
  let nextId = 1;
  const docListeners = new Map();
  const observers = [];
  const contexts = [];
  const stoppedTracks = [];
  const env = {
    document: {
      hidden: false,
      addEventListener: (t, fn) => docListeners.set(t, fn),
      removeEventListener: (t, fn) => { if (docListeners.get(t) === fn) docListeners.delete(t); },
    },
    requestAnimationFrame: (fn) => { const id = nextId++; frames.set(id, fn); return id; },
    cancelAnimationFrame: (id) => frames.delete(id),
    IntersectionObserver: class {
      constructor(cb) { this.cb = cb; this.disconnected = false; observers.push(this); }
      observe() {}
      disconnect() { this.disconnected = true; }
    },
    AudioContext: class {
      constructor() { this.closed = false; contexts.push(this); }
      createMediaStreamSource() { return { connect() {} }; }
      createAnalyser() {
        return {
          fftSize: 0,
          getFloatTimeDomainData: (buf) => { for (let i = 0; i < buf.length; i++) buf[i] = i % 2 ? amplitude : -amplitude; },
        };
      }
      resume() { return Promise.resolve(); }
      close() { this.closed = true; return Promise.resolve(); }
    },
  };
  const stream = { getTracks: () => [{ stop: () => stoppedTracks.push(1) }] };
  const el = { classList: classes() };
  const body = { style: {} };
  // Runs every pending frame at `now`, the way the browser would.
  const tick = (now) => {
    const pending = [...frames.values()];
    frames.clear();
    for (const fn of pending) fn(now);
  };
  const scale = () => Number(/scaleY\(([\d.]+)\)/.exec(body.style.transform)?.[1] ?? NaN);
  return { env, stream, el, body, frames, docListeners, observers, contexts, stoppedTracks, tick, scale };
}

test("the wash is on only while dictating or during a live call", () => {
  expect(auroraActive({ recording: true, callPhase: "idle" })).toBe(true);
  expect(auroraActive({ recording: false, callPhase: "live" })).toBe(true);
  for (const callPhase of ["idle", "connecting", "closing", undefined]) {
    expect(auroraActive({ recording: false, callPhase })).toBe(false);
  }
});

test("the clouds rise with the voice: a loud mic lifts them higher than a quiet one", () => {
  const measure = (amplitude) => {
    const h = harness({ amplitude });
    const stop = startAuroraWash(h.el, h.body, { getStream: () => h.stream, env: h.env });
    for (let t = 0; t <= 1000; t += 34) h.tick(t);
    const s = h.scale();
    stop();
    return s;
  };
  const quiet = measure(0);
  const loud = measure(0.2);
  expect(quiet).toBeCloseTo(0.5, 2);
  expect(loud).toBeGreaterThan(1.1);
});

test("waits for the microphone: no stream yet is silence, not an error", () => {
  const h = harness({ amplitude: 0.2 });
  let stream = null;
  const stop = startAuroraWash(h.el, h.body, { getStream: () => stream, env: h.env });
  h.tick(0);
  expect(h.contexts).toHaveLength(0);
  expect(h.scale()).toBeCloseTo(0.5, 2);
  stream = h.stream;
  h.tick(40);
  expect(h.contexts).toHaveLength(1);
  stop();
});

test("follows the live microphone when it changes, closing the old meter", () => {
  const h = harness({ amplitude: 0.1 });
  const other = { getTracks: () => [] };
  let stream = h.stream;
  const stop = startAuroraWash(h.el, h.body, { getStream: () => stream, env: h.env });
  h.tick(0);
  stream = other;
  h.tick(40);
  expect(h.contexts).toHaveLength(2);
  expect(h.contexts[0].closed).toBe(true);
  expect(h.contexts[1].closed).toBe(false);
  stream = null;
  h.tick(80);
  expect(h.contexts[1].closed).toBe(true);
  stop();
});

test("stops while the composer is off screen or the tab is hidden, and resumes", () => {
  const h = harness();
  const stop = startAuroraWash(h.el, h.body, { getStream: () => h.stream, env: h.env });
  expect(h.frames.size).toBe(1);

  h.observers[0].cb([{ isIntersecting: false }]);
  expect(h.frames.size).toBe(0);
  expect(h.el.classList.contains("is-paused")).toBe(true);

  h.observers[0].cb([{ isIntersecting: true }]);
  expect(h.frames.size).toBe(1);
  expect(h.el.classList.contains("is-paused")).toBe(false);

  h.env.document.hidden = true;
  h.docListeners.get("visibilitychange")();
  expect(h.frames.size).toBe(0);
  expect(h.el.classList.contains("is-paused")).toBe(true);

  h.env.document.hidden = false;
  h.docListeners.get("visibilitychange")();
  expect(h.frames.size).toBe(1);
  stop();
});

test("teardown leaves nothing running and never stops the capture's tracks", () => {
  const h = harness({ amplitude: 0.1 });
  const stop = startAuroraWash(h.el, h.body, { getStream: () => h.stream, env: h.env });
  h.tick(0);
  expect(h.contexts).toHaveLength(1);
  const dispatched = [...h.frames.values()];
  stop();
  expect(h.frames.size).toBe(0);
  // A frame the browser had already queued must not reschedule itself.
  for (const fn of dispatched) fn(100);
  expect(h.frames.size).toBe(0);
  expect(h.observers[0].disconnected).toBe(true);
  expect(h.docListeners.has("visibilitychange")).toBe(false);
  expect(h.contexts[0].closed).toBe(true);
  expect(h.stoppedTracks).toHaveLength(0);
});

test("reduced motion is a still picture: no loop, no audio graph, no observers", () => {
  const h = harness({ amplitude: 0.2 });
  let asked = 0;
  const stop = startAuroraWash(h.el, h.body, { getStream: () => { asked++; return h.stream; }, reduced: true, env: h.env });
  expect(h.el.classList.contains("is-still")).toBe(true);
  expect(h.frames.size).toBe(0);
  expect(h.observers).toHaveLength(0);
  expect(h.contexts).toHaveLength(0);
  expect(asked).toBe(0);
  expect(h.body.style.transform).toBeUndefined();
  stop();
  expect(h.el.classList.contains("is-still")).toBe(false);
});
