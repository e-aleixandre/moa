import { hash } from "./OwnerAvatar.jsx";

// faceMotion — the one clock every animated owner face shares.
//
// WHY AN EVENT SCHEDULER AND NOT KEYFRAMES. A face spends >95% of its life
// holding still: it blinks for ~150 ms every few seconds and moves its gaze
// every couple of seconds. Infinite CSS keyframes would keep the compositor
// producing frames for every face all the time, and a per-frame rAF loop would
// wake the main thread 60 times a second for nothing. Here there is ONE
// setTimeout, armed to the next event due across every visible face; the event
// flips a class or a few custom properties and CSS transitions do the tween.
// Between events nothing animates and no frame is rendered.
//
// STATE. The owner asked for behaviour by state, as a complement to the words
// that still say it (the face never replaces them). So the script has a MODE:
//   idle     — looks around, dwells, blinks; the component also breathes.
//   working  — concentrated: small quick saccades around a point low and to
//              the side, and fewer blinks (people blink less when focused).
//   asks     — looks at you and holds it; every few seconds a small nudge.
//   calm     — Sobria's conservative script, whatever the state.
// Saved has no script at all: the face is resting and does not move.
//
// DETERMINISM. Everything is seeded from the owner's codebase_key through the
// avatar's own FNV hash (with a seed of its own, so personality does not march
// in lockstep with shape or colour). Same owner → same personality → the same
// sequence of blinks and glances on every load and every machine. Real time
// only decides WHEN the next step of that sequence plays.

const PERSONALITY_SEED = 0x2545f491;

export const BLINK_MS = 150;
// How long a "waiting for you" nudge holds before it springs back.
export const NUDGE_MS = 240;
// The gap between the two blinks of a double blink, counted from the moment
// the first one reopens.
export const DOUBLE_GAP_MS = 110;
// How long the pointer keeps a following face's attention after it stops
// moving; then the face goes back to its own script.
export const FOLLOW_HOLD_MS = 2000;
// Events due within this window of the one that woke the timer are played in
// the same task. Each event is a style invalidation; a page of faces firing
// them 5 ms apart paid one style recalc each. 48 ms early is invisible.
export const COALESCE_MS = 48;

export const EYE_STYLES = ["lens", "capsule", "round"];

// mulberry32 — tiny, fast and good enough to make a face look random; what
// matters is that it is seedable and identical on every engine.
export function mulberry32(seed) {
  let a = seed >>> 0;
  return function next() {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

const round2 = (n) => Math.round(n * 100) / 100;

// facePersonality derives the owner's temperament. The ranges are narrow on
// purpose: every face must still read as calm in a list looked at for hours;
// the personality is in the rhythm, not in how much it moves.
export function facePersonality(seedKey) {
  const seed = hash(String(seedKey || ""), PERSONALITY_SEED);
  const r = mulberry32(seed);
  return {
    seed,
    blinkMean: Math.round(2500 + r() * 3500), // ms between blinks, before jitter
    doubleBlink: round2(0.06 + r() * 0.22), // chance a blink comes in a pair
    dwellMean: Math.round(1400 + r() * 3200), // ms the gaze rests (restlessness⁻¹)
    gazeRange: round2(0.36 + r() * 0.24), // how far from centre it wanders, 0..1
    centerBias: round2(0.25 + r() * 0.35), // chance the next glance is "back to you"
    eyeStyle: EYE_STYLES[Math.floor(r() * EYE_STYLES.length)],
    tilt: Math.round((r() * 2 - 1) * 10), // degrees, the pair's lean (Mirada)
    eyeGap: round2(4.1 + r() * 0.9), // half the distance between the eyes
    pupil: round2(1.45 + r() * 0.4), // Pupilas' pupil radius
    sclera: r() < 0.5 ? "round" : "tall",
    // Appended last so adding them did not reshuffle the traits above.
    breath: Math.round(4800 + r() * 2400), // ms per breath, idle only
    nudgeEvery: Math.round(2600 + r() * 1600), // ms between "your turn" nudges
    strokeLen: round2(3.4 + r() * 1.4), // Mirada's eye stroke length
    // Each stroke leans its own way around the pair's tilt: the asymmetry is
    // where the expression comes from (a level pair reads as a glyph).
    // Biased towards tops apart (friendly); tops together reads as a frown, so
    // it is allowed only mildly.
    skew: Math.round(-5 + r() * 17),
  };
}

// motionScript is the owner's endless, deterministic sequence of moves. Two
// independent streams, so how often it blinks never depends on how often it
// has looked around (and pausing one cannot shift the other).
export const MOTION_MODES = ["idle", "working", "asks", "calm"];

export function motionScript(p, { mode = "idle", calm = false } = {}) {
  if (calm) mode = "calm";
  const br = mulberry32(p.seed ^ 0xb1b1b1b1);
  const gr = mulberry32(p.seed ^ 0x6a2e1f37);
  const nr = mulberry32(p.seed ^ 0x3c6ef372);
  const blinkMean = p.blinkMean * (mode === "working" ? 1.5 : 1);
  let away = false;
  let gx = 0;
  let gy = 0;
  return {
    blink() {
      // 45%–155% of the mean, and now and then a long pause: a metronome
      // blink is the one thing that reads as mechanical.
      let d = blinkMean * (0.45 + br() * 1.1);
      if (br() < 0.12) d *= 1.8;
      return { delay: Math.round(d), double: br() < p.doubleBlink };
    },
    // nudge is the delay to the next "your turn" nudge, or null when this
    // mode has none.
    nudge() {
      if (mode !== "asks") return null;
      return Math.round(p.nudgeEvery * (0.7 + nr() * 0.6));
    },
    // gaze is the next move, or null when the eyes hold still (asks: it is
    // looking at you, and that is the whole message).
    gaze() {
      if (mode === "asks") return null;
      let nx;
      let ny;
      let delay;
      if (mode === "working") {
        // Saccades: short hops around the point it is working on, reading
        // something rather than looking around. Offsets from the working rest.
        nx = (gr() - 0.5) * 0.5;
        ny = (gr() - 0.5) * 0.3;
        delay = 350 + gr() * 800;
        gx = nx;
        gy = ny;
        return { delay: Math.round(delay), gx: round2(nx), gy: round2(ny), blink: false };
      }
      if (mode === "calm") {
        // Sobria: long rests looking at you, broken by a short glance that
        // comes straight back.
        if (away) {
          nx = 0;
          ny = 0;
          delay = 500 + gr() * 700;
        } else {
          const a = gr() * Math.PI * 2;
          const m = p.gazeRange * (0.6 + gr() * 0.4);
          nx = Math.cos(a) * m;
          ny = Math.sin(a) * m * 0.6;
          delay = p.dwellMean * (1.4 + gr() * 1.6);
        }
        away = !away;
      } else if (away && gr() < p.centerBias) {
        nx = 0;
        ny = 0;
        delay = p.dwellMean * (0.4 + gr() * 1.2);
        away = false;
      } else {
        const a = gr() * Math.PI * 2;
        const m = p.gazeRange * (0.45 + gr() * 0.55);
        nx = Math.cos(a) * m;
        ny = Math.sin(a) * m * 0.6;
        delay = p.dwellMean * (0.4 + gr() * 1.2);
        away = true;
      }
      // A big saccade is often accompanied by a blink, which is what makes a
      // move read as a glance rather than as a slide.
      const moved = Math.hypot(nx - gx, ny - gy);
      const blink = moved > p.gazeRange * 0.6 && gr() < 0.35;
      gx = nx;
      gy = ny;
      return { delay: Math.round(delay), gx: round2(nx), gy: round2(ny), blink };
    },
  };
}

// createFaceScheduler — the pure core, with the clock and the timer injected
// so tests can drive it without a DOM.
//
// env: { now(), setTimeout(fn, ms), clearTimeout(id), hidden?, reduced? }
export function createFaceScheduler(env) {
  const faces = new Set();
  let timer = null;
  let timerAt = Infinity;
  let frozen = false;
  let hidden = !!env.hidden;
  let systemReduced = !!env.reduced;
  let forcedReduced = false;

  const reduced = () => systemReduced || forcedReduced;
  const running = () => !frozen && !hidden && !reduced();
  const active = (f) => f.visible && running();

  // live says whether the face may run its continuous CSS (the idle breath):
  // only while it is on screen and the clock is running.
  function setLive(f) {
    const v = active(f);
    if (f.live === v) return;
    f.live = v;
    f.apply({ live: v });
  }

  function endNudge(f) {
    f.nudgeEnd = Infinity;
    f.apply({ nudge: false });
  }

  function drawNudge(f, t) {
    const d = f.script.nudge();
    f.nextNudge = d == null ? Infinity : t + d;
  }

  function fireBlink(f, t, scripted) {
    f.blinking = true;
    f.blinkEnd = t + BLINK_MS;
    if (scripted) {
      f.bonus = f.nextIsDouble ? 1 : 0;
      f.nextBlink = Infinity;
    }
    f.apply({ blink: true });
  }

  function endBlink(f, t) {
    f.blinking = false;
    f.blinkEnd = Infinity;
    f.apply({ blink: false });
    if (f.bonus > 0) {
      f.bonus -= 1;
      f.nextBlink = t + DOUBLE_GAP_MS;
      f.nextIsDouble = false;
    } else if (f.nextBlink === Infinity) {
      drawBlink(f, t);
    }
  }

  function drawBlink(f, t) {
    const b = f.script.blink();
    f.nextBlink = t + b.delay;
    f.nextIsDouble = b.double;
  }

  function drawGaze(f, t) {
    const g = f.script.gaze();
    f.pending = g;
    f.nextGaze = g ? t + g.delay : Infinity;
  }

  function step(f, t, h = t) {
    if (f.blinkEnd <= h) endBlink(f, t);
    if (f.nextBlink <= h) {
      if (f.blinking) f.nextBlink = f.blinkEnd + 60;
      else fireBlink(f, t, true);
    }
    if (f.nudgeEnd <= h) endNudge(f);
    if (f.nextNudge <= h) {
      f.nudgeEnd = t + NUDGE_MS;
      f.apply({ nudge: true });
      drawNudge(f, t);
    }
    if (f.nextGaze <= h) {
      if (t < f.followUntil) {
        // The pointer has the face's attention; the script waits for it.
        f.nextGaze = f.followUntil;
      } else {
        const g = f.pending;
        f.apply({ gx: g.gx, gy: g.gy });
        if (g.blink && !f.blinking) fireBlink(f, t, false);
        drawGaze(f, t);
      }
    }
  }

  function due(f) {
    return Math.min(f.nextBlink, f.blinkEnd, f.nextGaze, f.nextNudge, f.nudgeEnd);
  }

  function clear() {
    if (timer !== null) env.clearTimeout(timer);
    timer = null;
    timerAt = Infinity;
  }

  function arm() {
    if (!running()) return clear();
    let next = Infinity;
    for (const f of faces) if (active(f)) next = Math.min(next, due(f));
    if (next === Infinity) return clear();
    if (next === timerAt && timer !== null) return;
    clear();
    timerAt = next;
    timer = env.setTimeout(run, Math.max(0, next - env.now()));
  }

  function run() {
    timer = null;
    timerAt = Infinity;
    const t = env.now();
    for (const f of faces) if (active(f)) step(f, t, t + COALESCE_MS);
    arm();
  }

  // wake resumes a face that was offscreen, paused or hidden. Whatever fell
  // due while it was away is NOT replayed — a burst of catch-up blinks on
  // scrolling back is exactly the twitch this is meant to avoid.
  function wake(f, t) {
    if (f.blinking) endBlink(f, t);
    if (f.nudgeEnd < Infinity) endNudge(f);
    if (f.nextBlink < t) drawBlink(f, t);
    if (f.nextGaze < t) f.nextGaze = t + 400;
    if (f.nextNudge < t) drawNudge(f, t);
  }

  function wakeAll() {
    const t = env.now();
    for (const f of faces) if (active(f)) wake(f, t);
    for (const f of faces) setLive(f);
    arm();
  }

  function settle(resetGaze) {
    for (const f of faces) {
      if (f.blinking) {
        f.blinking = false;
        f.blinkEnd = Infinity;
        f.apply({ blink: false });
      }
      if (f.nudgeEnd < Infinity) endNudge(f);
      setLive(f);
      if (resetGaze) f.apply({ gx: 0, gy: 0 });
    }
  }

  function setMode(change) {
    const wasRunning = running();
    const wasReduced = reduced();
    change();
    if (reduced() && !wasReduced) settle(true);
    if (!running() && wasRunning) {
      clear();
      if (!reduced()) settle(false);
    } else if (running() && !wasRunning) wakeAll();
  }

  return {
    // register takes { seedKey | personality, mode, calm, follow, apply } and returns
    // the face's handle. A face starts invisible: the IntersectionObserver (or
    // the test) says when it is on screen.
    register(spec) {
      const personality = spec.personality || facePersonality(spec.seedKey);
      const f = {
        spec,
        personality,
        script: motionScript(personality, { mode: spec.mode, calm: !!spec.calm }),
        apply: spec.apply,
        follow: !!spec.follow,
        visible: false,
        blinking: false,
        blinkEnd: Infinity,
        nextBlink: Infinity,
        nextIsDouble: false,
        bonus: 0,
        nextGaze: Infinity,
        pending: null,
        followUntil: -Infinity,
        nextNudge: Infinity,
        nudgeEnd: Infinity,
        live: false,
      };
      const t = env.now();
      drawBlink(f, t);
      drawGaze(f, t);
      drawNudge(f, t);
      faces.add(f);
      const handle = {
        face: f,
        setVisible(v) {
          if (f.visible === !!v) return;
          f.visible = !!v;
          if (f.visible && running()) wake(f, env.now());
          if (!f.visible && f.nudgeEnd < Infinity) endNudge(f);
          setLive(f);
          arm();
        },
        // look points the face at something (the pointer) for FOLLOW_HOLD_MS.
        look(gx, gy) {
          if (!active(f) || !f.follow) return false;
          f.followUntil = env.now() + FOLLOW_HOLD_MS;
          f.apply({ gx, gy });
          arm();
          return true;
        },
        unregister() {
          faces.delete(f);
          arm();
        },
      };
      arm();
      return handle;
    },
    setFrozen(v) { setMode(() => { frozen = !!v; }); },
    setHidden(v) { setMode(() => { hidden = !!v; }); },
    setSystemReduced(v) { setMode(() => { systemReduced = !!v; }); },
    setForcedReduced(v) { setMode(() => { forcedReduced = !!v; }); },
    isRunning: running,
    isReduced: reduced,
    // followers are the faces the pointer may steer right now; the DOM glue
    // listens to the pointer only while there is at least one.
    followers() {
      const out = [];
      for (const f of faces) if (f.follow && active(f)) out.push(f);
      return out;
    },
    size: () => faces.size,
    timerArmed: () => timer !== null,
  };
}

/* ── The DOM glue ─────────────────────────────────────────────────────────
   One scheduler per document, created on first use, with ONE of each
   listener: an IntersectionObserver for every face, visibilitychange, the
   reduced-motion query, and a pointermove that only exists while a visible
   following face does. */

let shared = null;

export function faceMotion() {
  if (shared) return shared;
  if (typeof window === "undefined") return null;

  const rm = window.matchMedia?.("(prefers-reduced-motion: reduce)");
  const fine = window.matchMedia?.("(pointer: fine)");
  const listeners = new Set();
  const core = createFaceScheduler({
    now: () => performance.now(),
    setTimeout: (fn, ms) => window.setTimeout(fn, ms),
    clearTimeout: (id) => window.clearTimeout(id),
    hidden: document.hidden,
    reduced: !!rm?.matches,
  });
  const notify = () => { for (const fn of listeners) fn(); syncPointer(); };

  document.addEventListener("visibilitychange", () => core.setHidden(document.hidden));
  rm?.addEventListener?.("change", (e) => { core.setSystemReduced(e.matches); notify(); });

  const byEl = new Map();
  const io = typeof IntersectionObserver === "function"
    ? new IntersectionObserver((entries) => {
      for (const e of entries) byEl.get(e.target)?.setVisible(e.isIntersecting);
      syncPointer();
    }, { rootMargin: "48px" })
    : null;

  let px = 0;
  let py = 0;
  let raf = 0;
  let listening = false;
  function frame() {
    raf = 0;
    for (const f of core.followers()) {
      const r = f.spec.el.getBoundingClientRect();
      const dx = px - (r.left + r.width / 2);
      const dy = py - (r.top + r.height / 2);
      const d = Math.hypot(dx, dy);
      // Full deflection a few face-widths away; close to the face the eyes
      // ease back towards you instead of snapping across it.
      const k = d < 1 ? 0 : Math.min(1, d / (r.width * 2 + 120));
      const handle = byEl.get(f.spec.el);
      handle?.look(d < 1 ? 0 : (dx / d) * k, d < 1 ? 0 : (dy / d) * k);
    }
  }
  function onMove(e) {
    px = e.clientX;
    py = e.clientY;
    if (!raf) raf = requestAnimationFrame(frame);
  }
  function syncPointer() {
    const want = !!fine?.matches && core.followers().length > 0;
    if (want && !listening) window.addEventListener("pointermove", onMove, { passive: true });
    if (!want && listening) window.removeEventListener("pointermove", onMove);
    listening = want;
  }

  shared = {
    core,
    register(spec) {
      const handle = core.register(spec);
      byEl.set(spec.el, handle);
      if (io) io.observe(spec.el);
      else handle.setVisible(true);
      return () => {
        io?.unobserve(spec.el);
        byEl.delete(spec.el);
        handle.unregister();
        syncPointer();
      };
    },
    setFrozen(v) { core.setFrozen(v); notify(); },
    setForcedReduced(v) { core.setForcedReduced(v); notify(); },
    isReduced: () => core.isReduced(),
    subscribe(fn) { listeners.add(fn); return () => listeners.delete(fn); },
  };
  return shared;
}
