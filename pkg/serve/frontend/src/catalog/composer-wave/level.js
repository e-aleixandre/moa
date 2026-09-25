// The voice level the effects react to, 0..1, read once per frame.
//
// "fake" is a believable speaker without a microphone: phrases of a few
// seconds separated by breaths, syllables at ~4-5 Hz inside a phrase, each
// syllable its own loudness. Deterministic, so every composer on the page and
// every recording of the lab breathes the same way.
// "mic" is the real microphone through an AnalyserNode (RMS of the waveform).
// Both are smoothed with a fast attack and a slow release, the way a VU meter
// reads, so an effect never flickers with the raw signal.

function mulberry32(seed) {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

// A fixed script of phrases and pauses, looped: [start, end, loudness].
function buildScript(seed = 7, length = 40) {
  const rnd = mulberry32(seed);
  const phrases = [];
  let t = 0.6;
  while (t < length) {
    const dur = 1.2 + rnd() * 2.8;
    phrases.push([t, t + dur, 0.55 + rnd() * 0.45, rnd() * 10]);
    t += dur + 0.35 + rnd() * 1.1;
  }
  return { phrases, length: t };
}

const SCRIPT = buildScript();

export function fakeVoice(seconds) {
  const t = seconds % SCRIPT.length;
  for (const [a, b, loud, ph] of SCRIPT.phrases) {
    if (t < a || t > b) continue;
    const u = (t - a) / (b - a);
    const shape = Math.min(1, u * 8) * Math.min(1, (1 - u) * 5);
    const syl = 0.5 + 0.5 * Math.sin((t + ph) * 2 * Math.PI * 4.3);
    const syl2 = 0.5 + 0.5 * Math.sin((t + ph) * 2 * Math.PI * 1.7 + 1.3);
    return Math.min(1, loud * shape * (0.35 + 0.45 * syl * syl + 0.2 * syl2));
  }
  return 0.02;
}

class LevelSource {
  constructor() {
    this.mode = "fake";
    this.value = 0;
    this.last = 0;
    this.analyser = null;
    this.buf = null;
    this.stream = null;
    this.ctx = null;
  }

  async useMic() {
    if (!navigator.mediaDevices?.getUserMedia) throw new Error("This browser has no microphone API here (needs HTTPS).");
    this.stream = await navigator.mediaDevices.getUserMedia({ audio: { echoCancellation: true, noiseSuppression: true } });
    this.ctx = new AudioContext();
    const src = this.ctx.createMediaStreamSource(this.stream);
    this.analyser = this.ctx.createAnalyser();
    this.analyser.fftSize = 512;
    this.buf = new Float32Array(this.analyser.fftSize);
    src.connect(this.analyser);
    this.mode = "mic";
  }

  useFake() {
    this.stream?.getTracks().forEach((t) => t.stop());
    this.ctx?.close();
    this.stream = this.ctx = this.analyser = null;
    this.mode = "fake";
  }

  raw(now) {
    if (this.mode === "mic" && this.analyser) {
      this.analyser.getFloatTimeDomainData(this.buf);
      let sum = 0;
      for (let i = 0; i < this.buf.length; i++) sum += this.buf[i] * this.buf[i];
      const rms = Math.sqrt(sum / this.buf.length);
      // Speech sits around 0.02-0.2 RMS; a square root lifts the quiet end.
      return Math.min(1, Math.sqrt(Math.max(0, rms - 0.004) * 7));
    }
    return fakeVoice(now / 1000);
  }

  // Smoothed level. Called by every effect each frame; computed once per
  // frame however many readers there are.
  read(now = performance.now()) {
    if (now === this.last) return this.value;
    const dt = Math.min(0.1, (now - this.last) / 1000 || 0.016);
    this.last = now;
    const target = this.raw(now);
    const k = target > this.value ? 1 - Math.exp(-dt / 0.05) : 1 - Math.exp(-dt / 0.35);
    this.value += (target - this.value) * k;
    return this.value;
  }
}

export const level = new LevelSource();
