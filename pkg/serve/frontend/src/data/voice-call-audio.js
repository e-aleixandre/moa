// voice-call-audio.js — the one client audio path of a voice call, the same
// for every provider: microphone → AudioWorklet → PCM s16le mono 24 kHz →
// moa's WebSocket, and moa's PCM → AudioWorklet → speakers. No filtering,
// no gating, no WebRTC: what the provider hears and says is whatever this
// path carries, unchanged except for the sample-rate conversion the device
// forces.
//
// The two worklet processors are plain classes kept free of outside
// references, so the exact source the browser runs can be built from them
// and also driven render quantum by render quantum in tests.

export const CALL_SAMPLE_RATE = 24000;
// 20 ms frames: 480 samples, 960 bytes, under the server's 9600-byte cap.
export const CAPTURE_FRAME_SAMPLES = 480;
// Playback holds at most this much audio. Providers stream faster than real
// time, so a long answer arrives well ahead of being heard; beyond the cap
// new audio is dropped and reported rather than growing without bound.
export const PLAYBACK_MAX_SECONDS = 60;
// What the provider may say while the microphone prompt or the worklet load is
// still pending: kept for playback, bounded; overflow drops the oldest.
export const PENDING_MAX_SECONDS = 4;

// RateConverter turns a float stream at one rate into another. Downsampling
// averages each output period (a box filter that keeps aliasing down);
// upsampling interpolates linearly. Integer phase, so no drift accumulates.
export class RateConverter {
  constructor(inRate, outRate) {
    this.inRate = Math.round(inRate);
    this.outRate = Math.round(outRate);
    this.phase = 0;
    this.acc = 0;
    this.n = 0;
    this.prev = 0;
  }

  // push feeds input samples and calls emit(sample) for each output sample.
  push(input, emit) {
    const inRate = this.inRate;
    const outRate = this.outRate;
    if (inRate === outRate) {
      for (let i = 0; i < input.length; i++) emit(input[i]);
      return;
    }
    if (inRate > outRate) {
      for (let i = 0; i < input.length; i++) {
        this.acc += input[i];
        this.n++;
        this.phase += outRate;
        if (this.phase >= inRate) {
          this.phase -= inRate;
          emit(this.acc / this.n);
          this.acc = 0;
          this.n = 0;
        }
      }
      return;
    }
    // Upsampling: between prev and each new sample, emit the points whose
    // position falls in that interval. phase counts in units of 1/outRate of
    // an input period, scaled by inRate.
    for (let i = 0; i < input.length; i++) {
      const cur = input[i];
      while (this.phase < outRate) {
        const t = this.phase / outRate;
        emit(this.prev + (cur - this.prev) * t);
        this.phase += inRate;
      }
      this.phase -= outRate;
      this.prev = cur;
    }
  }

  reset() {
    this.phase = 0;
    this.acc = 0;
    this.n = 0;
    this.prev = 0;
  }
}

// CaptureFramer converts device-rate float input into fixed 20 ms frames of
// signed 16-bit little-endian PCM at 24 kHz.
export class CaptureFramer {
  constructor(deviceRate, outRate, frameSamples, Converter) {
    this.converter = new Converter(deviceRate, outRate);
    this.frameSamples = frameSamples;
    this.frame = new ArrayBuffer(frameSamples * 2);
    this.view = new DataView(this.frame);
    this.filled = 0;
  }

  // push returns the frames completed by this input, as ArrayBuffers.
  push(input) {
    const out = [];
    this.converter.push(input, (s) => {
      const clamped = s < -1 ? -1 : s > 1 ? 1 : s;
      this.view.setInt16(this.filled * 2, clamped < 0 ? Math.round(clamped * 0x8000) : Math.round(clamped * 0x7fff), true);
      this.filled++;
      if (this.filled === this.frameSamples) {
        out.push(this.frame);
        this.frame = new ArrayBuffer(this.frameSamples * 2);
        this.view = new DataView(this.frame);
        this.filled = 0;
      }
    });
    return out;
  }
}

// PlaybackRing is a bounded FIFO of 24 kHz samples read at the device rate.
// Underruns play silence; overflow drops the new audio and is counted.
export class PlaybackRing {
  constructor(capacity, callRate, deviceRate) {
    this.buf = new Float32Array(capacity);
    this.capacity = capacity;
    this.read = 0;
    this.size = 0;
    this.step = callRate / deviceRate;
    this.frac = 0;
    this.playing = false;
    this.underruns = 0;
    this.dropped = 0;
  }

  // push appends s16le PCM bytes; it returns how many samples were dropped.
  push(bytes) {
    const view = new DataView(bytes);
    const count = Math.floor(bytes.byteLength / 2);
    let dropped = 0;
    for (let i = 0; i < count; i++) {
      if (this.size === this.capacity) {
        dropped = count - i;
        break;
      }
      this.buf[(this.read + this.size) % this.capacity] = view.getInt16(i * 2, true) / 0x8000;
      this.size++;
    }
    this.dropped += dropped;
    return dropped;
  }

  clear() {
    this.read = 0;
    this.size = 0;
    this.frac = 0;
  }

  // pull fills out at the device rate and reports the transitions the main
  // thread cares about: 'playing' when audio starts, 'drained' when it ends.
  pull(out) {
    let event = null;
    for (let i = 0; i < out.length; i++) {
      if (this.size < 2) {
        if (this.size === 1 && this.frac === 0) {
          out[i] = this.buf[this.read];
          this.read = (this.read + 1) % this.capacity;
          this.size = 0;
          continue;
        }
        out[i] = 0;
        if (this.playing) {
          this.playing = false;
          this.underruns++;
          event = 'drained';
        }
        continue;
      }
      if (!this.playing) {
        this.playing = true;
        event = 'playing';
      }
      const a = this.buf[this.read];
      const b = this.buf[(this.read + 1) % this.capacity];
      out[i] = a + (b - a) * this.frac;
      this.frac += this.step;
      while (this.frac >= 1 && this.size > 1) {
        this.frac -= 1;
        this.read = (this.read + 1) % this.capacity;
        this.size--;
      }
    }
    return event;
  }
}

// workletSource is the module the AudioWorklet loads. The classes travel as
// expressions, so a bundler renaming them cannot break the worklet.
export function workletSource() {
  return `
const RateConverter = (${RateConverter.toString()});
const CaptureFramer = (${CaptureFramer.toString()});
const PlaybackRing = (${PlaybackRing.toString()});
class MoaCallCapture extends AudioWorkletProcessor {
  constructor() {
    super();
    this.framer = new CaptureFramer(sampleRate, ${CALL_SAMPLE_RATE}, ${CAPTURE_FRAME_SAMPLES}, RateConverter);
    this.on = true;
    this.port.onmessage = (e) => { if (e.data && e.data.type === 'stop') this.on = false; };
  }
  process(inputs) {
    if (!this.on) return false;
    const channel = inputs[0] && inputs[0][0];
    if (channel && channel.length) {
      for (const frame of this.framer.push(channel)) this.port.postMessage(frame, [frame]);
    }
    return true;
  }
}
class MoaCallPlayback extends AudioWorkletProcessor {
  constructor() {
    super();
    this.ring = new PlaybackRing(${CALL_SAMPLE_RATE * PLAYBACK_MAX_SECONDS}, ${CALL_SAMPLE_RATE}, sampleRate);
    this.on = true;
    this.port.onmessage = (e) => {
      const m = e.data || {};
      if (m.type === 'pcm') {
        const dropped = this.ring.push(m.pcm);
        if (dropped) this.port.postMessage({ type: 'overflow', dropped });
      } else if (m.type === 'clear') {
        const was = this.ring.playing || this.ring.size > 0;
        this.ring.clear();
        this.ring.playing = false;
        if (was) this.port.postMessage({ type: 'drained' });
      } else if (m.type === 'stop') {
        this.on = false;
      }
    };
  }
  process(_inputs, outputs) {
    if (!this.on) return false;
    const out = outputs[0] && outputs[0][0];
    if (out) {
      const event = this.ring.pull(out);
      for (let c = 1; c < outputs[0].length; c++) outputs[0][c].set(out);
      if (event) this.port.postMessage({ type: event });
    }
    return true;
  }
}
registerProcessor('moa-call-capture', MoaCallCapture);
registerProcessor('moa-call-playback', MoaCallPlayback);
`;
}

// createCallAudio is the browser implementation of the controller's audio
// boundary: { prepare, start(onPCM, {onPlayback}), play, interrupt, setMuted,
// stop, stream }. Everything it acquires — microphone, AudioContext, worklet
// nodes, module URL, wake lock — is released by stop(), which is idempotent.
export function createCallAudio({
  AudioContextImpl = globalThis.AudioContext || globalThis.webkitAudioContext,
  AudioWorkletNodeImpl = globalThis.AudioWorkletNode,
  mediaDevices = globalThis.navigator?.mediaDevices,
  claimWakeLock = null,
  URLImpl = globalThis.URL,
  BlobImpl = globalThis.Blob,
} = {}) {
  let ctx = null;
  let stream = null;
  let source = null;
  let capture = null;
  let playback = null;
  let moduleURL = '';
  let releaseWake = null;
  let stopped = false;
  let pending = [];
  let pendingBytes = 0;

  const engine = {
    get stream() { return stream; },
    pendingDropped: 0,
    supported: !!(AudioContextImpl && AudioWorkletNodeImpl && mediaDevices?.getUserMedia),

    // prepare runs inside the user's tap: browsers only let an AudioContext
    // start sounding from a gesture, and the call is opened after a fetch.
    prepare() {
      if (stopped || ctx || !AudioContextImpl) return;
      ctx = new AudioContextImpl();
      ctx.resume?.()?.catch?.(() => {});
    },

    async start(onPCM, { onPlayback } = {}) {
      if (stopped) throw new Error('audio stopped');
      engine.prepare();
      if (!ctx) throw new Error('audio unavailable');
      const mic = await mediaDevices.getUserMedia({ audio: true });
      if (stopped) {
        for (const t of mic.getTracks()) t.stop();
        throw new Error('audio stopped');
      }
      stream = mic;
      moduleURL = URLImpl.createObjectURL(new BlobImpl([workletSource()], { type: 'application/javascript' }));
      await ctx.audioWorklet.addModule(moduleURL);
      if (stopped) throw new Error('audio stopped');
      source = ctx.createMediaStreamSource(mic);
      capture = new AudioWorkletNodeImpl(ctx, 'moa-call-capture', { numberOfInputs: 1, numberOfOutputs: 1, outputChannelCount: [1] });
      capture.port.onmessage = (e) => { if (!stopped && e.data instanceof ArrayBuffer) onPCM(e.data); };
      playback = new AudioWorkletNodeImpl(ctx, 'moa-call-playback', { numberOfInputs: 0, numberOfOutputs: 1, outputChannelCount: [1] });
      playback.port.onmessage = (e) => { if (!stopped) onPlayback?.(e.data); };
      source.connect(capture);
      // The capture node is pulled by the graph only when it reaches the
      // destination; its output is silence.
      capture.connect(ctx.destination);
      playback.connect(ctx.destination);
      for (const chunk of pending) playback.port.postMessage({ type: 'pcm', pcm: chunk }, [chunk]);
      pending = [];
      pendingBytes = 0;
      await ctx.resume?.();
      releaseWake = claimWakeLock ? claimWakeLock() : null;
    },

    play(pcm) {
      if (stopped) return;
      const bytes = pcm instanceof ArrayBuffer ? pcm : pcm.buffer.slice(pcm.byteOffset, pcm.byteOffset + pcm.byteLength);
      if (!playback) {
        pending.push(bytes);
        pendingBytes += bytes.byteLength;
        const cap = CALL_SAMPLE_RATE * 2 * PENDING_MAX_SECONDS;
        while (pendingBytes > cap && pending.length) {
          const dropped = pending.shift();
          pendingBytes -= dropped.byteLength;
          engine.pendingDropped += dropped.byteLength;
        }
        return;
      }
      playback.port.postMessage({ type: 'pcm', pcm: bytes }, [bytes]);
    },

    interrupt() {
      pending = [];
      pendingBytes = 0;
      if (!stopped) playback?.port.postMessage({ type: 'clear' });
    },

    setMuted(muted) {
      for (const t of stream?.getAudioTracks?.() || []) t.enabled = !muted;
    },

    stop() {
      if (stopped) return;
      stopped = true;
      pending = [];
      pendingBytes = 0;
      try { capture?.port.postMessage({ type: 'stop' }); } catch { /* closing */ }
      try { playback?.port.postMessage({ type: 'stop' }); } catch { /* closing */ }
      for (const node of [source, capture, playback]) {
        try { node?.disconnect(); } catch { /* already gone */ }
      }
      for (const t of stream?.getTracks?.() || []) t.stop();
      stream = null;
      if (ctx) ctx.close?.()?.catch?.(() => {});
      ctx = null;
      if (moduleURL) URLImpl.revokeObjectURL(moduleURL);
      moduleURL = '';
      releaseWake?.();
      releaseWake = null;
    },
  };
  return engine;
}
