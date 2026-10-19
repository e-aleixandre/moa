// for-you-session.test.js — For you of ONE conversation: which sessions it
// answers for (an owner's include its sessions), the three states of the
// header button and the label that says them.
import { test, expect } from 'bun:test';
import { forYouForSession, forYouScopeIds, sessionForYouLabel } from './for-you-session.js';

const sessions = {
  own: { id: 'own', kind: 'owner' },
  c1: { id: 'c1', origin: 'owner', ownerId: 'o1' },
  c2: { id: 'c2', origin: 'owner', ownerId: 'o2' },
  solo: { id: 'solo' },
};
const owners = [{ id: 'o1', session_id: 'own' }, { id: 'o2', session_id: 'own2' }];
const read = '2026-10-19T09:00:00Z';
const state = (items, news) => ({ sessions, forYou: { items, news, loaded: true } });
const q = (id, session_id) => ({ id, kind: 'question', session_id });
const n = (id, session_id, read_at) => ({ id, revision: 1, session_id, kind: 'info', title: id, ...(read_at ? { read_at } : {}) });

test('an ordinary session answers for itself only', () => {
  expect([...forYouScopeIds('solo', sessions, owners)]).toEqual(['solo']);
  expect([...forYouScopeIds('c1', sessions, owners)]).toEqual(['c1']);
});

test('an owner answers for itself and the sessions that report to it', () => {
  expect([...forYouScopeIds('own', sessions, owners)].sort()).toEqual(['c1', 'own']);
});

test('a session with a question open shows 1 and only its own item', () => {
  const v = forYouForSession(state([q('a', 'solo'), q('b', 'c2')], []), 'solo', owners);
  expect(v.waiting).toBe(1);
  expect(v.items.map((i) => i.id)).toEqual(['a']);
});

test("an owner counts a child's permission", () => {
  const perm = { id: 'p', kind: 'permission', session_id: 'c1', blocking: true };
  const v = forYouForSession(state([perm, q('x', 'c2')], []), 'own', owners);
  expect(v.waiting).toBe(1);
  expect(v.items[0].id).toBe('p');
});

test('unread News make the dot; read ones stay in the list but do not count', () => {
  const s = state([], [n('n1', 'solo'), n('n2', 'solo', read), n('n3', 'c2')]);
  const v = forYouForSession(s, 'solo', owners);
  expect(v.unread).toBe(1);
  expect(v.news.map((x) => x.id).sort()).toEqual(['n1', 'n2']);
});

test('reading the only unread News turns the dot off', () => {
  const v = forYouForSession(state([], [n('n1', 'solo', read)]), 'solo', owners);
  expect(v.unread).toBe(0);
  expect(v.news).toHaveLength(1);
});

test('a session with nothing is quiet but still has its read News', () => {
  const v = forYouForSession(state([q('b', 'c2')], [n('n2', 'solo', read)]), 'solo', owners);
  expect([v.waiting, v.unread, v.news.length]).toEqual([0, 0, 1]);
});

test('the label says the three states in words', () => {
  expect(sessionForYouLabel(0, 0)).toBe('For you in this conversation');
  expect(sessionForYouLabel(2, 0)).toBe('For you in this conversation, 2 waiting');
  expect(sessionForYouLabel(0, 1)).toBe('For you in this conversation, 1 new');
  expect(sessionForYouLabel(2, 1)).toBe('For you in this conversation, 2 waiting, 1 new');
});
