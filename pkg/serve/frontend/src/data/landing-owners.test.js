// landing-owners.test.js — run with `bun test`
//
// Where the app lands when nothing is shown. An owner's conversation counts
// like any open session; the sessions an owner dispatched credit their
// activity to that owner rather than being landed on themselves.
import { test, expect, beforeEach } from 'bun:test';

const { setState, store } = await import('./store.js');
const { createTile, initIds, allSessionIds } = await import('./tileTree.js');
const { autoSelectMobile, autoFillTiles, __resetBootForTests } = await import('./tile-actions.js');
const { landingOrder } = await import('./util/project-sessions.js');

const owners = [
  { id: 'own_a', name: 'A', session_id: 'oa' },
  { id: 'own_b', name: 'B', session_id: 'ob' },
];

beforeEach(() => {
  globalThis.fetch = () => Promise.resolve(new Response('{}', { status: 200 }));
  __resetBootForTests();
  setState({ sessions: {}, activeSession: null, isMobile: false, owners: { list: [], loaded: false, error: null, retrying: false } });
});

test('an open owner is landed on when no ordinary session is open', () => {
  const sessions = {
    oa: { id: 'oa', kind: 'owner', state: 'idle', updated: 10 },
    s1: { id: 's1', state: 'saved', updated: 50 },
  };
  expect(landingOrder(sessions, owners)).toEqual(['oa']);
});

test('owners and ordinary sessions share one recency order', () => {
  const sessions = {
    oa: { id: 'oa', kind: 'owner', state: 'idle', updated: 30 },
    s1: { id: 's1', state: 'idle', updated: 20 },
    s2: { id: 's2', state: 'running', updated: 40 },
  };
  expect(landingOrder(sessions, owners)).toEqual(['s2', 'oa', 's1']);
});

test("a child's activity is credited to its owner, never landed on itself", () => {
  const sessions = {
    oa: { id: 'oa', kind: 'owner', state: 'idle', updated: 5 },
    ob: { id: 'ob', kind: 'owner', state: 'idle', updated: 20 },
    c1: { id: 'c1', origin: 'owner', ownerId: 'own_a', state: 'running', updated: 90 },
    s1: { id: 's1', state: 'idle', updated: 50 },
  };
  expect(landingOrder(sessions, owners)).toEqual(['oa', 's1', 'ob']);
});

test('an owner whose only live work is a child is still a landing target', () => {
  const sessions = {
    oa: { id: 'oa', kind: 'owner', state: 'saved', updated: 5 },
    c1: { id: 'c1', origin: 'owner', ownerId: 'own_a', state: 'running', updated: 90 },
  };
  expect(landingOrder(sessions, owners)).toEqual(['oa']);
});

test('children wait for the owners roster before they count', () => {
  const sessions = { c1: { id: 'c1', origin: 'owner', ownerId: 'own_a', state: 'running', updated: 90 } };
  expect(landingOrder(sessions, [])).toEqual([]);
});

test('a saved owner with nothing live is not landed on', () => {
  const sessions = {
    oa: { id: 'oa', kind: 'owner', state: 'saved', updated: 99 },
    c1: { id: 'c1', origin: 'owner', ownerId: 'own_a', state: 'saved', updated: 90 },
  };
  expect(landingOrder(sessions, owners)).toEqual([]);
});

test('mobile opens the owner instead of the empty state', () => {
  setState({
    isMobile: true,
    owners: { list: owners, loaded: true, error: null, retrying: false },
    sessions: { oa: { id: 'oa', kind: 'owner', state: 'idle', updated: 10, subagents: {} } },
  });
  autoSelectMobile();
  expect(store.get().activeSession).toBe('oa');
});

test('desktop fills the empty tile with the owner', () => {
  const tile = createTile();
  initIds(tile);
  setState({
    tileTree: tile,
    focusedTile: tile.id,
    owners: { list: owners, loaded: true, error: null, retrying: false },
    sessions: { oa: { id: 'oa', kind: 'owner', state: 'idle', updated: 10, subagents: {} } },
  });
  autoFillTiles();
  expect(allSessionIds(store.get().tileTree)).toEqual(['oa']);
});
