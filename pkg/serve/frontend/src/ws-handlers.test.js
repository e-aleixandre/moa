// ws-handlers.test.js — run with `bun test`
import { test, expect, beforeEach } from 'bun:test';
import { store, setState } from './store.js';
import { handleWsSubagentStart, handleWsSubagentEnd } from './ws-handlers.js';

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
