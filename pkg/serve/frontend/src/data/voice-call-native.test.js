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
  bridge.emit('c1', 'message', JSON.stringify({ type: 'speech_started' }));
  expect(calls).toContainEqual(['interrupt', 'c1']);
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
