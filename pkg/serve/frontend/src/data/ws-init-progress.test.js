import { test, expect, beforeEach, afterEach } from 'bun:test';
import { store, setState } from './store.js';
import {
  INIT_IDLE_TIMEOUT_MS, afterPendingInits, reconnectAll, retryHistoryHydration, syncConnections,
} from './api.js';

// A fake clock: timers fire only when advance() moves past their due time, in
// due order, so a test can walk a slow transfer second by second.
let now = 0;
let timers = [];
let originals;

function advance(ms) {
  const target = now + ms;
  for (;;) {
    timers.sort((a, b) => a.due - b.due);
    const next = timers[0];
    if (!next || next.due > target) break;
    timers.shift();
    now = next.due;
    next.callback();
  }
  now = target;
}

class TestWebSocket {
  constructor(url) {
    this.url = url;
    this.closed = false;
    TestWebSocket.instances.push(this);
  }
  close() {
    if (this.closed) return;
    this.closed = true;
    this.onclose?.();
  }
}

beforeEach(() => {
  now = 0;
  timers = [];
  TestWebSocket.instances = [];
  originals = {
    WebSocket: globalThis.WebSocket, location: globalThis.location,
    setTimeout: globalThis.setTimeout, clearTimeout: globalThis.clearTimeout,
  };
  globalThis.WebSocket = TestWebSocket;
  globalThis.location = { protocol: 'http:', host: 'localhost' };
  globalThis.setTimeout = (callback, delay) => {
    const timer = { callback, delay, due: now + delay };
    timers.push(timer);
    return timer;
  };
  globalThis.clearTimeout = (timer) => {
    timers = timers.filter((t) => t !== timer);
  };
  setState({ sessions: { s1: { id: 's1', messages: [], subagents: {} } }, activeSession: null, isMobile: true });
});

afterEach(() => {
  syncConnections([]);
  Object.assign(globalThis, originals);
});

const encoder = new TextEncoder();

function initJSON() {
  return JSON.stringify({ type: 'init', data: {
    messages: [{ role: 'user', content: [{ type: 'text', text: 'x'.repeat(300) }], _msg_id: 'm1' }],
    subagents: [], server_instance: 'instance-a', attention_namespace: 'instance-a:1',
  } });
}

// Split the init's bytes the way the server does: an announcement, then parts.
function initParts(count) {
  const bytes = encoder.encode(initJSON());
  const size = Math.ceil(bytes.length / count);
  const parts = [];
  for (let offset = 0; offset < bytes.length; offset += size) {
    parts.push(bytes.slice(offset, offset + size).buffer);
  }
  return { begin: JSON.stringify({ type: 'init_begin', data: { parts: parts.length, bytes: bytes.length } }), parts };
}

function lastSocket() {
  return TestWebSocket.instances.at(-1);
}

test('the socket asks for a chunked init', () => {
  syncConnections(['s1']);
  expect(lastSocket().url).toContain('init_chunks=1');
  expect(lastSocket().binaryType).toBe('arraybuffer');
});

test('an init that keeps arriving is never cut, however long it takes in total', () => {
  syncConnections(['s1']);
  const ws = lastSocket();
  const { begin, parts } = initParts(6);

  // Handshake, announcement and every part each land just inside the idle
  // deadline: the whole transfer takes several deadlines' worth of time.
  const gap = INIT_IDLE_TIMEOUT_MS - 1000;
  advance(gap);
  ws.onopen();
  advance(gap);
  ws.onmessage({ data: begin });
  for (const part of parts) {
    advance(gap);
    ws.onmessage({ data: part });
  }

  expect(now).toBeGreaterThan(5 * INIT_IDLE_TIMEOUT_MS);
  expect(ws.closed).toBe(false);
  expect(TestWebSocket.instances).toHaveLength(1);
  expect(store.get().sessions.s1).toMatchObject({ historyPending: false, historyHydrated: true, historyStale: false });
  expect(store.get().sessions.s1.messages.map((m) => m._msg_id)).toEqual(['m1']);
  // Nothing is left armed that could cut the now-live socket later.
  advance(10 * INIT_IDLE_TIMEOUT_MS);
  expect(ws.closed).toBe(false);
});

test('a socket that stops making progress is cut at the idle deadline and retried', () => {
  syncConnections(['s1']);
  const ws = lastSocket();
  const { begin, parts } = initParts(3);
  ws.onopen();
  ws.onmessage({ data: begin });
  ws.onmessage({ data: parts[0] });

  advance(INIT_IDLE_TIMEOUT_MS - 1);
  expect(ws.closed).toBe(false);
  advance(1);
  expect(ws.closed).toBe(true);
  expect(store.get().sessions.s1).toMatchObject({ historyPending: false, historyStale: true });

  // The retry starts from scratch after the backoff, not before.
  advance(999);
  expect(TestWebSocket.instances).toHaveLength(1);
  advance(1);
  expect(TestWebSocket.instances).toHaveLength(2);
});

test('a socket on which nothing arrives at all is cut at the deadline', () => {
  syncConnections(['s1']);
  const ws = lastSocket();
  advance(INIT_IDLE_TIMEOUT_MS);
  expect(ws.closed).toBe(true);
  expect(store.get().sessions.s1.historyStale).toBe(true);
});

test('failing attempts back off up to the cap, even across foreground reconnects and Retry now', () => {
  syncConnections(['s1']);
  const delays = [];
  const failAndMeasureRetry = () => {
    const before = TestWebSocket.instances.length;
    lastSocket().close();
    const start = now;
    while (TestWebSocket.instances.length === before) advance(100);
    delays.push(now - start);
  };

  failAndMeasureRetry();
  failAndMeasureRetry();
  reconnectAll(); // foreground: opens at once but must not forget the backoff
  failAndMeasureRetry();
  retryHistoryHydration('s1'); // Retry now: same
  failAndMeasureRetry();
  failAndMeasureRetry();
  failAndMeasureRetry();
  expect(delays).toEqual([1000, 2000, 4000, 8000, 16000, 16000]);

  // An accepted init is what earns a quick retry again.
  lastSocket().onmessage({ data: initJSON() });
  failAndMeasureRetry();
  expect(delays.at(-1)).toBe(1000);
});

test('a malformed chunked transfer closes the socket instead of waiting on it', () => {
  syncConnections(['s1']);
  lastSocket().onmessage({ data: new ArrayBuffer(4) }); // a part without an announcement
  expect(TestWebSocket.instances[0].closed).toBe(true);

  advance(1000);
  const { begin } = initParts(2);
  lastSocket().onmessage({ data: begin });
  lastSocket().onmessage({ data: JSON.stringify({ type: 'state_change', seq: 5, data: {} }) }); // interleaved
  expect(TestWebSocket.instances[1].closed).toBe(true);

  advance(2000);
  const oversized = initParts(2);
  const announced = JSON.parse(oversized.begin);
  announced.data.bytes -= 1;
  lastSocket().onmessage({ data: JSON.stringify(announced) });
  lastSocket().onmessage({ data: oversized.parts[0] });
  lastSocket().onmessage({ data: oversized.parts[1] });
  expect(TestWebSocket.instances[2].closed).toBe(true);
  expect(store.get().sessions.s1.historyHydrated).toBe(false);
});

test('refreshes wait for a pending init and run once it lands', () => {
  syncConnections(['s1']);
  const ran = [];
  afterPendingInits('foreground', () => ran.push('foreground'));
  afterPendingInits('poll', () => ran.push('poll-1'));
  afterPendingInits('poll', () => ran.push('poll-2')); // a later tick replaces the earlier one
  expect(ran).toEqual([]);

  lastSocket().onmessage({ data: initJSON() });
  expect(ran).toEqual(['foreground', 'poll-2']);

  // With every socket live, nothing waits.
  afterPendingInits('poll', () => ran.push('poll-3'));
  expect(ran.at(-1)).toBe('poll-3');
});

test('a failed init releases the refreshes it was holding back', () => {
  syncConnections(['s1']);
  let ran = 0;
  afterPendingInits('foreground', () => { ran += 1; });
  advance(INIT_IDLE_TIMEOUT_MS);
  expect(ran).toBe(1);
});
