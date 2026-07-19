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
const { loadSessions, openPersistedSubagent, sendMessage } = await import('./session-actions.js');

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

test('openPersistedSubagent restores newest-first transcripts to chronological order', async () => {
  setState({ sessions: { s1: { id: 's1', subagents: {} } } });
  apiResponse = {
    order: 'newest_first',
    task: 'inspect ordering',
    messages: [
      { id: 'newest-tool', role: 'tool', tool: 'bash', status: 'ok', target: '{"command":"go test"}' },
      { id: 'older-user', role: 'user', text: 'run the tests' },
    ],
  };

  await openPersistedSubagent('s1', 'job-1');

  const messages = store.get().sessions.s1.subagents['job-1'].messages;
  expect(messages[0]).toMatchObject({ role: 'user', _msg_id: 'older-user' });
  expect(messages[1]).toMatchObject({ _type: 'tool_start', tool_call_id: 'newest-tool' });
});

test('loadSessions preserves the live per-run token tally across a poll', async () => {
  // A run finished with a token tally; the poll (which changes the title, e.g.
  // a fresh brief) must not drop the live-only counts.
  setState({
    sessions: {
      s1: {
        id: 's1', state: 'idle', subagents: {},
        runTokensUp: 41200, runTokensDown: 8700, runStartedAtMs: 123,
      },
    },
  });
  apiResponse = [{ id: 's1', title: 'A new title', state: 'idle', cwd: '/x' }];

  await loadSessions();

  const s1 = store.get().sessions.s1;
  expect(s1.title).toBe('A new title'); // the poll did replace the object
  expect(s1.runTokensUp).toBe(41200);
  expect(s1.runTokensDown).toBe(8700);
});

test('sendMessage from idle resets the token tally to start the new run at zero', async () => {
  // The previous run's totals persist at idle; sending a new message begins a
  // fresh run and must zero the tally optimistically (the WS state_change reset
  // can't fire — this patch already made the session running).
  setState({
    sessions: {
      s1: {
        id: 's1', state: 'idle', subagents: {}, pendingSteers: null, messages: [],
        runTokensUp: 41200, runTokensDown: 8700,
      },
    },
  });
  apiResponse = { action: 'send' };

  await sendMessage('s1', 'next task', []);

  const s1 = store.get().sessions.s1;
  expect(s1.state).toBe('running');
  expect(s1.runTokensUp).toBe(0);
  expect(s1.runTokensDown).toBe(0);
});
