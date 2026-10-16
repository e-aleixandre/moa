import { test, expect, afterAll } from 'bun:test';
import { VoiceCallController, startFailureMessage, billingLabel } from './voice-call.js';

const savedLocation = Object.getOwnPropertyDescriptor(globalThis, 'location');
afterAll(() => {
  if (savedLocation) Object.defineProperty(globalThis, 'location', savedLocation);
  else delete globalThis.location;
});

async function flush(n = 12) { for (let i = 0; i < n; i++) await Promise.resolve(); }

function harness({ replies = [{ status: 201, body: { call_id: 'c1', provider: 'chatgpt-plan', billing: { kind: 'plan_unverified' }, ws_url: '/api/voice/calls/c1/ws' } }] } = {}) {
  const sockets = [];
  const requests = [];
  const engines = [];
  class WS {
    constructor(url) { this.url = url; this.readyState = 0; this.sent = []; this.bufferedAmount = 0; sockets.push(this); }
    send(d) { this.sent.push(d); }
    close() { this.readyState = 3; this.onclose?.(); }
    open() { this.readyState = 1; this.onopen?.(); }
    control(m) { this.onmessage?.({ data: JSON.stringify(m) }); }
  }
  const createAudio = () => {
    const e = { calls: [], capture: null, onPlayback: null };
    e.prepare = () => e.calls.push('prepare');
    e.start = async (onPCM, opts) => { e.calls.push('start'); e.capture = onPCM; e.onPlayback = opts?.onPlayback; };
    e.play = () => e.calls.push('play');
    e.interrupt = () => e.calls.push('interrupt');
    e.setMuted = (m) => e.calls.push(`muted:${m}`);
    e.stop = () => e.calls.push('stop');
    engines.push(e);
    return e;
  };
  const fetch = async (url, init) => {
    requests.push({ url, body: init?.body ? JSON.parse(init.body) : null, headers: init?.headers });
    const r = url.endsWith('/hangup') ? { status: 204 } : replies.shift();
    return new Response(r.body ? JSON.stringify(r.body) : null, { status: r.status });
  };
  globalThis.location = { protocol: 'https:', host: 'moa.test' };
  const c = new VoiceCallController({ sessionId: 's1', fetch, WebSocket: WS, createAudio });
  return { c, sockets, requests, engines };
}

async function live(h) {
  const p = h.c.start();
  await flush();
  h.sockets[0].open();
  await flush();
  h.sockets[0].control({ type: 'state', state: 'listening' });
  await p;
  return h.sockets[0];
}

test('mute stops sending captured audio and tells the server; unmute resumes', async () => {
  const h = harness();
  const ws = await live(h);
  const e = h.engines[0];
  e.capture(new ArrayBuffer(960));
  h.c.setMuted(true);
  e.capture(new ArrayBuffer(960));
  h.c.setMuted(false);
  e.capture(new ArrayBuffer(960));
  const binary = ws.sent.filter((d) => typeof d !== 'string');
  const json = ws.sent.filter((d) => typeof d === 'string').map((d) => JSON.parse(d));
  expect(binary).toHaveLength(2);
  expect(json).toEqual([{ type: 'mute', muted: true }, { type: 'mute', muted: false }]);
  expect(e.calls).toContain('muted:true');
  expect(h.c.state().muted).toBe(false);
});

test('a stalled socket drops new capture instead of queueing it without bound', async () => {
  const h = harness();
  const ws = await live(h);
  ws.bufferedAmount = 48001;
  h.engines[0].capture(new ArrayBuffer(960));
  h.engines[0].capture(new ArrayBuffer(960));
  ws.bufferedAmount = 0;
  h.engines[0].capture(new ArrayBuffer(960));
  expect(ws.sent.filter((d) => typeof d !== 'string')).toHaveLength(1);
  expect(h.c.state().droppedFrames).toBe(2);
});

test("the server's barge-in (state with interrupt) clears playback; playback drives speaking/listening", async () => {
  const h = harness();
  const ws = await live(h);
  const e = h.engines[0];
  e.onPlayback({ type: 'playing' });
  expect(h.c.state().phase).toBe('speaking');
  ws.control({ type: 'state', state: 'listening', interrupt: true });
  expect(e.calls.filter((x) => x === 'interrupt')).toHaveLength(1);
  expect(h.c.state().phase).toBe('listening');
  ws.control({ type: 'state', state: 'consulting' });
  expect(h.c.state()).toMatchObject({ phase: 'consulting', asks: 1 });
  ws.control({ type: 'needs_tap', item_id: 'ask-7', kind: 'ask' });
  ws.control({ type: 'passed', delivery_id: 'd1', summary: 'Ship it', queued: true });
  ws.control({ type: 'passed', delivery_id: 'd1', summary: 'Ship it', queued: true });
  expect(h.c.state().needsTap).toEqual({ itemId: 'ask-7', kind: 'ask' });
  expect(h.c.state().passed).toEqual([{ deliveryId: 'd1', summary: 'Ship it', queued: true }]);
});

test('every call gets a fresh audio engine, and the previous one is stopped', async () => {
  const h = harness({ replies: [
    { status: 201, body: { call_id: 'c1', provider: 'grok-plan', billing: { kind: 'plan_unverified' }, ws_url: '/api/voice/calls/c1/ws' } },
    { status: 201, body: { call_id: 'c2', provider: 'grok-plan', billing: { kind: 'plan_unverified' }, ws_url: '/api/voice/calls/c2/ws' } },
  ] });
  const ws = await live(h);
  ws.control({ type: 'ended', reason: 'end_call' });
  expect(h.c.state()).toMatchObject({ phase: 'ended', message: '' });
  expect(h.engines[0].calls).toContain('stop');
  const p = h.c.start();
  await flush();
  h.sockets[1].open();
  await flush();
  await p;
  expect(h.engines).toHaveLength(2);
  expect(h.engines[1].calls).toContain('start');
  expect(h.sockets[1].url).toBe('wss://moa.test/api/voice/calls/c2/ws');
});

test('hanging up while the call is still being created opens nothing afterwards', async () => {
  let release;
  const h = harness();
  const created = { status: 201, body: { call_id: 'c1', provider: 'grok-plan', billing: { kind: 'plan_unverified' }, ws_url: '/api/voice/calls/c1/ws' } };
  h.c.fetch = (url) => new Promise((resolve) => { release = () => resolve(new Response(JSON.stringify(created.body), { status: 201 })); h.requests.push({ url }); });
  const p = h.c.start();
  await flush();
  await h.c.hangup();
  release();
  await p;
  await flush();
  expect(h.sockets).toHaveLength(0);
  expect(h.c.state().phase).toBe('ended');
});

test('failures read as next steps, never as the server body', () => {
  for (const [error, reason] of [['plan_unavailable', 'no_plan'], ['plan_unavailable', 'unauthorized'], ['call_active', ''],
    ['rate_limited', ''], ['provider_unavailable', 'forbidden'], ['http_500', ''], ['plan_unavailable', '{"error":"x"}']]) {
    const msg = startFailureMessage(error, reason);
    expect(msg).not.toMatch(/[{}"]/);
    expect(msg.length).toBeGreaterThan(10);
  }
  expect(startFailureMessage('plan_unavailable', 'no_plan')).toContain('Settings');
  expect(billingLabel({ kind: 'plan_unverified' }, 'grok-plan')).toBe('Uses your Grok plan');
  expect(billingLabel({ kind: 'plan_unverified' }, 'chatgpt-plan')).toBe('Uses your ChatGPT plan');
  expect(billingLabel({ kind: 'plan_unverified' })).toBe('Uses your plan');
  expect(billingLabel({ kind: 'per_minute', usd_per_minute: 0.08 })).toBe('$0.08/min');
  expect(billingLabel({ kind: 'mystery' })).toBe('');
});

test('a microphone refusal hangs up the server call and says what to do', async () => {
  const h = harness();
  const p = h.c.start();
  await flush();
  h.engines[0].start = async () => { throw new Error('NotAllowedError'); };
  h.sockets[0].open();
  await flush();
  await p;
  expect(h.sockets[0].sent.map((d) => JSON.parse(d))).toEqual([{ type: 'hangup' }]);
  expect(h.c.state()).toMatchObject({ phase: 'ended', active: false });
  expect(h.c.state().message).toContain('microphone');
  expect(h.engines[0].calls).toContain('stop');
});
