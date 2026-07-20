// ws-handlers.test.js — run with `bun test`
import { test, expect, beforeEach } from 'bun:test';
import { store, setState } from './store.js';
import { projectStream, liveTrayAgents } from './stream-model.js';
import { handleWsInit, handleWsSubagentStart, handleWsSubagentEnd, normalizeConversationProjection, normalizeHistory, handleWsGoalChange, handleWsGoalVerify, handleWsBashComplete, handleWsSteer, handleWsSteersCanceled, handleWsRunEnd, handleWsCommandQueued, handleWsCommandDequeued, handleWsMessageEnd } from './ws-handlers.js';

function seedSession(id) {
  setState({ sessions: { [id]: { id, subagents: {} } } });
}

beforeEach(() => {
  setState({ sessions: {} });
});

test('normalizeConversationProjection preserves persisted tool activity', () => {
  const [tool] = normalizeConversationProjection([{
    id: 'tool:child:0', role: 'tool', tool: 'bash', action: 'bash',
    target: '{"command":"go test ./pkg/serve --token complete"}', status: 'ok',
  }]);

  expect(tool).toMatchObject({
    _type: 'tool_start',
    tool_call_id: 'tool:child:0',
    tool_name: 'bash',
    args: { command: 'go test ./pkg/serve --token complete' },
    activity: { action: 'bash', target: '{"command":"go test ./pkg/serve --token complete"}' },
    status: 'done',
  });
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

test('handleWsInit dedups restored subagent cards against a running snapshot entry', () => {
  const cases = [
    {
      name: 'a card with its persisted job ID',
      custom: {
        source: 'subagent', subagent_job_id: 'j1', subagent_task: 'Read-only diagnosis',
        subagent_status: 'completed', subagent_result: 'done',
      },
    },
    {
      name: 'a legacy card without a persisted job ID',
      custom: {
        source: 'subagent', subagent_task: 'Read-only diagnosis',
        subagent_status: 'completed', subagent_result: 'done',
      },
    },
  ];

  for (const scenario of cases) {
    seedSession(scenario.name);
    handleWsInit(scenario.name, {
      messages: [{ role: 'user', custom: scenario.custom, content: [] }],
      subagents: [{ job_id: 'j1', task: 'Read-only diagnosis', status: 'running' }],
    });

    const blocks = projectStream(store.get().sessions[scenario.name]);
    const agents = blocks.flatMap(block => (block.blocks || []).flatMap(inner => inner.agents || []));
    expect(agents).toHaveLength(1);
    expect(agents[0]).toMatchObject({ id: 'j1', state: 'done' });
  }
});

test('handleWsInit suppresses a canonicalized async legacy card in both stream and dock', () => {
  seedSession('s1');
  handleWsInit('s1', {
    messages: [{
      role: 'user',
      custom: {
        source: 'subagent', subagent_task: 'Read-only diagnosis',
        subagent_status: 'completed', subagent_result: 'done',
      },
      content: [],
    }],
    subagents: [{ job_id: 'j1', task: 'Read-only diagnosis', status: 'running', async: true }],
  });

  const session = store.get().sessions.s1;
  const agents = projectStream(session)
    .flatMap(block => (block.blocks || []).flatMap(inner => inner.agents || []));
  expect(agents).toEqual([expect.objectContaining({ id: 'j1', state: 'done' })]);
  expect(liveTrayAgents(session)).toEqual([]);
});

test('handleWsInit only correlates legacy cards to a unique live job', () => {
  seedSession('terminal');
  handleWsInit('terminal', {
    messages: [{
      role: 'user',
      custom: {
        source: 'subagent', subagent_task: 'Finished review',
        subagent_status: 'completed', subagent_result: 'done',
      },
      content: [],
    }],
    subagents: [{ job_id: 'terminal-job', task: 'Finished review', status: 'completed' }],
  });
  expect(store.get().sessions.terminal.messages[0].tool_call_id).toBe('subagent_0');

  seedSession('persisted');
  handleWsInit('persisted', {
    messages: [{
      role: 'user',
      custom: {
        source: 'subagent', subagent_job_id: 'persisted-job', subagent_task: 'Same task',
        subagent_status: 'completed', subagent_result: 'done',
      },
      content: [],
    }],
    subagents: [{ job_id: 'heuristic-job', task: 'Same task', status: 'running' }],
  });
  expect(store.get().sessions.persisted.messages[0].tool_call_id).toBe('subagent-persisted-job');
});

test('handleWsInit does not conflate a legacy card with multiple live jobs sharing its task', () => {
  seedSession('s1');
  handleWsInit('s1', {
    messages: [{
      role: 'user',
      custom: {
        source: 'subagent', subagent_task: 'Repeatable review',
        subagent_status: 'completed', subagent_result: 'done',
      },
      content: [],
    }],
    subagents: [
      { job_id: 'j1', task: 'Repeatable review', status: 'running' },
      { job_id: 'j2', task: 'Repeatable review', status: 'running' },
    ],
  });

  const blocks = projectStream(store.get().sessions.s1);
  const agents = blocks.flatMap(block => (block.blocks || []).flatMap(inner => inner.agents || []));
  expect(agents).toHaveLength(3);
  expect(agents.filter(agent => agent.state === 'running').map(agent => agent.id).sort()).toEqual(['j1', 'j2']);
});

test('handleWsInit keeps a distinct live subagent when a legacy card has another task', () => {
  seedSession('s1');
  handleWsInit('s1', {
    messages: [{
      role: 'user',
      custom: {
        source: 'subagent', subagent_task: 'Completed review',
        subagent_status: 'completed', subagent_result: 'done',
      },
      content: [],
    }],
    subagents: [{ job_id: 'j1', task: 'Still running analysis', status: 'running' }],
  });

  const blocks = projectStream(store.get().sessions.s1);
  const agents = blocks.flatMap(block => (block.blocks || []).flatMap(inner => inner.agents || []));
  expect(agents).toHaveLength(2);
  expect(agents.map(agent => agent.state).sort()).toEqual(['done', 'running']);
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

test('handleWsInit clears a stuck subagentCount when no live jobs remain', () => {
  seedSession('s1');
  // An async job finished while this pane had no WS, so the terminal count=0
  // event was missed and the badge/dot stayed stuck at 1.
  setState({ sessions: { s1: { ...store.get().sessions.s1, subagentCount: 1 } } });
  handleWsInit('s1', { messages: [], subagents: [] });
  expect(store.get().sessions.s1.subagentCount).toBe(0);
});

test('handleWsInit recomputes subagentCount from live async jobs in the snapshot', () => {
  seedSession('s1');
  handleWsInit('s1', {
    messages: [],
    subagents: [
      { job_id: 'a', async: true, status: 'running' },
      { job_id: 'b', async: true, status: 'cancelling' },
      { job_id: 'c', async: false, status: 'running' }, // sync — not counted
      { job_id: 'd', async: true, status: 'completed' }, // terminal — not counted
    ],
  });
  expect(store.get().sessions.s1.subagentCount).toBe(2);
});

test('handleWsInit replaces server-ID steers with the authoritative snapshot', () => {
  seedSession('s1');
  // A chip that already carries a server ID and was confirmed by its POST: the
  // snapshot is authoritative.
  setState({ sessions: { s1: { ...store.get().sessions.s1, pendingSteers: [{ id: 'srv1', text: 'Please continue with the tests', confirmed: true }] } } });

  // The server consumed the steer while this pane was hidden, so the snapshot
  // no longer lists it.
  handleWsInit('s1', {
    messages: [{ role: 'user', content: [{ type: 'text', text: 'Please continue with the tests' }] }],
  });

  const session = store.get().sessions.s1;
  expect(session.pendingSteers).toBeNull();
  expect(session.messages).toHaveLength(1);
});

test('handleWsInit keeps an in-flight local chip (ID not yet in snapshot) but adopts snapshot steers', () => {
  seedSession('s1');
  setState({ sessions: { s1: { ...store.get().sessions.s1, pendingSteers: [{ id: 'c-local1', text: 'just typed' }] } } });

  handleWsInit('s1', {
    messages: [],
    pending_steers: [{ id: 'srv9', text: 'queued elsewhere' }],
  });

  const steers = store.get().sessions.s1.pendingSteers;
  expect(steers).toHaveLength(2);
  expect(steers[0]).toMatchObject({ id: 'srv9', text: 'queued elsewhere', confirmed: true });
  expect(steers[1]).toEqual({ id: 'c-local1', text: 'just typed' });
});

test('handleWsInit drops a confirmed local chip whose ID the server already dropped', () => {
  seedSession('s1');
  setState({ sessions: { s1: { ...store.get().sessions.s1, pendingSteers: [{ id: 'c-gone', text: 'delivered already', confirmed: true }] } } });

  // The chip was confirmed (POST returned) but the snapshot no longer lists it,
  // so the server delivered/cancelled it — the stale local chip must not linger.
  handleWsInit('s1', { messages: [], pending_steers: [] });

  expect(store.get().sessions.s1.pendingSteers).toBeNull();
});

test('handleWsInit keeps an unconfirmed in-flight chip absent from the snapshot', () => {
  seedSession('s1');
  setState({ sessions: { s1: { ...store.get().sessions.s1, pendingSteers: [{ id: 'c-inflight', text: 'just sent' }] } } });

  // The POST hasn't returned (confirmed !== true) and the snapshot predates it,
  // so the chip must survive the reconnect.
  handleWsInit('s1', { messages: [], pending_steers: [] });

  const steers = store.get().sessions.s1.pendingSteers;
  expect(steers).toHaveLength(1);
  expect(steers[0].id).toBe('c-inflight');
});

test('handleWsSteer removes the queued chip by ID, not by text', () => {
  seedSession('s1');
  setState({ sessions: { s1: { ...store.get().sessions.s1, messages: [], pendingSteers: [
    { id: 'a', text: 'same text' },
    { id: 'b', text: 'same text' },
  ] } } });

  handleWsSteer('s1', { id: 'b', text: 'same text' });

  const steers = store.get().sessions.s1.pendingSteers;
  expect(steers).toHaveLength(1);
  expect(steers[0].id).toBe('a');
});

test('handleWsSteer dedups the injected user message by MsgID', () => {
  seedSession('s1');
  setState({ sessions: { s1: { ...store.get().sessions.s1, messages: [
    { role: 'user', _msg_id: 'm1', content: [{ type: 'text', text: 'already here' }] },
  ], pendingSteers: [{ id: 'z', text: 'already here' }] } } });

  // The reconnect snapshot already contained the message; the Steered event
  // (seq > cut) must not add it a second time, but must still clear the chip.
  handleWsSteer('s1', { id: 'z', msg_id: 'm1', text: 'already here' });

  const sess = store.get().sessions.s1;
  expect(sess.messages).toHaveLength(1);
  expect(sess.pendingSteers).toBeNull();
});

test('handleWsSteersCanceled clears the shared queue on every client', () => {
  seedSession('s1');
  setState({ sessions: { s1: { ...store.get().sessions.s1, pendingSteers: [
    { id: 'srv', text: 'queued' },
    { id: 'c-local', text: 'just typed' },
  ] } } });

  handleWsSteersCanceled('s1');

  expect(store.get().sessions.s1.pendingSteers).toBeNull();
});

test('handleWsCommandQueued appends a command chip and confirms an optimistic one by ID', () => {
  seedSession('s1');
  // From another device: no local chip yet — append it.
  handleWsCommandQueued('s1', { id: 'cmd-1', raw: '/compact' });
  let steers = store.get().sessions.s1.pendingSteers;
  expect(steers).toHaveLength(1);
  expect(steers[0]).toMatchObject({ id: 'cmd-1', text: '/compact', command: true, confirmed: true });

  // Same device that already showed an optimistic (unconfirmed) chip: confirm,
  // don't duplicate.
  setState({ sessions: { s1: { ...store.get().sessions.s1, pendingSteers: [
    { id: 'cmd-2', text: '/model sonnet', command: true },
  ] } } });
  handleWsCommandQueued('s1', { id: 'cmd-2', raw: '/model sonnet' });
  steers = store.get().sessions.s1.pendingSteers;
  expect(steers).toHaveLength(1);
  expect(steers[0].confirmed).toBe(true);
});

test('handleWsCommandDequeued removes the command chip; a failure surfaces a toast', () => {
  seedSession('s1');
  setState({ sessions: { s1: { ...store.get().sessions.s1, pendingSteers: [
    { id: 'cmd-1', text: '/compact', command: true, confirmed: true },
    { id: 'q2', text: 'keep me' },
  ] } } });

  // Executed at idle: chip gone, others kept, no toast.
  const before = getToasts().length;
  handleWsCommandDequeued('s1', { id: 'cmd-1', raw: '/compact', executed: true });
  let steers = store.get().sessions.s1.pendingSteers;
  expect(steers).toHaveLength(1);
  expect(steers[0].id).toBe('q2');
  expect(getToasts().length).toBe(before);

  // Failed permanently: chip gone AND an error toast is raised.
  handleWsCommandDequeued('s1', { id: 'q2', raw: '/bogus', executed: false, err: 'boom' });
  expect(store.get().sessions.s1.pendingSteers).toBeNull();
  expect(getToasts().length).toBe(before + 1);
});

test('mergeSteers carries command/images through a reconnect snapshot', () => {
  seedSession('s1');
  handleWsInit('s1', {
    state: 'running',
    pending_steers: [
      { id: 'cmd-1', text: '/compact', command: true },
      { id: 'm1', text: 'look at this', images: 2 },
    ],
  });
  const steers = store.get().sessions.s1.pendingSteers;
  expect(steers).toHaveLength(2);
  expect(steers[0]).toMatchObject({ id: 'cmd-1', command: true, confirmed: true });
  expect(steers[1]).toMatchObject({ id: 'm1', images: 2, command: false, confirmed: true });
});

test('handleWsRunEnd keeps genuinely queued steers (mostrar la verdad)', () => {
  seedSession('s1');
  setState({ sessions: { s1: { ...store.get().sessions.s1, messages: [], state: 'running', pendingSteers: [{ id: 'q1', text: 'do this next' }] } } });

  handleWsRunEnd('s1');

  const steers = store.get().sessions.s1.pendingSteers;
  expect(steers).toHaveLength(1);
  expect(steers[0].id).toBe('q1');
});

// Regression for bug #2: a stale "compacting" spinner must be cleared by the
// authoritative snapshot when the compaction finished while the pane had no WS.
test('handleWsInit clears a stale compacting spinner from the snapshot', () => {
  seedSession('s1');
  setState({ sessions: { s1: { ...store.get().sessions.s1, compacting: true } } });

  // Reconnect: the server's snapshot says no compaction is in progress.
  handleWsInit('s1', { messages: [] });

  expect(store.get().sessions.s1.compacting).toBe(false);
});

// Regression for bug #2: a compaction still running at reconnect must restore
// the spinner from the snapshot.
test('handleWsInit restores an in-progress compacting spinner from the snapshot', () => {
  seedSession('s1');
  handleWsInit('s1', { messages: [], compacting: true });

  expect(store.get().sessions.s1.compacting).toBe(true);
});

// Regression for bug #3: a reconnect during generation must restore the whole
// streamed-so-far reply from the snapshot, not start from the next delta.
test('handleWsInit restores the in-flight streamed reply from the snapshot', () => {
  seedSession('s1');
  handleWsInit('s1', {
    messages: [],
    streaming_text: 'partial reply',
    streaming_thinking: 'partial thought',
  });

  expect(store.get().sessions.s1.streamingText).toBe('partial reply');
  expect(store.get().sessions.s1.thinkingText).toBe('partial thought');
});

// A reconnect when nothing is streaming must leave the buffers empty (null),
// not carry stale streaming text over from a previous connection.
test('handleWsInit clears streaming buffers when nothing is in flight', () => {
  seedSession('s1');
  setState({ sessions: { s1: { ...store.get().sessions.s1, streamingText: 'stale', thinkingText: 'stale' } } });

  handleWsInit('s1', { messages: [] });

  expect(store.get().sessions.s1.streamingText).toBe(null);
  expect(store.get().sessions.s1.thinkingText).toBe(null);
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

// Parity with the TUI "verifying…" status: goal_verify toggles a flag the
// TaskBar pill reads, and it's cleared when the goal ends.
test('handleWsGoalVerify toggles goalVerifying', () => {
  seedSession('s1');
  setState({ sessions: { s1: { ...store.get().sessions.s1, goalActive: true } } });

  handleWsGoalVerify('s1', { active: true, iteration: 2 });
  expect(store.get().sessions.s1.goalVerifying).toBe(true);

  handleWsGoalVerify('s1', { active: false, iteration: 2 });
  expect(store.get().sessions.s1.goalVerifying).toBe(false);
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

test('handleWsBashComplete adds a bash card to the chat', () => {
  setState({ sessions: { s1: { id: 's1', subagents: {}, messages: [] } } });
  handleWsBashComplete('s1', { job_id: 'bash-1', command: 'sleep 5; echo done', status: 'completed', text: '[bash job completed] Job bash-1 finished.\nCommand: sleep 5; echo done\n\nOutput:\ndone' });
  const msgs = store.get().sessions.s1.messages;
  const card = msgs[msgs.length - 1];
  expect(card._type).toBe('tool_start');
  expect(card.tool_name).toBe('bash');
  expect(card.status).toBe('done');
  expect(card.args.command).toBe('sleep 5; echo done');
});

test('normalizeHistory reloads a bash_job custom notification as a bash card', () => {
  const raw = [{
    role: 'user',
    custom: { source: 'bash_job', bash_command: 'make build', bash_status: 'completed' },
    content: [{ type: 'text', text: '[bash job completed] Job bash-9 finished.\nCommand: make build\n\nOutput:\nok' }],
  }];
  const out = normalizeHistory(raw);
  expect(out.length).toBe(1);
  expect(out[0]._type).toBe('tool_start');
  expect(out[0].tool_name).toBe('bash');
  expect(out[0].args.command).toBe('make build');
  expect(out[0].status).toBe('done');
});

test('normalizeHistory reloads a prefix-based bash notification (no custom)', () => {
  const raw = [{
    role: 'user',
    content: [{ type: 'text', text: '[bash job failed] Job bash-2 failed.\nCommand: false\nOutput:\nboom' }],
  }];
  const out = normalizeHistory(raw);
  expect(out[0]._type).toBe('tool_start');
  expect(out[0].tool_name).toBe('bash');
  expect(out[0].args.command).toBe('false');
  expect(out[0].status).toBe('error');
});

import { handleWsRateLimit } from './ws-handlers.js';

test('handleWsRateLimit stores per-session pcts and does not touch the global snapshot for OpenAI', () => {
  setState({
    sessions: { s1: { id: 's1', provider: 'openai', subagents: {} } },
    usage: { available: true, five_hour: { utilization: 10 }, seven_day: { utilization: 20 } },
  });

  handleWsRateLimit('s1', { five_hour_pct: 40, seven_day_pct: 51, on_overage: false });

  const sess = store.get().sessions.s1;
  expect(sess.rlFiveHourPct).toBe(40);
  expect(sess.rlSevenDayPct).toBe(51);
  // Anthropic global snapshot must be untouched by an OpenAI session.
  expect(store.get().usage.five_hour.utilization).toBe(10);
  expect(store.get().usage.seven_day.utilization).toBe(20);
});

test('handleWsRateLimit patches the global snapshot for Anthropic sessions', () => {
  setState({
    sessions: { s1: { id: 's1', provider: 'anthropic', subagents: {} } },
    usage: { available: true, five_hour: { utilization: 10 }, seven_day: { utilization: 20 } },
  });

  handleWsRateLimit('s1', { five_hour_pct: 40, seven_day_pct: 51, on_overage: false });

  expect(store.get().usage.five_hour.utilization).toBe(40);
  expect(store.get().usage.seven_day.utilization).toBe(51);
  expect(store.get().sessions.s1.rlFiveHourPct).toBe(40);
});

test('handleWsRateLimit ignores unknown windows (pct < 0)', () => {
  setState({
    sessions: { s1: { id: 's1', provider: 'openai', subagents: {} } },
    usage: null,
  });

  handleWsRateLimit('s1', { five_hour_pct: 40, seven_day_pct: -1, on_overage: false });

  const sess = store.get().sessions.s1;
  expect(sess.rlFiveHourPct).toBe(40);
  expect(sess.rlSevenDayPct).toBeUndefined();
});

test('handleWsRateLimit isolates providers in a mixed layout', () => {
  setState({
    sessions: {
      a: { id: 'a', provider: 'anthropic', subagents: {} },
      o: { id: 'o', provider: 'openai', subagents: {} },
    },
    usage: { available: true, five_hour: { utilization: 5 }, seven_day: { utilization: 6 } },
  });

  // OpenAI session updates only its own per-session pcts, not the global snapshot.
  handleWsRateLimit('o', { five_hour_pct: 80, seven_day_pct: 90, on_overage: false });
  expect(store.get().sessions.o.rlFiveHourPct).toBe(80);
  expect(store.get().usage.five_hour.utilization).toBe(5);

  // Anthropic session patches the global snapshot; OpenAI's per-session values stay put.
  handleWsRateLimit('a', { five_hour_pct: 30, seven_day_pct: 40, on_overage: false });
  expect(store.get().usage.five_hour.utilization).toBe(30);
  expect(store.get().sessions.o.rlFiveHourPct).toBe(80);
  expect(store.get().sessions.a.rlFiveHourPct).toBe(30);
});

// --- Per-run token tally (heartbeat) ---
// A run spans several model calls (each tool round-trip is another call). Every
// call replays the whole accumulated context as input, so the ↑ tally must take
// the LAST call's input (not sum, which would double-count context every step
// and inflate ↑). ↓ output accumulates (each call generates fresh output).
test('handleWsMessageEnd: ↑ input is the last call, ↓ output accumulates', () => {
  setState({ sessions: { s1: { id: 's1', subagents: {}, messages: [] } } });
  handleWsMessageEnd('s1', 'a', 'm1', 12000, 300);
  expect(store.get().sessions.s1.runTokensUp).toBe(12000);
  expect(store.get().sessions.s1.runTokensDown).toBe(300);
  // Second model call in the same run: input grew (context replayed + tool
  // results), output is new. ↑ = latest (18000), NOT 30000; ↓ = 300+450.
  handleWsMessageEnd('s1', 'ab', 'm2', 18000, 450);
  expect(store.get().sessions.s1.runTokensUp).toBe(18000);
  expect(store.get().sessions.s1.runTokensDown).toBe(750);
});

test('handleWsMessageEnd: a zero-input message keeps the last real ↑ value', () => {
  setState({ sessions: { s1: { id: 's1', subagents: {}, messages: [] } } });
  handleWsMessageEnd('s1', 'a', 'm1', 9000, 200);
  handleWsMessageEnd('s1', 'ab', 'm2', 0, 120); // no usage reported this message
  expect(store.get().sessions.s1.runTokensUp).toBe(9000);
  expect(store.get().sessions.s1.runTokensDown).toBe(320);
});
