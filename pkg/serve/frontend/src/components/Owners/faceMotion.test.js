// faceMotion.test.js — the animated owner face's clock (lab candidate).
//
// What has to hold: a face is the same creature on every load (personality and
// script are a function of codebase_key alone), the two Winerim owners are not
// twins, and the scheduler costs nothing when nobody can see the faces —
// reduced motion, a hidden tab, or a face scrolled offscreen.

import { expect, test } from "bun:test";
import {
  BLINK_MS, NUDGE_MS, createFaceScheduler, facePersonality, motionScript,
} from "./faceMotion.js";
import { combineGaze, poseVars } from "./OwnerFace.jsx";
import { AVATAR_SHAPES, DEFAULT_AVATAR_SHAPES, defaultAvatar, ownerAvatar } from "./avatar-identity.js";

// A fake clock: timers fire only when the test advances time.
function fakeEnv(over = {}) {
  let now = 0;
  let seq = 0;
  const timers = new Map();
  const env = {
    now: () => now,
    setTimeout(fn, ms) { const id = ++seq; timers.set(id, { fn, at: now + ms }); return id; },
    clearTimeout(id) { timers.delete(id); },
    ...over,
  };
  env.advance = (ms) => {
    const end = now + ms;
    for (;;) {
      let next = null;
      for (const [id, t] of timers) if (t.at <= end && (!next || t.at < next[1].at)) next = [id, t];
      if (!next) break;
      timers.delete(next[0]);
      now = next[1].at;
      next[1].fn();
    }
    now = end;
  };
  env.pending = () => timers.size;
  return env;
}

function recorder() {
  const events = [];
  return { events, apply: (ev) => events.push(ev) };
}

const script = (key, n, calm, mode) => {
  const s = motionScript(facePersonality(key), { calm, mode });
  return Array.from({ length: n }, () => [s.blink(), s.gaze()]);
};

test("the same key is the same personality and the same script", () => {
  expect(facePersonality("winerim-backend")).toEqual(facePersonality("winerim-backend"));
  expect(script("moa", 40)).toEqual(script("moa", 40));
  expect(script("moa", 40, true)).toEqual(script("moa", 40, true));
});

test("different keys are different creatures", () => {
  const keys = ["moa", "facturas-api", "catas-app", "etiquetas-pdf", "landing-2026", "sommelier-bot"];
  const seeds = new Set(keys.map((k) => facePersonality(k).seed));
  expect(seeds.size).toBe(keys.length);
  expect(script("moa", 10)).not.toEqual(script("catas-app", 10));
});

test("the Winerim pair do not blink or look around alike", () => {
  const a = facePersonality("winerim-backend");
  const b = facePersonality("winerim-web");
  expect(a.seed).not.toBe(b.seed);
  expect(a.blinkMean).not.toBe(b.blinkMean);
  expect(a.dwellMean).not.toBe(b.dwellMean);
  expect(script("winerim-backend", 10)).not.toEqual(script("winerim-web", 10));
});

test("personalities stay inside their ranges", () => {
  for (let i = 0; i < 500; i++) {
    const p = facePersonality(`k${i}`);
    expect(p.blinkMean).toBeGreaterThanOrEqual(2500);
    expect(p.blinkMean).toBeLessThanOrEqual(6000);
    expect(p.gazeRange).toBeLessThanOrEqual(0.6);
    const s = motionScript(p);
    for (let j = 0; j < 20; j++) {
      const g = s.gaze();
      expect(Math.abs(g.gx)).toBeLessThanOrEqual(0.6);
      expect(Math.abs(g.gy)).toBeLessThanOrEqual(0.6);
      expect(g.delay).toBeGreaterThan(0);
    }
  }
});

test("a visible face blinks, reopens after BLINK_MS and moves its gaze", () => {
  const env = fakeEnv();
  const sch = createFaceScheduler(env);
  const r = recorder();
  const h = sch.register({ seedKey: "moa", apply: r.apply });
  h.setVisible(true);
  env.advance(30000);
  const blinks = r.events.filter((e) => e.blink === true).length;
  const opens = r.events.filter((e) => e.blink === false).length;
  const gazes = r.events.filter((e) => "gx" in e).length;
  expect(blinks).toBeGreaterThan(3);
  expect(opens).toBe(blinks);
  expect(gazes).toBeGreaterThan(3);
  expect(BLINK_MS).toBeLessThan(200);
});

test("two loads of the same owner play the same sequence", () => {
  const run = () => {
    const env = fakeEnv();
    const r = recorder();
    createFaceScheduler(env).register({ seedKey: "winerim-web", apply: r.apply }).setVisible(true);
    env.advance(20000);
    return r.events;
  };
  expect(run()).toEqual(run());
});

test("reduced motion: no events and no timer", () => {
  const env = fakeEnv({ reduced: true });
  const sch = createFaceScheduler(env);
  const r = recorder();
  sch.register({ seedKey: "moa", apply: r.apply }).setVisible(true);
  env.advance(60000);
  expect(r.events).toEqual([]);
  expect(sch.timerArmed()).toBe(false);
});

test("turning reduced motion on mid-flight settles the face and stops the clock", () => {
  const env = fakeEnv();
  const sch = createFaceScheduler(env);
  const r = recorder();
  sch.register({ seedKey: "moa", apply: r.apply }).setVisible(true);
  env.advance(10000);
  sch.setForcedReduced(true);
  const at = r.events.length;
  // The settle: eyes forward, never left shut.
  expect(r.events.slice(at - 1)).toContainEqual({ gx: 0, gy: 0 });
  env.advance(60000);
  expect(r.events.length).toBe(at);
  expect(sch.timerArmed()).toBe(false);
  sch.setForcedReduced(false);
  env.advance(20000);
  expect(r.events.length).toBeGreaterThan(at);
});

test("offscreen faces are skipped and do not keep the timer alive", () => {
  const env = fakeEnv();
  const sch = createFaceScheduler(env);
  const seen = recorder();
  const off = recorder();
  sch.register({ seedKey: "moa", apply: seen.apply }).setVisible(true);
  sch.register({ seedKey: "catas-app", apply: off.apply });
  env.advance(30000);
  expect(seen.events.length).toBeGreaterThan(0);
  expect(off.events).toEqual([]);

  const only = createFaceScheduler(fakeEnv());
  only.register({ seedKey: "moa", apply: () => {} });
  expect(only.timerArmed()).toBe(false);
});

test("scrolling back in does not replay what fell due while away", () => {
  const env = fakeEnv();
  const sch = createFaceScheduler(env);
  const r = recorder();
  const h = sch.register({ seedKey: "moa", apply: r.apply });
  h.setVisible(true);
  env.advance(5000);
  h.setVisible(false);
  const at = r.events.length;
  env.advance(120000);
  expect(r.events.length).toBe(at);
  h.setVisible(true);
  env.advance(50);
  // At most the reopening of a lid left shut; never a burst of blinks.
  expect(r.events.slice(at).filter((e) => e.blink === true)).toEqual([]);
});

test("a hidden document stops everything; frozen keeps the pose", () => {
  const env = fakeEnv({ hidden: true });
  const sch = createFaceScheduler(env);
  const r = recorder();
  sch.register({ seedKey: "moa", apply: r.apply }).setVisible(true);
  env.advance(30000);
  expect(r.events).toEqual([]);
  sch.setHidden(false);
  env.advance(30000);
  const at = r.events.length;
  expect(at).toBeGreaterThan(0);
  sch.setFrozen(true);
  const frozenAt = r.events.length;
  // Freezing may reopen a lid and stop the breath; it never moves the gaze.
  expect(r.events.slice(at).every((e) => e.blink === false || e.live === false)).toBe(true);
  env.advance(30000);
  expect(r.events.length).toBe(frozenAt);
});

test("pointer follow holds the script off, then hands back", () => {
  const env = fakeEnv();
  const sch = createFaceScheduler(env);
  const r = recorder();
  const h = sch.register({ seedKey: "moa", follow: true, apply: r.apply });
  h.setVisible(true);
  expect(sch.followers().length).toBe(1);
  expect(h.look(0.9, 0.1)).toBe(true);
  const at = r.events.length;
  env.advance(1900);
  expect(r.events.slice(at).filter((e) => "gx" in e)).toEqual([]);
  env.advance(20000);
  expect(r.events.slice(at).filter((e) => "gx" in e).length).toBeGreaterThan(0);

  const plain = sch.register({ seedKey: "catas-app", apply: () => {} });
  plain.setVisible(true);
  expect(plain.look(1, 0)).toBe(false);
  sch.setForcedReduced(true);
  expect(sch.followers()).toEqual([]);
});

test("state poses: working rests low and aside, asks is pinned on you", () => {
  const p = facePersonality("moa");
  expect(combineGaze("idle", 0, 0)).toEqual([0, 0]);
  // Working saccades stay around the working rest: never back to centre.
  const w = motionScript(p, { mode: "working" });
  for (let i = 0; i < 200; i++) {
    const g = w.gaze();
    const [x, y] = combineGaze("working", g.gx, g.gy);
    expect(x).toBeGreaterThan(0.25);
    expect(y).toBeGreaterThan(0.25);
  }
  // Waiting for you is pinned on you whatever the script says.
  expect(combineGaze("asks", 0.6, -0.6)).toEqual([0, 0]);
  // The head turn: the far eye is the foreshortened one.
  const v = poseVars(p, "circle", 1, 0);
  expect(v["--rs"]).toBeLessThan(v["--ls"]);
  expect(v["--rs"]).toBeLessThan(0.7);
});
test("each state has its own deterministic script", () => {
  for (const mode of ["idle", "working", "asks"]) {
    expect(script("moa", 20, false, mode)).toEqual(script("moa", 20, false, mode));
  }
  const p = facePersonality("moa");
  const idle = motionScript(p, { mode: "idle" });
  const work = motionScript(p, { mode: "working" });
  const mean = (s) => Array.from({ length: 50 }, () => s.gaze().delay).reduce((a, b) => a + b) / 50;
  // Working is concentrated: its gaze hops come much faster than idle's.
  expect(mean(work)).toBeLessThan(mean(idle) / 1.5);
  const asks = motionScript(p, { mode: "asks" });
  expect(asks.gaze()).toBeNull();
  expect(asks.nudge()).toBeGreaterThan(1000);
  expect(idle.nudge()).toBeNull();
});

test("waiting for you nudges and holds the gaze; idle never nudges", () => {
  const env = fakeEnv();
  const sch = createFaceScheduler(env);
  const asks = recorder();
  const idle = recorder();
  sch.register({ seedKey: "moa", mode: "asks", apply: asks.apply }).setVisible(true);
  sch.register({ seedKey: "moa", mode: "idle", apply: idle.apply }).setVisible(true);
  env.advance(30000);
  expect(asks.events.filter((e) => "gx" in e)).toEqual([]);
  const on = asks.events.filter((e) => e.nudge === true).length;
  expect(on).toBeGreaterThan(4);
  expect(asks.events.filter((e) => e.nudge === false).length).toBe(on);
  expect(idle.events.some((e) => "nudge" in e)).toBe(false);
  expect(NUDGE_MS).toBeLessThan(400);
});

test("live (the breath) is on only while the face is visible and running", () => {
  const env = fakeEnv();
  const sch = createFaceScheduler(env);
  const r = recorder();
  const h = sch.register({ seedKey: "moa", apply: r.apply });
  const lives = () => r.events.filter((e) => "live" in e).map((e) => e.live);
  expect(lives()).toEqual([]);
  h.setVisible(true);
  sch.setFrozen(true);
  sch.setFrozen(false);
  h.setVisible(false);
  sch.setForcedReduced(true);
  h.setVisible(true);
  expect(lives()).toEqual([true, false, true, false]);
});

test("triangle and cloud are selectable but never a default; the pool is unchanged", () => {
  expect(DEFAULT_AVATAR_SHAPES).toEqual(["circle", "squircle", "blob", "hexagon", "drop", "pill"]);
  expect(AVATAR_SHAPES).toEqual([...DEFAULT_AVATAR_SHAPES, "triangle", "cloud"]);
  for (let i = 0; i < 2000; i++) {
    expect(DEFAULT_AVATAR_SHAPES).toContain(defaultAvatar(`k${i}`).shape);
  }
  // Golden, computed before the two shapes existed (pkg/owner/avatar_test.go
  // holds the same values): no owner that never chose changes face.
  expect(defaultAvatar("winerim-backend")).toEqual({ shape: "blob", color: "mint" });
  expect(defaultAvatar("etiquetas-pdf")).toEqual({ shape: "pill", color: "peach" });
  expect(defaultAvatar("")).toEqual({ shape: "squircle", color: "sage" });
  // A stored opt-in shape is honoured, not sent back to the default.
  for (const shape of ["triangle", "cloud"]) {
    expect(ownerAvatar({ codebase_key: "moa", avatar: { shape, color: "sky" } })).toEqual({ shape, color: "sky" });
  }
  expect(ownerAvatar({ codebase_key: "moa", avatar: { shape: "star", color: "sky" } }).shape).toBe("circle");
});
test("each eye stroke leans its own way for most owners", () => {
  let asymmetric = 0;
  for (let i = 0; i < 200; i++) if (facePersonality(`k${i}`).skew !== 0) asymmetric++;
  expect(asymmetric).toBeGreaterThan(180);
});

test("events due close together are played in one task", () => {
  let wakes = 0;
  const env = fakeEnv();
  const inner = env.setTimeout;
  env.setTimeout = (fn, ms) => inner(() => { wakes++; fn(); }, ms);
  const sch = createFaceScheduler(env);
  let events = 0;
  for (let i = 0; i < 40; i++) {
    sch.register({ seedKey: `k${i}`, apply: () => { events++; } }).setVisible(true);
  }
  env.advance(30000);
  expect(events).toBeGreaterThan(0);
  // Without coalescing every event wakes the timer on its own.
  expect(wakes).toBeLessThan(events * 0.75);
});
