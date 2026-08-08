// close-session-reselect.test.js — run with `bun test`
//
// Regression: closing the session you were looking at left the surface empty
// even though other sessions were still open. Closing keeps the session in the
// store (as `saved`), so the app-level effect that re-fills on a session-COUNT
// change never fired — the mobile screen showed "No open sessions" while the
// drawer, reading the store directly, still listed three active ones.
import { test, expect, beforeEach, mock } from 'bun:test';

const realApi = await import('./api.js');
mock.module('./api.js', () => ({
  ...realApi,
  api: async () => ({}),
  syncConnections: () => {},
}));

const { store, setState } = await import('./store.js');
const { createTile, initIds, allSessionIds } = await import('./tileTree.js');
const { closeSession } = await import('./session-actions.js');

// Three open sessions, s1 the most recent (and the one being looked at).
function seed(isMobile) {
  const now = Date.now();
  const tile = createTile();
  initIds(tile);
  setState({
    sessions: {
      s1: { id: 's1', state: 'idle', updated: now, subagents: {} },
      s2: { id: 's2', state: 'idle', updated: now - 1000, subagents: {} },
      s3: { id: 's3', state: 'idle', updated: now - 2000, subagents: {} },
    },
    tileTree: tile,
    focusedTile: tile.id,
    activeSession: isMobile ? 's1' : null,
    isMobile,
  });
  return tile;
}

beforeEach(() => setState({ sessions: {}, activeSession: null, isMobile: false }));

test('closing the active session on mobile selects the next open one', async () => {
  seed(true);

  await closeSession('s1');

  const state = store.get();
  expect(state.sessions.s1.state).toBe('saved');
  // Not left on the empty state: the next most recent OPEN session takes over.
  expect(state.activeSession).toBe('s2');
});

test('closing a session on mobile that is not the active one leaves the selection alone', async () => {
  seed(true);

  await closeSession('s2');

  expect(store.get().activeSession).toBe('s1');
});

test('closing the last open session on mobile does land on the empty state', async () => {
  seed(true);
  setState({ sessions: { s1: { id: 's1', state: 'idle', updated: Date.now(), subagents: {} } } });

  await closeSession('s1');

  expect(store.get().activeSession).toBeNull();
});

test('closing a tiled session on desktop refills the tile with another open one', async () => {
  const tile = seed(false);
  setState((s) => ({ tileTree: { ...s.tileTree, sessionId: 's1' } }));
  expect(allSessionIds(store.get().tileTree)).toEqual(['s1']);

  await closeSession('s1');

  const assigned = allSessionIds(store.get().tileTree);
  expect(assigned).toEqual(['s2']);
  expect(store.get().focusedTile).toBe(tile.id);
});

test('closing drops a retained resolved-prompt receipt before the session can reopen', async () => {
  seed(true);
  setState({ sessions: {
    ...store.get().sessions,
    s1: {
      ...store.get().sessions.s1,
      resolvedPendingAttention: { id: 'ask-1', unseenGen: 7, serverInstance: 'server-a' },
    },
  } });

  await closeSession('s1');

  expect(store.get().sessions.s1.resolvedPendingAttention).toBeNull();
});
