import { test, expect } from 'bun:test';
import {
  workletSource, createCallAudio, CALL_SAMPLE_RATE, CAPTURE_FRAME_SAMPLES, PLAYBACK_MAX_SECONDS, PENDING_MAX_SECONDS,
} from './voice-call-audio.js';

// Loads the exact module source the browser's AudioWorklet runs into a
// minimal AudioWorkletGlobalScope and returns its processors, so these
// tests drive the real processors one 128-frame render quantum at a time.
function loadWorklet(deviceRate) {
  const processors = {};
  class AudioWorkletProcessor {
    constructor() {
      this.posted = [];
      this.port = { postMessage: (m) => this.posted.push(m), onmessage: null };
    }
  }
  const scope = new Function('AudioWorkletProcessor', 'registerProcessor', 'sampleRate', workletSource());
  scope(AudioWorkletProcessor, (name, cls) => { processors[name] = cls; }, deviceRate);
  return processors;
}

function pcmBytes(samples) {
  const buf = new ArrayBuffer(samples.length * 2);
  const view = new DataView(buf);
  samples.forEach((s, i) => view.setInt16(i * 2, s, true));
  return buf;
}

function decode(buf) {
  const view = new DataView(buf);
  return Array.from({ length: buf.byteLength / 2 }, (_, i) => view.getInt16(i * 2, true));
}

function render(proc, quanta = 1) {
  const blocks = [];
  for (let q = 0; q < quanta; q++) {
    const out = new Float32Array(128);
    expect(proc.process([], [[out]])).toBe(true);
    blocks.push(out);
  }
  return blocks;
}

test('capture: 48 kHz device input becomes 20 ms frames of s16le mono 24 kHz', () => {
  const { 'moa-call-capture': Capture } = loadWorklet(48000);
  const proc = new Capture();
  // 1920 device samples = 40 ms = two frames at 24 kHz. Pairs of equal
  // samples survive the 2:1 average exactly, including both PCM extremes.
  const input = new Float32Array(1920);
  for (let i = 0; i < input.length; i++) {
    const k = Math.floor(i / 2);
    input[i] = k === 0 ? -1 : k === 1 ? 1 : k === 2 ? 0.5 : 0;
  }
  for (let off = 0; off < input.length; off += 128) proc.process([[input.subarray(off, off + 128)]]);
  expect(proc.posted).toHaveLength(2);
  for (const frame of proc.posted) {
    expect(frame).toBeInstanceOf(ArrayBuffer);
    expect(frame.byteLength).toBe(CAPTURE_FRAME_SAMPLES * 2);
  }
  expect(decode(proc.posted[0]).slice(0, 4)).toEqual([-32768, 32767, 16384, 0]);
});

test('capture: 44.1 kHz input yields 24 kHz frames at the right rate, and stop ends processing', () => {
  const { 'moa-call-capture': Capture } = loadWorklet(44100);
  const proc = new Capture();
  const quantum = new Float32Array(128).fill(0.25);
  // One second of device audio = 24000 samples = 50 frames.
  const quanta = Math.ceil(44100 / 128);
  for (let i = 0; i < quanta; i++) proc.process([[quantum]]);
  const samples = proc.posted.length * CAPTURE_FRAME_SAMPLES;
  expect(Math.abs(samples - CALL_SAMPLE_RATE)).toBeLessThanOrEqual(CAPTURE_FRAME_SAMPLES);
  expect(new Set(decode(proc.posted[3]))).toEqual(new Set([Math.round(0.25 * 0x7fff)]));
  proc.port.onmessage({ data: { type: 'stop' } });
  expect(proc.process([[quantum]])).toBe(false);
});

test('playback: underrun plays silence and reports playing then drained once', () => {
  const { 'moa-call-playback': Playback } = loadWorklet(24000);
  const proc = new Playback();
  // Nothing queued: silence, no events.
  const [idle] = render(proc);
  expect(idle.every((s) => s === 0)).toBe(true);
  expect(proc.posted).toEqual([]);

  // 200 samples at the device's own rate: one full quantum, then the rest
  // of the queue and an underrun inside the second quantum.
  const samples = Array.from({ length: 200 }, (_, i) => (i % 2 ? 8192 : -8192));
  proc.port.onmessage({ data: { type: 'pcm', pcm: pcmBytes(samples) } });
  const [first, second] = render(proc, 2);
  expect(first[0]).toBeCloseTo(-0.25, 5);
  expect(first[1]).toBeCloseTo(0.25, 5);
  expect(Array.from(second.subarray(80))).toEqual(new Array(48).fill(0));
  expect(proc.posted.map((m) => m.type)).toEqual(['playing', 'drained']);
  // Starving further does not repeat the report.
  render(proc, 3);
  expect(proc.posted.map((m) => m.type)).toEqual(['playing', 'drained']);
});

test('playback: 24 kHz audio is resampled to a 48 kHz device without changing its duration', () => {
  const { 'moa-call-playback': Playback } = loadWorklet(48000);
  const proc = new Playback();
  proc.port.onmessage({ data: { type: 'pcm', pcm: pcmBytes(new Array(2400).fill(16384)) } });
  // 100 ms of call audio is 4800 device samples: 37.5 quanta.
  const blocks = render(proc, 40);
  const nonzero = blocks.reduce((n, b) => n + b.filter((s) => s !== 0).length, 0);
  expect(Math.abs(nonzero - 4800)).toBeLessThanOrEqual(2);
  expect(blocks[10][5]).toBeCloseTo(0.5, 5);
});

test('playback: a clear (barge-in) empties the queue at once', () => {
  const { 'moa-call-playback': Playback } = loadWorklet(24000);
  const proc = new Playback();
  proc.port.onmessage({ data: { type: 'pcm', pcm: pcmBytes(new Array(24000).fill(1000)) } });
  render(proc);
  proc.port.onmessage({ data: { type: 'clear' } });
  const [after] = render(proc);
  expect(after.every((s) => s === 0)).toBe(true);
  expect(proc.posted.map((m) => m.type)).toEqual(['playing', 'drained']);
  // New audio after the interruption plays from its own start.
  proc.port.onmessage({ data: { type: 'pcm', pcm: pcmBytes([4096, 4096, 4096]) } });
  const [resumed] = render(proc);
  expect(resumed[0]).toBeCloseTo(0.125, 5);
});

test('playback: the queue is bounded; overflow drops new audio and reports it', () => {
  const { 'moa-call-playback': Playback } = loadWorklet(24000);
  const proc = new Playback();
  const cap = CALL_SAMPLE_RATE * PLAYBACK_MAX_SECONDS;
  const chunk = pcmBytes(new Array(CALL_SAMPLE_RATE * 10).fill(100));
  for (let i = 0; i < PLAYBACK_MAX_SECONDS / 10; i++) proc.port.onmessage({ data: { type: 'pcm', pcm: chunk.slice(0) } });
  expect(proc.posted).toEqual([]);
  proc.port.onmessage({ data: { type: 'pcm', pcm: pcmBytes(new Array(500).fill(100)) } });
  expect(proc.posted).toEqual([{ type: 'overflow', dropped: 500 }]);
  // Playing frees room again.
  render(proc, 10);
  proc.port.onmessage({ data: { type: 'pcm', pcm: pcmBytes(new Array(500).fill(100)) } });
  expect(proc.posted.filter((m) => m.type === 'overflow')).toHaveLength(1);
  expect(cap).toBe(1_440_000);
});

// The browser engine releases every resource it acquired, whatever the order
// of the user's actions.
function fakeBrowser() {
  const log = [];
  const track = { kind: 'audio', enabled: true, readyState: 'live', stop() { this.readyState = 'ended'; log.push('track.stop'); } };
  const stream = { getTracks: () => [track], getAudioTracks: () => [track] };
  const nodes = [];
  class Node {
    constructor(ctx, name) {
      this.name = name;
      this.port = { posted: [], postMessage: (m) => this.port.posted.push(m), onmessage: null };
      nodes.push(this);
    }
    connect() {}
    disconnect() { log.push(`disconnect:${this.name}`); }
  }
  class Ctx {
    constructor() { this.state = 'running'; this.destination = {}; log.push('ctx.new'); this.audioWorklet = { addModule: async (url) => log.push(`addModule:${url}`) }; }
    resume() { return Promise.resolve(); }
    close() { this.state = 'closed'; log.push('ctx.close'); return Promise.resolve(); }
    createMediaStreamSource() { return new Node(this, 'source'); }
  }
  const urls = { created: [], revoked: [] };
  return {
    log, track, nodes, urls,
    deps: {
      AudioContextImpl: Ctx,
      AudioWorkletNodeImpl: Node,
      mediaDevices: { getUserMedia: async (c) => { log.push(`gum:${JSON.stringify(c)}`); return stream; } },
      claimWakeLock: () => { log.push('wake.claim'); return () => log.push('wake.release'); },
      URLImpl: { createObjectURL: () => { urls.created.push('blob:1'); return 'blob:1'; }, revokeObjectURL: (u) => urls.revoked.push(u) },
      BlobImpl: class { constructor(parts) { this.parts = parts; } },
    },
  };
}

test('engine: stop releases microphone, AudioContext, nodes, module URL and wake lock exactly once', async () => {
  const b = fakeBrowser();
  const engine = createCallAudio(b.deps);
  const frames = [];
  const events = [];
  await engine.start((pcm) => frames.push(pcm), { onPlayback: (e) => events.push(e) });
  expect(b.log).toContain('wake.claim');
  const [, capture, playback] = b.nodes;
  capture.port.onmessage({ data: new ArrayBuffer(960) });
  expect(frames).toHaveLength(1);
  engine.play(new Uint8Array([1, 0, 2, 0]).subarray(2));
  expect(playback.port.posted[0].type).toBe('pcm');
  expect(new Uint8Array(playback.port.posted[0].pcm)).toEqual(new Uint8Array([2, 0]));
  engine.setMuted(true);
  expect(b.track.enabled).toBe(false);
  engine.interrupt();
  expect(playback.port.posted.at(-1)).toEqual({ type: 'clear' });

  engine.stop();
  engine.stop();
  expect(b.track.readyState).toBe('ended');
  expect(b.log.filter((l) => l === 'ctx.close')).toHaveLength(1);
  expect(b.log.filter((l) => l === 'wake.release')).toHaveLength(1);
  expect(b.urls.revoked).toEqual(['blob:1']);
  expect(b.log.filter((l) => l.startsWith('disconnect:'))).toHaveLength(3);
  // Late worklet frames and playback after stop go nowhere.
  capture.port.onmessage({ data: new ArrayBuffer(960) });
  playback.port.onmessage({ data: { type: 'playing' } });
  engine.play(new ArrayBuffer(2));
  expect(frames).toHaveLength(1);
  expect(events).toEqual([]);
  expect(playback.port.posted.filter((m) => m.type === 'pcm')).toHaveLength(1);
});

test('engine: the audio session is play-and-record before the context exists, and restored on stop', async () => {
  const b = fakeBrowser();
  const seen = [];
  const audioSession = { _t: 'auto', get type() { return this._t; }, set type(v) { this._t = v; seen.push(v); } };
  const Ctx = b.deps.AudioContextImpl;
  b.deps.AudioContextImpl = class extends Ctx { constructor() { super(); b.log.push(`ctx.session:${audioSession.type}`); } };
  const engine = createCallAudio({ ...b.deps, audioSession });
  engine.prepare();
  await engine.start(() => {});
  expect(b.log).toContain('ctx.session:play-and-record');
  engine.stop();
  engine.stop();
  expect(seen).toEqual(['play-and-record', 'auto']);
});

test('engine: stopping while the microphone prompt is open still releases the microphone', async () => {
  const b = fakeBrowser();
  let grant;
  b.deps.mediaDevices.getUserMedia = () => new Promise((resolve) => { grant = resolve; });
  const engine = createCallAudio(b.deps);
  const started = engine.start(() => {});
  await Promise.resolve();
  engine.stop();
  grant({ getTracks: () => [b.track], getAudioTracks: () => [b.track] });
  await expect(started).rejects.toThrow();
  expect(b.track.readyState).toBe('ended');
  expect(b.log).not.toContain('wake.claim');
  expect(b.log.filter((l) => l === 'ctx.close')).toHaveLength(1);
});

// I1: the provider can speak while the microphone prompt or the worklet load
// is still pending. That audio is kept (bounded) and plays once playback is
// ready; it is not dropped.
const pcmFrame = (n, fill) => new Uint8Array(n).fill(fill).buffer;

test('engine: audio received before playback is ready plays, in order, once it is ready', async () => {
  const b = fakeBrowser();
  let grant;
  b.deps.mediaDevices.getUserMedia = () => new Promise((resolve) => { grant = resolve; });
  const engine = createCallAudio(b.deps);
  const started = engine.start(() => {});
  await Promise.resolve();
  engine.play(pcmFrame(4, 1));
  engine.play(new Uint8Array([9, 9, 2, 2, 2, 2]).subarray(2));
  grant({ getTracks: () => [b.track], getAudioTracks: () => [b.track] });
  await started;
  const playback = b.nodes[2];
  const pcm = playback.port.posted.filter((m) => m.type === 'pcm').map((m) => Array.from(new Uint8Array(m.pcm)));
  expect(pcm).toEqual([[1, 1, 1, 1], [2, 2, 2, 2]]);
  engine.play(pcmFrame(2, 3));
  expect(playback.port.posted.filter((m) => m.type === 'pcm')).toHaveLength(3);
});

test('engine: the audio kept before ready is bounded; overflow drops the oldest and counts it', async () => {
  const b = fakeBrowser();
  let grant;
  b.deps.mediaDevices.getUserMedia = () => new Promise((resolve) => { grant = resolve; });
  const engine = createCallAudio(b.deps);
  const started = engine.start(() => {});
  await Promise.resolve();
  const cap = CALL_SAMPLE_RATE * 2 * PENDING_MAX_SECONDS;
  const chunk = CALL_SAMPLE_RATE * 2; // one second
  for (let i = 0; i < PENDING_MAX_SECONDS + 3; i++) engine.play(pcmFrame(chunk, i + 1));
  expect(engine.pendingDropped).toBe(3 * chunk);
  grant({ getTracks: () => [b.track], getAudioTracks: () => [b.track] });
  await started;
  const sent = b.nodes[2].port.posted.filter((m) => m.type === 'pcm');
  expect(sent.reduce((n, m) => n + m.pcm.byteLength, 0)).toBe(cap);
  expect(new Uint8Array(sent[0].pcm)[0]).toBe(4);
  expect(engine.pendingDropped).toBe(3 * chunk);
});

test('engine: a barge-in before ready discards what was waiting; stop discards it too', async () => {
  const b = fakeBrowser();
  let grant;
  b.deps.mediaDevices.getUserMedia = () => new Promise((resolve) => { grant = resolve; });
  const engine = createCallAudio(b.deps);
  const started = engine.start(() => {});
  await Promise.resolve();
  engine.play(pcmFrame(4, 1));
  engine.interrupt();
  engine.play(pcmFrame(4, 2));
  grant({ getTracks: () => [b.track], getAudioTracks: () => [b.track] });
  await started;
  const pcm = b.nodes[2].port.posted.filter((m) => m.type === 'pcm').map((m) => Array.from(new Uint8Array(m.pcm)));
  expect(pcm).toEqual([[2, 2, 2, 2]]);
});
