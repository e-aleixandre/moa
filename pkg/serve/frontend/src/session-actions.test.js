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
const { loadSessions } = await import('./session-actions.js');

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
