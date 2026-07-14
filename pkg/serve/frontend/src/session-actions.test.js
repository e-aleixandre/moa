// session-actions.test.js — run with `bun test`
//
// Verifies the session poll (loadSessions) preserves WS/live-only fields that
// the /api/sessions response doesn't carry — in particular the OpenAI usage
// percents, which have no poller to restore them (regression: they flickered
// away every poll tick).
import { test, expect, beforeEach, mock } from 'bun:test';

// Mock only the network call, keeping api.js's other exports intact (other
// modules transitively import syncConnections/reconnectAll/etc. from it).
let apiResponse = [];
const realApi = await import('./api.js');
mock.module('./api.js', () => ({
  ...realApi,
  api: async () => apiResponse,
}));

const { store, setState } = await import('./store.js');
const { loadSessions, sendMessage } = await import('./session-actions.js');

beforeEach(() => {
  setState({ sessions: {}, tileTree: null, activeSession: null });
});

test('loadSessions preserves OpenAI rate-limit percents across a poll', async () => {
  // Seed an existing OpenAI session carrying live-only usage percents.
  setState({
    sessions: {
      s1: {
        id: 's1', provider: 'openai', state: 'idle', subagents: {},
        rlFiveHourPct: 42, rlSevenDayPct: 55,
      },
    },
  });
  // The poll response knows nothing about the usage percents.
  apiResponse = [{ id: 's1', title: 'S1', state: 'idle', provider: 'openai', cwd: '/x' }];

  await loadSessions();

  const s1 = store.get().sessions.s1;
  expect(s1.rlFiveHourPct).toBe(42);
  expect(s1.rlSevenDayPct).toBe(55);
});

test('loadSessions leaves rate-limit percents undefined for a fresh session', async () => {
  apiResponse = [{ id: 's2', title: 'S2', state: 'idle', provider: 'openai', cwd: '/y' }];

  await loadSessions();

  const s2 = store.get().sessions.s2;
  expect(s2.rlFiveHourPct).toBeUndefined();
  expect(s2.rlSevenDayPct).toBeUndefined();
});

test('sendMessage mid-run records the image count on the optimistic steer chip', async () => {
  // A running session: the send becomes a steer, and the optimistic chip must
  // carry the number of attached images so the UI can badge it and warn on
  // pull-back/abort (base64 is not tracked locally, only the count).
  setState({ sessions: { s1: { id: 's1', state: 'running', subagents: {}, pendingSteers: null, messages: [] } } });
  apiResponse = { action: 'steer' };

  await sendMessage('s1', 'look at these', [
    { name: 'a.png', mime: 'image/png', data: 'AAAA', isImage: true },
    { name: 'b.png', mime: 'image/png', data: 'BBBB', isImage: true },
    { name: 'notes.txt', mime: 'text/plain', data: 'Q0M=', isImage: false },
  ]);

  const steers = store.get().sessions.s1.pendingSteers;
  expect(steers).toHaveLength(1);
  expect(steers[0].text).toBe('look at these');
  expect(steers[0].images).toBe(2);
  expect(steers[0].confirmed).toBe(true);
});

test('sendMessage mid-run without images omits the images field', async () => {
  setState({ sessions: { s1: { id: 's1', state: 'running', subagents: {}, pendingSteers: null, messages: [] } } });
  apiResponse = { action: 'steer' };

  await sendMessage('s1', 'just text', []);

  const steers = store.get().sessions.s1.pendingSteers;
  expect(steers).toHaveLength(1);
  expect(steers[0].images).toBeUndefined();
});
