import { test, expect, beforeEach, afterEach } from 'bun:test';
import { nativeCallBridge, createNativeCallDeps } from './voice-call-native.js';
import { VoiceCallController } from './voice-call.js';

function fakeBridge() {
  const calls = [];
  let n = 0;
  const bridge = {
    version: 1,
    onevent: null,
    open: (url, opts) => { calls.push(['open', url, opts.name]); return `c${++n}`; },
    send: (id, text) => calls.push(['send', id, text]),
    close: (id) => calls.push(['close', id]),
    startAudio: async (id) => { calls.push(['startAudio', id]); },
    stopAudio: (id) => calls.push(['stopAudio', id]),
    interrupt: (id) => calls.push(['interrupt', id]),
    active: async () => null,
    setMuted: (id, m) => calls.push(['setMuted', id, m]),
    emit(id, type, data) { this.onevent({ id, type, data }); },
  };
  return { bridge, calls };
}

// The controller builds the socket URL from the page's location.
let savedLocation;
beforeEach(() => { savedLocation = globalThis.location; globalThis.location = { protocol: 'https:', host: 'moa.example' }; });
afterEach(() => { globalThis.location = savedLocation; });

const flush = () => new Promise((r) => setTimeout(r, 0));

function controllerWith(bridge) {
  const deps = createNativeCallDeps(bridge, { name: () => 'Infra' });
  const fetch = async (url) => {
    if (url === '/api/voice/calls') return { status: 201, ok: true, json: async () => ({ call_id: 'k1', provider: 'chatgpt-plan', ws_url: '/api/voice/calls/k1/ws' }) };
    return { status: 200, ok: true, json: async () => ({}) };
  };
  return new VoiceCallController({ sessionId: 's1', fetch, WebSocket: deps.WebSocket, createAudio: deps.createAudio });
}

test('bridge: only a v1 bridge with open() is used', () => {
  expect(nativeCallBridge({})).toBeNull();
  expect(nativeCallBridge({ MoaNativeCall: { version: 1 } })).toBeNull();
  const b = { version: 1, open() {} };
  expect(nativeCallBridge({ MoaNativeCall: b })).toBe(b);
});

test('native call: the app owns socket and audio; the controller mirrors its frames', async () => {
  const { bridge, calls } = fakeBridge();
  const c = controllerWith(bridge);
  const started = c.start();
  await flush();
  expect(calls[0]).toEqual(['open', 'wss://moa.example/api/voice/calls/k1/ws', 'Infra']);
  bridge.emit('c1', 'open');
  await started;
  expect(calls).toContainEqual(['startAudio', 'c1']);
  bridge.emit('c1', 'message', JSON.stringify({ type: 'state', state: 'listening' }));
  expect(c.state().phase).toBe('listening');
  bridge.emit('c1', 'playback', 'playing');
  expect(c.state().phase).toBe('speaking');
  // The app clears its own playback on a barge-in as it arrives; the
  // mirror, which may run much later, must not clear it again (it would cut
  // the next reply).
  bridge.emit('c1', 'message', JSON.stringify({ type: 'speech_started' }));
  bridge.emit('c1', 'message', JSON.stringify({ type: 'state', state: 'listening', interrupt: true }));
  expect(calls.filter((x) => x[0] === 'interrupt')).toEqual([]);
  expect(c.state().phase).toBe('listening');

  // Muted from the lock screen: the interface follows without a second
  // control from the page.
  bridge.emit('c1', 'mute', true);
  expect(c.state().muted).toBe(true);
  expect(calls.filter((x) => x[0] === 'send')).toEqual([]);

  // Muted from the page: the app is told and the control goes out.
  c.setMuted(false);
  expect(calls).toContainEqual(['setMuted', 'c1', false]);
  expect(calls).toContainEqual(['send', 'c1', JSON.stringify({ type: 'mute', muted: false })]);
});

test('native call: the app losing the socket ends the call as client_lost', async () => {
  const { bridge } = fakeBridge();
  const c = controllerWith(bridge);
  const started = c.start();
  await flush();
  bridge.emit('c1', 'open');
  await started;
  bridge.emit('c1', 'close');
  expect(c.state().phase).toBe('ended');
  expect(c.state().endedReason).toBe('client_lost');
});

test('native call: the server ending (e.g. hung up from the lock screen) ends it with its reason', async () => {
  const { bridge, calls } = fakeBridge();
  const c = controllerWith(bridge);
  const started = c.start();
  await flush();
  bridge.emit('c1', 'open');
  await started;
  bridge.emit('c1', 'message', JSON.stringify({ type: 'ended', reason: 'hangup' }));
  expect(c.state().phase).toBe('ended');
  expect(c.state().endedReason).toBe('hangup');
  expect(calls).toContainEqual(['stopAudio', 'c1']);
  expect(calls).toContainEqual(['close', 'c1']);
});

test('native call: hanging up from the page stops the app audio and closes its socket', async () => {
  const { bridge, calls } = fakeBridge();
  const c = controllerWith(bridge);
  const started = c.start();
  await flush();
  bridge.emit('c1', 'open');
  await started;
  await c.hangup();
  expect(calls).toContainEqual(['stopAudio', 'c1']);
  expect(calls).toContainEqual(['close', 'c1']);
  expect(c.state().endedReason).toBe('hangup');
});

test('native call: a microphone the app cannot open ends the call on the server', async () => {
  const { bridge, calls } = fakeBridge();
  bridge.startAudio = async () => { throw new Error('microphone'); };
  const c = controllerWith(bridge);
  const started = c.start();
  await flush();
  bridge.emit('c1', 'open');
  await started;
  expect(calls).toContainEqual(['send', 'c1', JSON.stringify({ type: 'hangup' })]);
  expect(c.state().phase).toBe('ended');
});

test('native call: a reloaded page adopts the call the app kept, without opening another', async () => {
  const { bridge, calls } = fakeBridge();
  const deps = createNativeCallDeps(bridge);
  const fetches = [];
  const c = new VoiceCallController({ sessionId: 's9', fetch: async (u) => { fetches.push(u); return { ok: true, status: 200, json: async () => ({}) }; }, WebSocket: deps.WebSocket, createAudio: deps.createAudio });
  deps.adopt('kept');
  await c.adopt({ callId: 'k7', muted: true });
  expect(calls.filter((x) => x[0] === 'open')).toEqual([]);
  expect(fetches).toEqual([]);
  expect(calls).toContainEqual(['startAudio', 'kept']);
  expect(c.state()).toMatchObject({ phase: 'listening', callId: 'k7', muted: true, active: true });
  bridge.emit('kept', 'message', JSON.stringify({ type: 'ended', reason: 'hangup' }));
  expect(c.state().phase).toBe('ended');
  expect(calls).toContainEqual(['close', 'kept']);
});

// The bridge script the iOS app injects, run as the page would run it.
async function injectedBridge() {
  const swift = await Bun.file(new URL('../../../../../mobile/ios/App/App/NativeVoiceCall.swift', import.meta.url)).text();
  const src = swift.match(/static let javaScript = #"""\n([\s\S]*?)\n\s*"""#/)[1];
  const posted = [];
  const win = { webkit: { messageHandlers: { moaNativeCall: { postMessage: (m) => posted.push(m) } } } };
  new Function('window', src)(win);
  return { b: win.MoaNativeCall, posted };
}

test('injected bridge: a pending startAudio is rejected when its call closes or stops', async () => {
  const { b, posted } = await injectedBridge();
  const id = b.open('wss://x/api/voice/calls/k/ws', { name: 'n', sessionId: 's' });
  expect(posted[0]).toMatchObject({ op: 'open', id, sessionId: 's' });
  const p1 = b.startAudio(id);
  b.__event({ id, type: 'close' });
  await expect(p1).rejects.toThrow('closed');
  const p2 = b.startAudio(id);
  b.stopAudio(id);
  await expect(p2).rejects.toThrow('stopped');
  const p3 = b.startAudio(id);
  b.__event({ id, type: 'audio', data: 'ok' });
  await expect(p3).resolves.toBeUndefined();
});

test('injected bridge: active() resolves with the app snapshot', async () => {
  const { b, posted } = await injectedBridge();
  const p = b.active();
  expect(posted.at(-1)).toMatchObject({ op: 'active' });
  b.__event({ id: '', type: 'active', data: { id: 'n1', sessionId: 's', callId: 'k', muted: false } });
  await expect(p).resolves.toEqual({ id: 'n1', sessionId: 's', callId: 'k', muted: false });
});

test('native call: hanging up while the app opens the microphone ends as hangup, not microphone', async () => {
  const { b } = await injectedBridge();
  const deps = createNativeCallDeps(b, { name: () => 'X' });
  let release;
  const fetch = async (url) => {
    if (url === '/api/voice/calls') return { status: 201, ok: true, json: async () => ({ call_id: 'k1', ws_url: '/api/voice/calls/k1/ws' }) };
    await new Promise((r) => { release = r; });
    return { status: 200, ok: true, json: async () => ({}) };
  };
  const c = new VoiceCallController({ sessionId: 's1', fetch, WebSocket: deps.WebSocket, createAudio: deps.createAudio });
  c.start();
  await flush();
  b.__event({ id: c.ws.id, type: 'open' });
  await flush();
  const hung = c.hangup();
  await flush();
  release();
  await hung;
  expect(c.state().endedReason).toBe('hangup');
  expect(c.state().message).toBe('');
});

test('native call: the app dropping the call before audio hands the same call to the page', async () => {
  const { b } = await injectedBridge();
  const deps = createNativeCallDeps(b, { name: () => 'X' });
  const opened = [];
  class PageSocket {
    constructor(url) { this.url = url; this.readyState = 0; opened.push(this); }
    send() {}
    close() { this.readyState = 3; }
  }
  const pageAudio = { started: 0, prepare() {}, async start() { this.started++; }, stop() {}, play() {}, interrupt() {}, setMuted() {} };
  const notes = [];
  const fetch = async () => ({ status: 201, ok: true, json: async () => ({ call_id: 'k1', ws_url: '/api/voice/calls/k1/ws' }) });
  const c = new VoiceCallController({
    sessionId: 's1', fetch, WebSocket: deps.WebSocket, createAudio: deps.createAudio,
    fallback: { WebSocket: PageSocket, createAudio: () => pageAudio }, onFallback: (n) => notes.push(n),
  });
  const started = c.start();
  await flush();
  const id = c.ws.id;
  b.__event({ id, type: 'open' });
  await flush();
  b.__event({ id, type: 'close' }); // build 7: the app closed it 70 ms after open
  await flush();
  expect(opened).toHaveLength(1);
  expect(opened[0].url).toBe('wss://moa.example/api/voice/calls/k1/ws?client_note=native%3Aclosed_before_audio');
  expect(notes).toEqual(['closed_before_audio']);
  expect(c.state().phase).toBe('connecting');
  opened[0].readyState = 1;
  opened[0].onopen();
  await flush();
  expect(pageAudio.started).toBe(1);
  expect(c.state().active).toBe(true);
  // Once on the page's socket, a drop is a real loss.
  opened[0].onclose();
  expect(c.state().endedReason).toBe('client_lost');
  await started;
});

test('native call: the app audio failing before its close still hands the call to the page', async () => {
  const { b } = await injectedBridge();
  const deps = createNativeCallDeps(b, { name: () => 'X' });
  const opened = [];
  class PageSocket {
    constructor(url) { this.url = url; this.readyState = 0; opened.push(this); }
    send() {}
    close() { this.readyState = 3; }
  }
  const pageAudio = { prepare() {}, async start() {}, stop() {}, play() {}, interrupt() {}, setMuted() {} };
  const fetch = async () => ({ status: 201, ok: true, json: async () => ({ call_id: 'k1', ws_url: '/api/voice/calls/k1/ws' }) });
  const c = new VoiceCallController({
    sessionId: 's1', fetch, WebSocket: deps.WebSocket, createAudio: deps.createAudio,
    fallback: { WebSocket: PageSocket, createAudio: () => pageAudio },
  });
  c.start();
  await flush();
  const id = c.ws.id;
  b.__event({ id, type: 'open' });
  await flush();
  // Two separate evaluations from the app: the audio error, then its close.
  b.__event({ id, type: 'audio', data: 'audio_failed' });
  await flush();
  b.__event({ id, type: 'close', data: 'native:engine x mic=granted' });
  await flush();
  expect(c.state().endedReason).not.toBe('microphone');
  expect(opened).toHaveLength(1);
  expect(opened[0].url).toContain('client_note=native%3Aaudio_failed');
  expect(c.state().phase).toBe('connecting');
});

test('native call: a microphone the user denied ends the call as microphone, no takeover', async () => {
  const { b } = await injectedBridge();
  const deps = createNativeCallDeps(b, { name: () => 'X' });
  const opened = [];
  const fetch = async () => ({ status: 201, ok: true, json: async () => ({ call_id: 'k1', ws_url: '/api/voice/calls/k1/ws' }) });
  const c = new VoiceCallController({
    sessionId: 's1', fetch, WebSocket: deps.WebSocket, createAudio: deps.createAudio,
    fallback: { WebSocket: class { constructor() { opened.push(this); } }, createAudio: () => ({ prepare() {}, async start() {}, stop() {} }) },
  });
  c.start();
  await flush();
  b.__event({ id: c.ws.id, type: 'open' });
  await flush();
  b.__event({ id: c.ws.id, type: 'audio', data: 'microphone' });
  await flush();
  expect(c.state().endedReason).toBe('microphone');
  expect(opened).toHaveLength(0);
});
