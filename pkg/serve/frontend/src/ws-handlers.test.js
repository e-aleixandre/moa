// ws-handlers.test.js — run with `bun test`
import { test, expect, beforeEach } from 'bun:test';
import { store, setState } from './store.js';
import { handleWsInit, handleWsSubagentStart, handleWsSubagentEnd, normalizeHistory, handleWsGoalChange } from './ws-handlers.js';

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

// Regression for bug #7: persisted goal-lifecycle markers (role "goal") must
// rebuild as system lines so a reopened conversation shows the goal record.
test('normalizeHistory renders role "goal" markers as system lines', () => {
  const out = normalizeHistory([
    { role: 'goal', custom: { goal: true, phase: 'start' }, content: [{ type: 'text', text: '🎯 Goal started: ship it' }] },
    { role: 'assistant', msg_id: 'a1', content: [{ type: 'text', text: 'working' }] },
    { role: 'goal', custom: { goal: true, phase: 'iteration' }, content: [{ type: 'text', text: '🎯 Goal iteration 1 — not done yet\nkeep going' }] },
    { role: 'goal', custom: { goal: true, phase: 'end' }, content: [{ type: 'text', text: '🎯 Goal ended: objective met' }] },
  ]);
  const systems = out.filter(m => m._type === 'system');
  expect(systems).toHaveLength(3);
  expect(systems[0].text).toContain('Goal started');
  expect(systems[1].text).toContain('iteration 1');
  expect(systems[2].text).toContain('Goal ended');
});

// Bug #7 parity: a fresh goal activation shows a live "start" line (matching the
// persisted marker rendered on reopen); a re-announcement must not duplicate it.
test('handleWsGoalChange adds a live start line once on fresh activation', () => {
  seedSession('s1');
  setState({ sessions: { s1: { ...store.get().sessions.s1, messages: [] } } });

  handleWsGoalChange('s1', { active: true, objective: 'ship it', iteration: 0 });
  let msgs = store.get().sessions.s1.messages;
  expect(msgs).toHaveLength(1);
  expect(msgs[0]._type).toBe('system');
  expect(msgs[0].text).toContain('Goal started');

  // A later goal_change echo (already active, or iteration > 0) must not re-add.
  handleWsGoalChange('s1', { active: true, objective: 'ship it', iteration: 1 });
  msgs = store.get().sessions.s1.messages;
  expect(msgs).toHaveLength(1);
});

import { handleWsStateChange } from './ws-handlers.js';
import { getToasts } from './notifications.js';

// Bug: an OpenAI usage-limit (429) ends the run in the "error" state. The web
// must surface the reason (parity with the TUI's run-end error block), not stay
// silent. The session is kept visible so the focused-tile path runs (no
// navigator-dependent attention notification).
test('handleWsStateChange surfaces a quota error as a toast', () => {
  setState({ sessions: { s1: { id: 's1', state: 'running', subagents: {} } }, isMobile: true, activeSession: 's1' });
  const before = getToasts().length;

  handleWsStateChange('s1', { state: 'error', error: 'openai quota exceeded: The usage limit has been reached (resets in 2h 36m)' });

  const toasts = getToasts();
  expect(toasts.length).toBe(before + 1);
  const t = toasts[toasts.length - 1];
  expect(t.title).toBe('Usage limit reached');
  expect(t.detail).toContain('resets in 2h 36m');
});

// A clean idle end must NOT produce an error toast.
test('handleWsStateChange does not toast on a normal idle end', () => {
  setState({ sessions: { s1: { id: 's1', state: 'running', subagents: {} } }, isMobile: true, activeSession: 's1' });
  const before = getToasts().length;
  handleWsStateChange('s1', { state: 'idle' });
  expect(getToasts().length).toBe(before);
});
