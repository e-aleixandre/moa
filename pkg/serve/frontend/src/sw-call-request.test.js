import { test, expect } from 'bun:test';
import { readFileSync } from 'node:fs';

// The worker is copied verbatim by the build and shares no module with the
// app, so the real file is evaluated against a fake worker scope.
const source = readFileSync(new URL('./sw.js', import.meta.url), 'utf8');

function worker({ clients = [] } = {}) {
  const handlers = {};
  const shown = [];
  const opened = [];
  const self = {
    location: { origin: 'https://moa.test' },
    addEventListener: (type, fn) => { handlers[type] = fn; },
    registration: { showNotification: (title, options) => { shown.push({ title, options }); return Promise.resolve(); } },
    clients: {
      matchAll: async () => clients,
      openWindow: async (url) => { opened.push(url); },
      claim: async () => {},
    },
    skipWaiting() {},
  };
  new Function('self', source)(self);
  const run = async (type, event) => {
    const waits = [];
    handlers[type]({ ...event, waitUntil: (p) => waits.push(p) });
    await Promise.all(waits);
  };
  return { shown, opened, run };
}

const push = (data) => ({ data: { json: () => data } });
const callPush = {
  title: 'Infra is calling', body: 'Infra', session_id: 's1', tag: 'req:s1',
  kind: 'call_request', level: 'urgent', request_id: 'cr-1',
};

test('a call_request push stays until touched and remembers its request', async () => {
  const w = worker();
  await w.run('push', push(callPush));
  expect(w.shown).toHaveLength(1);
  const { options } = w.shown[0];
  expect(options.requireInteraction).toBe(true);
  expect(options.tag).toBe('req:s1');
  expect(options.data).toMatchObject({ session_id: 's1', kind: 'call_request', request_id: 'cr-1' });
});

test('other pushes are untouched: no requireInteraction, no request id', async () => {
  const w = worker();
  await w.run('push', push({ title: 'moa needs your decision', session_id: 's1', tag: 'req:s1', kind: 'ask', level: 'urgent' }));
  expect(w.shown[0].options.requireInteraction).toBeFalsy();
  expect(w.shown[0].options.data.request_id || '').toBe('');
});

function click(data) {
  let closed = false;
  return { notification: { data, close: () => { closed = true; } }, isClosed: () => closed };
}

test('tapping a call notification with no window opens its deep link, with the owner as fallback', async () => {
  const w = worker();
  await w.run('notificationclick', click({ session_id: 's1', kind: 'call_request', request_id: 'cr-1' }));
  expect(w.opened).toHaveLength(1);
  const url = new URL(w.opened[0], 'https://moa.test');
  expect(url.searchParams.get('call_request')).toBe('cr-1');
  expect(url.searchParams.get('session')).toBe('s1');
});

test('tapping it with a window open posts the request to that window', async () => {
  const messages = [];
  const client = {
    url: 'https://moa.test/',
    focus: async () => {},
    postMessage: (msg, ports) => {
      messages.push(msg);
      ports[0].postMessage({ type: 'open-call-request-ack', requestId: msg.requestId });
    },
  };
  const w = worker({ clients: [client] });
  await w.run('notificationclick', click({ session_id: 's1', kind: 'call_request', request_id: 'cr-1' }));
  expect(messages).toHaveLength(1);
  expect(messages[0]).toMatchObject({ type: 'open-call-request', callRequestId: 'cr-1', sessionId: 's1' });
  expect(w.opened).toHaveLength(0);
});

test('an unsafe request id is dropped: the tap just opens the conversation', async () => {
  const w = worker();
  await w.run('notificationclick', click({ session_id: 's1', kind: 'call_request', request_id: '../x?y' }));
  expect(w.opened).toEqual(['/?session=s1']);
});
