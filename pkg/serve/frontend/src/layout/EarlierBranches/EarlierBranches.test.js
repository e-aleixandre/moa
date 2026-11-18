import { test, expect, beforeEach } from 'bun:test';
import { readFileSync } from 'node:fs';
import { shouldAskBranchPoints } from './EarlierBranches.jsx';
import { setState, store } from '../../data/store.js';
import { beginHistoryHydration } from '../../data/history-hydration.js';
import { handleWsInit } from '../../data/ws-handlers.js';
import { loadSessions } from '../../data/session-actions.js';

beforeEach(() => setState({ sessions: {}, activeSession: null, isMobile: false }));

test('the branch points are not asked for while the transcript is empty only because the init has not arrived', () => {
  expect(shouldAskBranchPoints({ sessionId: 's1', empty: true, initReceived: false })).toBe(false);
  expect(shouldAskBranchPoints({ sessionId: 's1', empty: true, initReceived: undefined })).toBe(false);
});

test('they are asked for when the transcript is still empty after the init', () => {
  expect(shouldAskBranchPoints({ sessionId: 's1', empty: true, initReceived: true })).toBe(true);
});

test('a transcript with messages, or no session, never asks', () => {
  expect(shouldAskBranchPoints({ sessionId: 's1', empty: false, initReceived: true })).toBe(false);
  expect(shouldAskBranchPoints({ sessionId: '', empty: true, initReceived: true })).toBe(false);
});

test('the init marks the session and a reconnect does not unmark it', () => {
  setState({ sessions: { s1: { id: 's1', messages: [], subagents: {} } } });
  expect(store.get().sessions.s1.initReceived).toBeUndefined();
  handleWsInit('s1', { messages: [], subagents: [] });
  expect(store.get().sessions.s1.initReceived).toBe(true);
  beginHistoryHydration('s1');
  expect(store.get().sessions.s1.initReceived).toBe(true);
});

test('both conversation screens hand the init flag to the link', () => {
  for (const file of [
    '../ConversationScreen/ConversationScreen.jsx',
    '../mobile/MobileConversationScreen/MobileConversationScreen.jsx',
  ]) {
    expect(readFileSync(new URL(file, import.meta.url), 'utf8')).toContain('initReceived={!!session.initReceived}');
  }
});

// The roster rebuilds each session field by field. A poll that changes
// anything (the title here) must not erase the proof that the init arrived, or
// the link on an empty branch vanishes until the next reconnect.
test('a roster poll keeps the init proof, and a new server incarnation drops it', async () => {
  const saved = { fetch: globalThis.fetch, location: globalThis.location };
  const info = { id: 's1', title: 'first', state: 'idle', attention_namespace: 'ns', server_instance: 'i1' };
  globalThis.location = { protocol: 'http:', host: 'moa.test' };
  globalThis.fetch = async () => ({ ok: true, status: 200, headers: new Headers(), json: async () => [info], text: async () => JSON.stringify([info]) });
  try {
    await loadSessions();
    expect(store.get().sessions.s1.initReceived).toBeFalsy();
    handleWsInit('s1', { messages: [], subagents: [], attention_namespace: 'ns', server_instance: 'i1', last_seq: 3, state: 'idle' });
    expect(store.get().sessions.s1.initReceived).toBe(true);

    info.title = 'second';
    await loadSessions();
    expect(store.get().sessions.s1.title).toBe('second');
    expect(store.get().sessions.s1.initReceived).toBe(true);

    info.server_instance = 'i2';
    await loadSessions();
    expect(store.get().sessions.s1.initReceived).toBe(false);
  } finally {
    globalThis.fetch = saved.fetch;
    if (saved.location === undefined) delete globalThis.location; else globalThis.location = saved.location;
  }
});
