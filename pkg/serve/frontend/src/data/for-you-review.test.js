import { afterEach, expect, test } from 'bun:test';
import { store, setState } from './store.js';
import { __resetForYouForTests, loadForYou, markNewsRead, unreadNewsCount, forYouCount, forgetForYouItem } from './for-you.js';
import { spineSessions } from '../layout/Sidebar/sessions.js';
import { partitionByAttention } from './util/project-sessions.js';
import { ownerRows } from './owners-model.js';

const originalFetch = globalThis.fetch;
afterEach(() => { globalThis.fetch = originalFetch; setState({ forYou: undefined }); __resetForYouForTests(); });

function deferred() { let resolve; const promise = new Promise((r) => { resolve = r; }); return { promise, resolve }; }
const response = (body) => new Response(JSON.stringify(body), { status: 200 });

test('review: saved question flows into the same Needs attention partition and owner projection', () => {
  const saved = { id: 's', state: 'saved', questionIds: ['q'], updated: 1, cwd: '/project' };
  const { active, saved: archived } = spineSessions({ s: saved, plain: { id: 'plain', state: 'saved', updated: 2 } });
  expect(active.length).toBe(0);
  const asking = archived.filter((s) => s.state === 'permission');
  const { needs } = partitionByAttention([...active, ...asking]);
  expect(needs.map((n) => n.id)).toEqual(['s']);
  expect(archived.filter((s) => s.state !== 'permission').map((s) => s.id)).toEqual(['plain']);
  expect(ownerRows([{ id: 'o', session_id: 'os', session_state: 'saved', open_question_ids: ['oq'] }], {})[0].session_state).toBe('permission');
});

test('review RED: a GET started before reading must not restore an unread dot after the read succeeds', async () => {
  __resetForYouForTests();
  const stale = deferred();
  const n = { id: 'n', revision: 1, title: 'News' };
  let gets = 0;
  globalThis.fetch = (_path, opts = {}) => {
    if (opts.method === 'POST') return Promise.resolve(response({ marked: 1 }));
    gets++;
    return gets === 1 ? stale.promise : Promise.resolve(response({ items: [], news: [{ ...n, read_at: '2026-10-19T10:00:00Z' }] }));
  };
  setState({ forYou: { items: [], news: [n], loaded: true } });
  const poll = loadForYou();
  const read = markNewsRead([n]);
  expect(unreadNewsCount(store.get())).toBe(0);
  await new Promise((resolve) => setTimeout(resolve, 0));
  expect(gets).toBe(1);
  stale.resolve(response({ items: [], news: [n] }));
  await Promise.all([poll, read]);
  expect(unreadNewsCount(store.get())).toBe(0);
});

test('review RED: an older in-flight GET must not resurrect a question that was just answered', async () => {
  __resetForYouForTests();
  const stale = deferred();
  let gets = 0;
  globalThis.fetch = () => ++gets === 1 ? stale.promise : Promise.resolve(response({ items: [], news: [] }));
  setState({ forYou: { items: [{ id: 'q', session_id: 's' }], news: [], loaded: true } });
  const poll = loadForYou();
  forgetForYouItem('q');
  expect(forYouCount(store.get())).toBe(0);
  stale.resolve(response({ items: [{ id: 'q', session_id: 's' }], news: [] }));
  await poll;
  expect(forYouCount(store.get())).toBe(0);
});
