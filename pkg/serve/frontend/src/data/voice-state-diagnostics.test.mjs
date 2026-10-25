import test from 'node:test';
import assert from 'node:assert/strict';
import { VoiceCallController } from './voice-call.js';
import { fixture } from './voice-state-diagnostics-fixture.mjs';
import { createNativeCallDeps } from './voice-call-native.js';

test('diagnostic chronology distinguishes real ring drain/refill, clear, and plain server override', async () => {
  const h = await fixture();
  try {
    h.feed(); h.render(5); h.feed(24000); h.render();
    h.c.receive(JSON.stringify({ type: 'state', state: 'listening', interrupt: true }));
    h.feed(24000); h.render();
    h.c.receive(JSON.stringify({ type: 'state', state: 'listening' }));
    h.flush();
    const report = h.sent.find((r) => r.type === 'audio_metrics');
    assert.ok(report, 'server activation must enable numeric UI reports');
    assert.equal(report.client, 2);
    const edges = report.trace.filter((r) => r[2] === 20);
    assert.deepEqual(edges.map((r) => [r[3], r[5], r[6]]), [
      [3, 2, 3], [1, 3, 4], [2, 4, 3], [1, 3, 4], [4, 4, 3], [1, 3, 4], [3, 4, 3],
    ]);
    assert.ok(report.trace.some((r) => r[2] === 21 && r[3] === 4));
    assert.ok(report.trace.every((r, i, a) => r.length === 8 && r.every(Number.isSafeInteger)
      && (!i || (r[0] > a[i - 1][0] && r[1] >= a[i - 1][1]))));
    assert.ok(!JSON.stringify(report).includes('synthetic'));
  } finally { h.c.finish('hangup'); }
});

test('diagnostic OFF preserves ring/controller behavior and sends nothing', async () => {
  const h = await fixture(false);
  try {
    h.feed(); h.render(5); h.feed(); h.render(5); h.flush();
    assert.deepEqual(h.phases.slice(-4), ['speaking', 'listening', 'speaking', 'listening']);
    assert.deepEqual(h.sent, []);
  } finally { h.c.finish('hangup'); }
});

test('diagnostic UI trace is bounded and counts dropped observations under socket backlog', async () => {
  const h = await fixture();
  try {
    h.socket.bufferedAmount = 48001;
    for (let i = 0; i < 40; i++) { h.feed(); h.render(5); }
    h.flush(); assert.equal(h.sent.length, 0);
    h.socket.bufferedAmount = 0; h.flush();
    assert.equal(h.sent.length, 1);
    assert.equal(h.sent[0].trace.length, 24);
    assert.ok(h.sent[0].trace_dropped >= 57);
    assert.ok(h.sent[0].reports_skipped >= 1);
    assert.ok(JSON.stringify(h.sent[0]).length <= 4096);
  } finally { h.c.finish('hangup'); }
});

test('stalled native transport admits zero UI reports over 60 ticks and preserves the ordinary mirror', async () => {
  const sent = [], actions = [];
  let tick, time = 0;
  const bridge = {
    open: () => 'local', send: (_id, data) => sent.push(JSON.parse(data)),
    close() { actions.push('close'); }, async startAudio() { actions.push('start'); },
    stopAudio() { actions.push('stop'); }, setMuted(_id, muted) { actions.push(`muted:${muted}`); },
  };
  const deps = createNativeCallDeps(bridge);
  const c = new VoiceCallController({ ...deps, sessionId: 'synthetic',
    fetch: async () => ({ status: 201, json: async () => ({ call_id: 'synthetic' }) }),
  });
  const original = globalThis.setInterval;
  const originalPerformance = Object.getOwnPropertyDescriptor(globalThis, 'performance');
  globalThis.setInterval = (fn) => { tick = fn; return 1; };
  Object.defineProperty(globalThis, 'performance', { configurable: true, value: { now: () => time } });
  try {
    const start = c.start();
    for (let i = 0; i < 8; i++) await Promise.resolve();
    bridge.onevent({ id: 'local', type: 'open' }); await start;
    const control = (msg) => bridge.onevent({ id: 'local', type: 'message', data: JSON.stringify(msg) });
    control({ type: 'state', state: 'listening', audio_diagnostics: { v: 1, active: true, interval_ms: 1000 } });
    bridge.onevent({ id: 'local', type: 'playback', data: 'playing' });
    assert.equal(c.state().phase, 'speaking');
    bridge.onevent({ id: 'local', type: 'playback', data: 'drained' });
    assert.equal(c.state().phase, 'listening');
    control({ type: 'state', state: 'consulting' });
    assert.equal(c.state().phase, 'consulting');
    control({ type: 'state', state: 'listening', interrupt: true });
    assert.equal(c.state().phase, 'listening');
    c.receive(new ArrayBuffer(960)); // Native audio remains outside the page.
    for (let i = 0; i < 60; i++) { time += 1000; tick?.(); }
    assert.equal(sent.filter((r) => r.type === 'audio_metrics' && r.client === 2).length, 0,
      'fire-and-forget native bridge must not admit periodic UI reports');
    assert.equal(tick, undefined, 'unsupported transport must not create a diagnostic timer');
    c.setMuted(true);
    c.sendJSON({ type: 'ping' });
    assert.deepEqual(sent, [{ type: 'mute', muted: true }, { type: 'ping' }]);
    bridge.onevent({ id: 'local', type: 'mute', data: false });
    assert.equal(c.state().muted, false);
    assert.deepEqual(actions, ['start', 'muted:true']);
    c.finish('hangup');
    assert.deepEqual(actions, ['start', 'muted:true', 'stop', 'close']);
  } finally {
    globalThis.setInterval = original;
    Object.defineProperty(globalThis, 'performance', originalPerformance);
    c.finish('hangup');
  }
});
