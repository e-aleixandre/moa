import { test, expect, afterAll } from 'bun:test';
import { startCallRequestsSync, currentCallRequests, setCallRequests, writeCallRequest, callHeldRequest } from './data/call-requests.js';
import { incomingRequest } from './data/call-requests-model.js';
import { shouldRing } from './data/ring-tone.js';

const original = { fetch: globalThis.fetch, WebSocket: globalThis.WebSocket, location: Object.getOwnPropertyDescriptor(globalThis, 'location') };
afterAll(() => {
  globalThis.fetch = original.fetch;
  globalThis.WebSocket = original.WebSocket;
  if (original.location) Object.defineProperty(globalThis, 'location', original.location); else delete globalThis.location;
});
const req = { id: 'r1', owner_id: 'o1', owner_name: 'Infra', session_id: 's1', level: 'ring', state: 'ringing', reason: 'Deploy blocked', expires_at: new Date(Date.now() + 45000).toISOString() };
let socket;
class WS {
  constructor() { this.readyState = 1; socket = this; }
  message(requests) { this.onmessage?.({ data: JSON.stringify({ type: 'call_requests', requests }) }); }
  close() { this.readyState = 3; }
}

// One lifecycle mirrors bootstrap (GET and WS simultaneously), followed by
// foreground return, with only transport delivery order under test control.
test('F08 UI3: an older GET must not resurrect a request removed by a newer WS snapshot', async () => {
  let finishGET;
  globalThis.location = { protocol: 'https:', host: 'moa.test' };
  globalThis.WebSocket = WS;
  globalThis.fetch = () => new Promise((resolve) => { finishGET = resolve; });
  startCallRequestsSync();
  socket.message([req]);
  socket.message([]); // owner stopped / someone else answered in the meantime
  expect(currentCallRequests()).toEqual([]);
  finishGET(Response.json({ requests: [req] })); // initial GET was already serialized
  await new Promise((r) => setTimeout(r, 5));
  console.log('UI3 after newer WS then delayed GET', JSON.stringify(currentCallRequests()));
  expect(incomingRequest(currentCallRequests())).toBeNull();
});

test('F08 UI4: an expired incoming request must stop ringing while the feed is disconnected', async () => {
  const past = { ...req, expires_at: new Date(Date.now() - 1000).toISOString() };
  setCallRequests([past]); // cached last valid feed snapshot, no network now
  const incoming = incomingRequest(currentCallRequests());
  console.log('UI4 past expires_at still has ringing tone', shouldRing(incoming, true));
  expect(shouldRing(incoming, true)).toBe(false);
});

test('F11: a network failure cancelling a held request rejects and keeps the request', async () => {
  const held = { ...req, state: 'held' };
  setCallRequests([held]);
  globalThis.fetch = async () => { throw new TypeError('offline'); };
  await expect(writeCallRequest(held.id)).rejects.toThrow('offline');
  expect(currentCallRequests()[0]?.state).toBe('held');
});

test('F11: a failure choosing held Call rejects before touching the current call', async () => {
  const calls = [];
  globalThis.fetch = async (path) => { calls.push(path); throw new TypeError('offline'); };
  await expect(callHeldRequest({ ...req, state: 'held' })).rejects.toThrow('offline');
  expect(calls).toHaveLength(1);
  expect(calls[0]).toContain('/accept');
});

test('F11: only the 409 of an already-resolved request is silent', async () => {
  const held = { ...req, state: 'held' };
  setCallRequests([held]);
  globalThis.fetch = async () => new Response('gone', { status: 409 });
  expect(await writeCallRequest(held.id)).toBe(true);
  expect(await callHeldRequest(held)).toBe(false);
  globalThis.fetch = async () => new Response('boom', { status: 500 });
  await expect(writeCallRequest(held.id)).rejects.toMatchObject({ status: 500 });
  await expect(callHeldRequest(held)).rejects.toMatchObject({ status: 500 });
  expect(currentCallRequests()[0]?.state).toBe('held');
});

test('F08: ringing stops at expires_at, held never lapses, and the timer is computed from it', async () => {
  const { ringExpired, msUntilExpiry } = await import('./data/call-requests-model.js');
  const now = Date.now();
  const ringing = { ...req, expires_at: new Date(now + 5000).toISOString() };
  expect(shouldRing(ringing, true, now)).toBe(true);
  expect(shouldRing(ringing, true, now + 6000)).toBe(false);
  expect(msUntilExpiry(ringing, now)).toBe(5000);
  expect(incomingRequest([ringing], now + 6000)).toBeNull();
  const held = { ...req, state: 'held', expires_at: new Date(now - 1000).toISOString() };
  expect(ringExpired(held, now)).toBe(false);
  expect(msUntilExpiry(held, now)).toBeNull();
});
