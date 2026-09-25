// aurora-wash.js — the ambient wash under the composer while the voice is
// live (dictating or on a call). The DOM and the CSS are ComposerAurora's;
// this is the part that moves them, kept free of Preact so it can be tested
// with a fake environment.
//
// Cost rules, measured in the design lab (?view=composer-wave on
// design/composer-wave): the clouds drift on CSS keyframes, which the
// compositor runs; JavaScript only sets one transform and one opacity, at most
// 30 times a second. Everything stops while the tab is hidden or the composer
// is off screen, and reduced motion is a still picture with no loop and no
// audio graph at all.

const FRAME_MS = 1000 / 30;
// Speech RMS sits around 0.02-0.2; the square root lifts the quiet end.
const NOISE_FLOOR = 0.004;
const GAIN = 7;
const ATTACK_S = 0.05;
const RELEASE_S = 0.35;

// createLevelMeter reads a microphone stream through an AnalyserNode. It never
// stops the stream's tracks: the recorder or the call that opened it owns them.
export function createLevelMeter(stream, AudioContextCtor) {
  if (!stream || !AudioContextCtor) return null;
  let ctx;
  try {
    ctx = new AudioContextCtor();
    const source = ctx.createMediaStreamSource(stream);
    const analyser = ctx.createAnalyser();
    analyser.fftSize = 512;
    source.connect(analyser);
    const buf = new Float32Array(analyser.fftSize);
    // A context made after the tap may start suspended; the stream is already
    // live, so asking is enough where the browser allows it.
    ctx.resume?.()?.catch?.(() => {});
    return {
      read() {
        analyser.getFloatTimeDomainData(buf);
        let sum = 0;
        for (let i = 0; i < buf.length; i++) sum += buf[i] * buf[i];
        const rms = Math.sqrt(sum / buf.length);
        return Math.min(1, Math.sqrt(Math.max(0, rms - NOISE_FLOOR) * GAIN));
      },
      dispose() { ctx.close?.()?.catch?.(() => {}); },
    };
  } catch {
    ctx?.close?.()?.catch?.(() => {});
    return null;
  }
}

function defaultEnv() {
  const g = globalThis;
  return {
    document: g.document,
    requestAnimationFrame: g.requestAnimationFrame?.bind(g),
    cancelAnimationFrame: g.cancelAnimationFrame?.bind(g),
    IntersectionObserver: g.IntersectionObserver,
    AudioContext: g.AudioContext || g.webkitAudioContext,
  };
}

// startAuroraWash animates `el` (the wash) and its `body` (the clouds' parent)
// from the voice level of getStream()'s microphone. Returns the teardown.
export function startAuroraWash(el, body, { getStream, reduced = false, env = defaultEnv() } = {}) {
  if (reduced) {
    el.classList.add('is-still');
    return () => el.classList.remove('is-still');
  }

  const { document: doc, requestAnimationFrame: raf, cancelAnimationFrame: caf } = env;
  let frame = 0;
  let lastDraw = -Infinity;
  let lastTick = 0;
  let level = 0;
  let meter = null;
  let meterStream = null;
  let onScreen = true;
  let stopped = false;

  const draw = (now) => {
    frame = 0;
    if (stopped) return;
    frame = raf(draw);
    if (now - lastDraw < FRAME_MS - 2) return;
    const dt = Math.min(0.1, lastTick ? (now - lastTick) / 1000 : FRAME_MS / 1000);
    lastDraw = lastTick = now;
    // Follow whichever microphone is live now: dictation and a call can
    // overlap, and either may end first.
    const stream = getStream?.() || null;
    if (stream !== meterStream) {
      meter?.dispose();
      meter = createLevelMeter(stream, env.AudioContext);
      meterStream = stream;
    }
    const target = meter ? meter.read() : 0;
    const k = 1 - Math.exp(-dt / (target > level ? ATTACK_S : RELEASE_S));
    level += (target - level) * k;
    body.style.transform = `scaleY(${(0.5 + 0.8 * level).toFixed(3)})`;
    body.style.opacity = (0.5 + 0.5 * level).toFixed(3);
  };

  const running = () => frame !== 0;
  const sync = () => {
    const visible = onScreen && !doc?.hidden;
    el.classList.toggle('is-paused', !visible);
    if (visible && !running() && raf) {
      lastTick = 0;
      frame = raf(draw);
    } else if (!visible && running()) {
      caf?.(frame);
      frame = 0;
    }
  };

  const io = env.IntersectionObserver
    ? new env.IntersectionObserver((entries) => {
      onScreen = entries[entries.length - 1].isIntersecting;
      sync();
    })
    : null;
  io?.observe(el);
  doc?.addEventListener?.('visibilitychange', sync);
  sync();

  return () => {
    stopped = true;
    if (frame) caf?.(frame);
    frame = 0;
    io?.disconnect();
    doc?.removeEventListener?.('visibilitychange', sync);
    meter?.dispose();
    meter = null;
  };
}
