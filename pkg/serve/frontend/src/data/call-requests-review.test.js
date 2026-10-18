import { test, expect } from 'bun:test';
import { setCallRequests, startFeedPresence } from './call-requests.js';
import { installOpenSessionNavigation } from './push-navigation.js';
import { presenceFrame } from './presence.js';

// R3: an older snapshot's sweep still waiting for the registration must not
// close the notification of a request that went live after it.
test('an older in-flight snapshot must not close a newly-live call notification', async () => {
  const previous = Object.getOwnPropertyDescriptor(globalThis, 'navigator');
  const n = { data: { kind: 'call_request', request_id: 'new-request' }, closed: false, close() { this.closed = true; } };
  let release;
  let calls = 0;
  const registration = { getNotifications: async () => [n] };
  Object.defineProperty(globalThis, 'navigator', {
    configurable: true,
    value: { serviceWorker: { getRegistration: () => (++calls === 1 ? new Promise((r) => { release = r; }) : Promise.resolve(registration)) } },
  });
  try {
    setCallRequests([]); // older snapshot, registration lookup pending
    setCallRequests([{ id: 'new-request', state: 'ringing' }]); // newer one; its push is shown
    await new Promise((r) => setTimeout(r, 0));
    expect(n.closed).toBe(false);
    release(registration);
    await new Promise((r) => setTimeout(r, 0));
    expect(n.closed).toBe(false);
  } finally {
    if (previous) Object.defineProperty(globalThis, 'navigator', previous);
    else delete globalThis.navigator;
  }
});

// R4: ACK only when something opened, so the service worker's own fallback
// runs when nothing did.
test('a failed warm ended-request fallback does not ACK success', async () => {
  let handler;
  const acks = [];
  const sw = { addEventListener: (_, fn) => { handler = fn; }, removeEventListener() {} };
  const stop = installOpenSessionNavigation({ serviceWorker: sw, openCallRequest: async () => false, selectSession: () => false });
  handler({ data: { type: 'open-call-request', callRequestId: 'ended-request', sessionId: 'owner-not-loaded', requestId: 'tap-1' }, ports: [{ postMessage: (m) => acks.push(m) }] });
  await new Promise((r) => setTimeout(r, 0));
  stop();
  expect(acks).toEqual([]);
});

// R1: the requests feed tells the server whether this document is visible,
// with the renewal the conversation presence uses.
test('the requests feed reports document visibility and renews it', () => {
  const sent = [];
  const ws = { readyState: 1, send: (m) => sent.push(m) };
  const listeners = {};
  const doc = {
    visibilityState: 'visible',
    addEventListener: (t, f) => { listeners[t] = f; },
    removeEventListener: (t) => { delete listeners[t]; },
  };
  const timers = [];
  const stop = startFeedPresence(ws, doc, { setInterval: (f) => { timers.push(f); return timers.length; }, clearInterval() {} });
  expect(sent).toEqual([presenceFrame(true)]);
  doc.visibilityState = 'hidden';
  listeners.visibilitychange();
  expect(sent.at(-1)).toBe(presenceFrame(false));
  doc.visibilityState = 'visible';
  listeners.visibilitychange();
  timers[0]();
  expect(sent.slice(-2)).toEqual([presenceFrame(true), presenceFrame(true)]);
  stop();
  expect(listeners.visibilitychange).toBeUndefined();
});
