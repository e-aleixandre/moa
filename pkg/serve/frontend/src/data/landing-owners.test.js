// landing-owners.test.js — run with `bun test`
//
// Where the app lands when nothing is shown. An owner's conversation counts
// like any open session; the sessions an owner dispatched credit their
// activity to that owner rather than being landed on themselves.
import { test, expect, beforeEach, afterEach } from 'bun:test';

const { setState, store, updateSession } = await import('./store.js');
const { createTile, initIds, allSessionIds } = await import('./tileTree.js');
const { autoSelectMobile, autoFillTiles, watchLanding, __resetBootForTests } = await import('./tile-actions.js');
const { landingOrder } = await import('./util/project-sessions.js');

const owners = [
  { id: 'own_a', name: 'A', session_id: 'oa' },
  { id: 'own_b', name: 'B', session_id: 'ob' },
];

let unwatch = null;

beforeEach(() => {
  globalThis.fetch = () => Promise.resolve(new Response('{}', { status: 200 }));
  __resetBootForTests();
  setState({ sessions: {}, activeSession: null, isMobile: false, sessionsLoaded: false, owners: { list: [], loaded: false, error: null, retrying: false } });
});

afterEach(() => {
  unwatch?.();
  unwatch = null;
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

test('an owner turning live in place pulls the empty screen onto it', () => {
  setState({
    isMobile: true,
    sessionsLoaded: true,
    owners: { list: owners, loaded: true, error: null, retrying: false },
    sessions: {
      oa: { id: 'oa', kind: 'owner', state: 'saved', updated: 10, subagents: {} },
      s1: { id: 's1', state: 'saved', updated: 50, subagents: {} },
    },
  });
  unwatch = watchLanding();
  expect(store.get().activeSession).toBe(null);
  // Woken by a report on the server: same roster size, now live.
  updateSession('oa', { state: 'idle', updated: 60 });
  expect(store.get().activeSession).toBe('oa');
});

test('nothing is decided until both lists have loaded', () => {
  setState({
    isMobile: true,
    sessions: {
      oa: { id: 'oa', kind: 'owner', state: 'idle', updated: 5, subagents: {} },
      s1: { id: 's1', state: 'idle', updated: 20, subagents: {} },
      c1: { id: 'c1', origin: 'owner', ownerId: 'own_a', state: 'running', updated: 90, subagents: {} },
    },
  });
  unwatch = watchLanding();
  // Sessions are in, owners are not: landing now would pick s1 over the owner
  // whose child is the most recent work.
  expect(store.get().activeSession).toBe(null);
  setState({ owners: { list: owners, loaded: true, error: null, retrying: false } });
  expect(store.get().activeSession).toBe(null);
  setState({ sessionsLoaded: true });
  expect(store.get().activeSession).toBe('oa');
});

const { createSplit } = await import('./tileTree.js');

function desktop(tree, focusedTile, sessions) {
  setState({
    isMobile: false,
    sessionsLoaded: true,
    tileTree: tree,
    focusedTile,
    owners: { list: owners, loaded: true, error: null, retrying: false },
    sessions,
  });
}

test('desktop: a conversation turning live fills the empty focused pane', () => {
  const tile = createTile();
  initIds(tile);
  desktop(tile, tile.id, { oa: { id: 'oa', kind: 'owner', state: 'saved', updated: 10, subagents: {} } });
  unwatch = watchLanding();
  expect(allSessionIds(store.get().tileTree)).toEqual([]);
  updateSession('oa', { state: 'idle', updated: 60 });
  expect(allSessionIds(store.get().tileTree)).toEqual(['oa']);
});

test('desktop: a pane left empty beside a shown session is not refilled when something turns live', () => {
  const shown = createTile('s1');
  const empty = createTile();
  const tree = createSplit('horizontal', [shown, empty]);
  initIds(tree);
  desktop(tree, shown.id, {
    s1: { id: 's1', state: 'idle', updated: 50, subagents: {} },
    oa: { id: 'oa', kind: 'owner', state: 'saved', updated: 10, subagents: {} },
  });
  unwatch = watchLanding();
  updateSession('oa', { state: 'idle', updated: 60 });
  expect(allSessionIds(store.get().tileTree)).toEqual(['s1']);
});
