// for-you.test.js — the For you slice: what the doors count, an answered row
// leaving at once, and News read at the revision the client showed. Run with
// `bun test`.

import { test, expect, afterEach } from 'bun:test';
import { store, setState } from './store.js';
import { __resetForYouForTests, forYouCount, forYouSlice, forgetForYouItem, loadForYou, markNewsRead, unreadNewsCount } from './for-you.js';
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
