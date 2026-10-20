// for-you.test.js — the For you slice: what the doors count, an answered row
// leaving at once, and News read at the revision the client showed. Run with
// `bun test`.

import { test, expect, afterEach } from 'bun:test';
import { store, setState } from './store.js';
import { __resetForYouForTests, forYouCount, forYouSlice, forgetForYouItem, loadForYou, markNewsRead, orderWaiting, unreadNewsCount } from './for-you.js';
import { waitsForYou } from './util/format.js';

const originalFetch = globalThis.fetch;
let calls = [];
let reply = { items: [], news: [] };
function serve() {
  __resetForYouForTests();
  calls = [];
  globalThis.fetch = (path, init = {}) => {
    calls.push({ path: String(path), method: init.method || 'GET', body: init.body ? JSON.parse(init.body) : undefined });
    if (init.method === 'POST') return Promise.resolve(new Response(JSON.stringify({ marked: 1 }), { status: 200 }));
    return Promise.resolve(new Response(JSON.stringify(reply), { status: 200 }));
  };
}
afterEach(() => { globalThis.fetch = originalFetch; setState({ forYou: undefined }); });

const news = (id, revision, read = false) => ({ id, revision, session_id: 's1', kind: 'info', title: id, ...(read ? { read_at: '2026-10-19T09:00:00Z' } : {}) });

test('the doors count what waits and the unread news', async () => {
  serve();
  reply = { items: [{ id: 'q1', kind: 'question', session_id: 's1' }, { id: 'p1', kind: 'permission', session_id: 's2', blocking: true }], news: [news('n1', 1), news('n2', 3, true)] };
  await loadForYou();
  expect(forYouCount(store.get())).toBe(2);
  expect(unreadNewsCount(store.get())).toBe(1);
});

test('an answered row leaves at once, before the refresh confirms it', async () => {
  serve();
  reply = { items: [{ id: 'q1' }, { id: 'q2' }], news: [] };
  await loadForYou();
  reply = { items: [{ id: 'q2' }], news: [] };
  forgetForYouItem('q1');
  expect(forYouSlice(store.get()).items.map((i) => i.id)).toEqual(['q2']);
});

test('a response without news keeps the news shown', async () => {
  serve();
  reply = { items: [], news: [news('n1', 1)] };
  await loadForYou();
  reply = { items: [] };
  await loadForYou();
  expect(forYouSlice(store.get()).news.map((n) => n.id)).toEqual(['n1']);
});

test('reading sends each item at the revision shown; Mark all read is the snapshot of unread', async () => {
  serve();
  reply = { items: [], news: [news('n1', 2), news('n2', 1), news('n3', 1, true)] };
  await loadForYou();
  const shown = forYouSlice(store.get()).news;
  const done = markNewsRead([shown[0]]);
  expect(forYouSlice(store.get()).news.find((n) => n.id === 'n1').read_at).toBeTruthy();
  await done;
  expect(calls.find((c) => c.method === 'POST').body).toEqual({ items: [{ id: 'n1', revision: 2 }] });

  calls = [];
  await markNewsRead(shown.filter((n) => !n.read_at && n.id !== 'n1'));
  expect(calls.find((c) => c.method === 'POST').body).toEqual({ items: [{ id: 'n2', revision: 1 }] });

  calls = [];
  await markNewsRead([]);
  expect(calls.some((c) => c.method === 'POST')).toBe(false);
});

test('a newer revision arriving meanwhile is not flipped read locally', async () => {
  serve();
  reply = { items: [], news: [news('n1', 2)] };
  await loadForYou();
  calls = [];
  await markNewsRead([news('n1', 1)]);
  expect(calls.find((c) => c.method === 'POST').body).toEqual({ items: [{ id: 'n1', revision: 1 }] });
  expect(forYouSlice(store.get()).news[0].read_at).toBeUndefined();
});

test('a session waits for you with a blocking request or an open async question from the roster', () => {
  expect(waitsForYou({ state: 'idle' })).toBe(false);
  expect(waitsForYou({ state: 'idle', questionIds: ['q1'] })).toBe(true);
  expect(waitsForYou({ state: 'idle', questionIds: [] })).toBe(false);
  expect(waitsForYou({ state: 'permission' })).toBe(true);
  expect(waitsForYou({ state: 'idle', pendingSecrets: [{ id: 'x' }] })).toBe(true);
});

test('opening a session from the sidebar while For you shows leaves For you', async () => {
  const { openSession } = await import('./tile-actions.js');
  const { openForYouView } = await import('./for-you.js');
  globalThis.fetch = () => Promise.resolve(new Response(JSON.stringify({ items: [], news: [] }), { status: 200 }));
  setState({ isMobile: true, view: null, sessions: { s1: { id: 's1', title: 'One', messages: [] } } });
  openForYouView();
  expect(store.get().view).toBe('foryou');
  expect(openSession('s1')).toBe(true);
  expect(store.get().view).not.toBe('foryou');
  setState({ isMobile: false, sessions: {} });
});

test('orderWaiting puts blocking items before task requests, newest first in each group', () => {
  const mk = (id, kind, since) => ({ id, kind, since });
  const out = orderWaiting([
    mk('t-old', 'task_request', '2026-10-20T08:00:00Z'),
    mk('q-old', 'question', '2026-10-20T07:00:00Z'),
    mk('t-new', 'task_request', '2026-10-20T10:00:00Z'),
    mk('s-new', 'secret', '2026-10-20T09:00:00Z'),
  ]).map((i) => i.id);
  expect(out).toEqual(['s-new', 'q-old', 't-new', 't-old']);
});
