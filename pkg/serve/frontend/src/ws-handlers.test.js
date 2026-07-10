// ws-handlers.test.js — run with `bun test`
import { test, expect, beforeEach } from 'bun:test';
import { store, setState } from './store.js';
import { handleWsInit, handleWsSubagentStart, handleWsSubagentEnd } from './ws-handlers.js';

function seedSession(id) {
  setState({ sessions: { [id]: { id, subagents: {} } } });
}

beforeEach(() => {
  setState({ sessions: {} });
});

test('handleWsSubagentStart creates a running entry with async flag', () => {
  seedSession('s1');
  handleWsSubagentStart('s1', { job_id: 'j1', task: 't', model: 'm', async: false });
  const sa = store.get().sessions.s1.subagents.j1;
  expect(sa.status).toBe('running');
  expect(sa.async).toBe(false);
});

test('handleWsSubagentStart flips async without touching a running status', () => {
  seedSession('s1');
  handleWsSubagentStart('s1', { job_id: 'j1', task: 't', model: 'm', async: false });
  handleWsSubagentStart('s1', { job_id: 'j1', async: true });
  const sa = store.get().sessions.s1.subagents.j1;
  expect(sa.status).toBe('running');
  expect(sa.async).toBe(true);
});

// Regression test for the promote/finish race: promoting a sync subagent
// right as it completes can deliver the subagent_start echo (async:true)
// AFTER the subagent_end that already marked it terminal. The stale
// subagent_start must not resurrect it as 'running' forever.
test('handleWsSubagentStart does not downgrade a terminal status back to running', () => {
  seedSession('s1');
  handleWsSubagentStart('s1', { job_id: 'j1', task: 't', model: 'm', async: false });
  handleWsSubagentEnd('s1', { job_id: 'j1', status: 'completed' });
  // Late-arriving promote echo.
  handleWsSubagentStart('s1', { job_id: 'j1', async: true });
  const sa = store.get().sessions.s1.subagents.j1;
  expect(sa.status).toBe('completed');
  expect(sa.async).toBe(true);
});

test('handleWsInit preserves the bounded-history marker', () => {
  seedSession('s1');
  handleWsInit('s1', {
    messages: [{ role: 'assistant', msg_id: 'latest', content: [{ type: 'text', text: 'latest' }] }],
    history_truncated: true,
  });
  const session = store.get().sessions.s1;
  expect(session.historyTruncated).toBe(true);
  expect(session.messages).toHaveLength(1);
});

test('handleWsInit clears a steer consumed while the session was hidden', () => {
  seedSession('s1');
  setState({ sessions: { s1: { ...store.get().sessions.s1, pendingSteers: ['Please continue with the tests'] } } });

  // The client did not have a WS while hidden, but the server consumed the
  // steer and includes it in its authoritative history on reconnect.
  handleWsInit('s1', {
    messages: [{ role: 'user', content: [{ type: 'text', text: 'Please continue with the tests' }] }],
  });

  const session = store.get().sessions.s1;
  expect(session.pendingSteers).toBeNull();
  expect(session.messages).toHaveLength(1);
});
