import { beforeEach, expect, test } from 'bun:test';
import { loadOlderHistory, seedOlderHistory } from './history-paging.js';
import { handleWsInit } from './ws-handlers.js';
import { setState, store } from './store.js';

beforeEach(() => {
  setState({ sessions: {}, activeSession: null });
});

function session(olderHistory = {}) {
  return {
    id: 's1', messages: [{ role: 'user', _msg_id: 'kept' }], subagents: {},
    olderHistory: { before: 'cursor', hasMore: true, loading: false, epoch: 4, prependVersion: 0, ...olderHistory },
  };
}

test('a full init invalidates an in-flight page even with its same cursor', async () => {
  let resolve;
  globalThis.fetch = () => new Promise(done => { resolve = done; });
  setState({ sessions: { s1: session() } });

  const pending = loadOlderHistory('s1');
  expect(store.get().sessions.s1.olderHistory.loading).toBe(true);
  handleWsInit('s1', {
    messages: [{ role: 'assistant', msg_id: 'authoritative', content: [{ type: 'text', text: 'fresh' }] }],
    history_before: 'cursor', subagents: [],
  });
  const afterInit = store.get().sessions.s1.olderHistory;
  expect(afterInit).toMatchObject({ before: 'cursor', hasMore: true, loading: false, epoch: 5 });

  resolve({ ok: true, status: 200, text: async () => JSON.stringify({
    messages: [{ role: 'user', msg_id: 'stale', content: [{ type: 'text', text: 'old' }] }],
    next_before: '', has_more: false,
  }) });
  await pending;
  expect(store.get().sessions.s1.messages.map(m => m._msg_id)).toEqual(['authoritative']);
  expect(store.get().sessions.s1.olderHistory.loading).toBe(false);
});

test('paging neither starts without a cursor nor while a page is in flight', async () => {
  let calls = 0;
  globalThis.fetch = () => { calls++; return new Promise(() => {}); };
  setState({ sessions: { s1: session({ hasMore: false }) } });
  expect(await loadOlderHistory('s1')).toBe(false);
  setState({ sessions: { s1: session({ loading: true }) } });
  expect(await loadOlderHistory('s1')).toBe(false);
  expect(calls).toBe(0);
});

test('seeding always creates a new epoch for an authoritative replacement', () => {
  setState({ sessions: { s1: session({ loading: true }) } });
  seedOlderHistory('s1', 'cursor');
  expect(store.get().sessions.s1.olderHistory).toMatchObject({ epoch: 5, loading: false });
});
