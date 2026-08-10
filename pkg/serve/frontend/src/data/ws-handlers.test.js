// ws-handlers.test.js — run with `bun test`
import { test, expect, beforeEach } from 'bun:test';
import { store, setState } from './store.js';
import { projectStream, liveTrayAgents } from './stream-model.js';
import { handleWsInit, handleWsSubagentStart, handleWsSubagentEnd, upsertTerminalSubagentOutcome, normalizeConversationProjection, normalizeHistory, appendNormalizedHistoryDelta, handleWsGoalChange, handleWsGoalVerify, handleWsBashComplete, handleWsBashJobStart, handleWsBashJobEnd, handleWsSteer, handleWsSteersCanceled, handleWsRunEnd, handleWsCommand, handleWsCommandQueued, handleWsCommandDequeued, handleWsRunTokens, handleWsUserMessage, handleWsToolStart, handleWsToolEnd, handleWsStateChange, handleWsAskUser, handleWsPermissionRequest, handleWsAskResolved, handleWsPermissionResolved, handleWsCompactionEnd } from './ws-handlers.js';
import { liveVerb } from './util/activity.js';
import { bashJobView } from './bash-job-view-model.js';
import { __resetAttentionArrivalsForTests } from './attention-arrivals.js';
import { loadSessions } from './session-actions.js';

function seedSession(id) {
  setState({ sessions: { [id]: { id, messages: [], subagents: {} } } });
}

beforeEach(() => {
  globalThis.fetch = () => Promise.resolve(new Response('', { status: 204 }));
  setState({ sessions: {}, isMobile: false, activeSession: null, drawerOpen: false, paletteOpen: false });
  __resetAttentionArrivalsForTests();
});


// These assert the committed acknowledgement cursor and cleared dot rather than
// the request URL: another suite in this run installs
// a module mock over api.js's `api`, so the wire call is not observable here.
// api.test.js covers the exact /read URL against the unmocked module.
// Foregroundedness is part of the read contract, so these state it explicitly
// instead of inheriting whatever `document` another suite left behind.
function foreground(run) {
  const saved = globalThis.document;
  Object.defineProperty(globalThis, 'document', { configurable: true, value: { hidden: false } });
  try {
    return run();
  } finally {
    if (saved === undefined) delete globalThis.document;
    else Object.defineProperty(globalThis, 'document', { configurable: true, value: saved });
  }
}

async function settleAcknowledgement() {
  for (let i = 0; i < 4; i++) await Promise.resolve();
}


test('a confirmed foreground init records its last sequence as the cursor candidate', async () => {
  setState({
    isMobile: true, activeSession: 'cursor-init',
    sessions: { 'cursor-init': {
      id: 'cursor-init', unseen: true, unseenSeq: 7,
      attentionNamespace: 'server-a:1', serverInstance: 'server-a', messages: [], subagents: {},
    } },
  });
  foreground(() => handleWsInit('cursor-init', {
    messages: [], subagents: [], server_instance: 'server-a', attention_namespace: 'server-a:1', last_seq: 42,
  }));
  await settleAcknowledgement();
  expect(store.get().sessions['cursor-init']).toMatchObject({ readCandidateSeq: 42, ackedThroughSeq: 42 });
});

test('a hidden tab never acknowledges a confirmed init cursor', async () => {
  const originalDocument = globalThis.document;
  const reads = [];
  globalThis.fetch = (path) => {
    reads.push(path);
    return Promise.resolve(new Response('', { status: 204 }));
  };
  Object.defineProperty(globalThis, 'document', { configurable: true, value: { hidden: true } });
  try {
    setState({
      isMobile: true, activeSession: 'cursor-hidden',
      sessions: { 'cursor-hidden': {
        id: 'cursor-hidden', attentionNamespace: 'server-a:1', serverInstance: 'server-a', messages: [], subagents: {},
      } },
    });
    handleWsInit('cursor-hidden', {
      messages: [], subagents: [], server_instance: 'server-a', attention_namespace: 'server-a:1', last_seq: 42,
    });
    await settleAcknowledgement();
    expect(reads).toEqual([]);
  } finally {
    if (originalDocument === undefined) delete globalThis.document;
    else Object.defineProperty(globalThis, 'document', { configurable: true, value: originalDocument });
  }
});

test('an init acknowledgement cannot clear a newer cursor occurrence after a stale roster response', async () => {
  const reads = [];
  let resolveRead;
  globalThis.fetch = (path) => {
    if (path.startsWith('/api/sessions/cursor-newer/read?')) {
      reads.push(path);
      return new Promise(resolve => { resolveRead = resolve; });
    }
    if (path === '/api/sessions') {
      return Promise.resolve(new Response(JSON.stringify([{
        id: 'cursor-newer', title: 'Cursor newer', state: 'idle', provider: 'openai', cwd: '/x',
        server_instance: 'server-a', attention_namespace: 'server-a:1', unseen: true, unseen_seq: 100,
      }]), { status: 200 }));
    }
    return Promise.resolve(new Response('', { status: 204 }));
  };
  setState({
    isMobile: true, activeSession: 'cursor-newer',
    sessions: { 'cursor-newer': {
      id: 'cursor-newer', unseen: true, unseenSeq: 100,
      attentionNamespace: 'server-a:1', serverInstance: 'server-a', messages: [], subagents: {},
    } },
  });
  foreground(() => handleWsInit('cursor-newer', {
    messages: [], subagents: [], server_instance: 'server-a', attention_namespace: 'server-a:1', last_seq: 100,
  }));
  await Promise.resolve();
  expect(reads).toEqual(['/api/sessions/cursor-newer/read?through_seq=100&attention_namespace=server-a%3A1']);
  setState({ activeSession: null });
  handleWsRunEnd('cursor-newer', { text: 'new result' }, 101);
  await loadSessions();
  resolveRead(new Response('', { status: 204 }));
  await settleAcknowledgement();
  expect(store.get().sessions['cursor-newer']).toMatchObject({ unseen: true, unseenSeq: 101, ackedThroughSeq: 100 });
});

test('a visible live attention event acknowledges its event sequence', async () => {
  setState({
    isMobile: true, activeSession: 'cursor-live',
    sessions: { 'cursor-live': {
      id: 'cursor-live', attentionNamespace: 'server-a:1', serverInstance: 'server-a', messages: [], subagents: {},
    } },
  });
  foreground(() => handleWsPermissionRequest('cursor-live', { id: 'p1', tool_name: 'bash', args: {} }, 57));
  await settleAcknowledgement();
  expect(store.get().sessions['cursor-live']).toMatchObject({ unseenSeq: 57, ackedThroughSeq: 57 });
});

test('a hidden live attention event never acknowledges its event sequence', async () => {
  const originalDocument = globalThis.document;
  const reads = [];
  globalThis.fetch = (path) => {
    reads.push(path);
    return Promise.resolve(new Response('', { status: 204 }));
  };
  Object.defineProperty(globalThis, 'document', { configurable: true, value: { hidden: true } });
  try {
    setState({
      isMobile: true, activeSession: 'cursor-live-hidden',
      sessions: { 'cursor-live-hidden': {
        id: 'cursor-live-hidden', attentionNamespace: 'server-a:1', serverInstance: 'server-a', messages: [], subagents: {},
      } },
    });
    handleWsPermissionRequest('cursor-live-hidden', { id: 'p1', tool_name: 'bash', args: {} }, 57);
    await settleAcknowledgement();
    expect(reads).toEqual([]);
  } finally {
    if (originalDocument === undefined) delete globalThis.document;
    else Object.defineProperty(globalThis, 'document', { configurable: true, value: originalDocument });
  }
});

test('cursor live classification ignores cancelled and error run ends', async () => {
  setState({
    isMobile: true, activeSession: 'cursor-classification',
    sessions: { 'cursor-classification': {
      id: 'cursor-classification', state: 'running', attentionNamespace: 'server-a:1', serverInstance: 'server-a', messages: [], subagents: {},
    } },
  });
  foreground(() => handleWsRunEnd('cursor-classification', { cancelled: true }, 11));
  await settleAcknowledgement();
  expect(store.get().sessions['cursor-classification'].ackedThroughSeq || 0).toBe(0);

  handleWsStateChange('cursor-classification', { state: 'running' }, 12);
  foreground(() => handleWsStateChange('cursor-classification', { state: 'error', error: 'boom' }, 13));
  foreground(() => handleWsRunEnd('cursor-classification', { has_error: true }, 14));
  await settleAcknowledgement();
  expect(store.get().sessions['cursor-classification']).toMatchObject({ unseenSeq: 13, ackedThroughSeq: 13 });
});




test('a subscribed but unselected session is never marked seen by its own init', async () => {
  setState({
    isMobile: true, activeSession: 'ack-other',
    sessions: {
      'ack-other': { id: 'ack-other', messages: [], subagents: {} },
      // Has a live socket (it is receiving init) but is not on screen. Push
      // suppression is the server's wsConns gate and is deliberately unrelated
      // to this: subscribed is not seen.
      'ack-bg': { id: 'ack-bg', unseen: true, unseenSeq: 5, serverInstance: 'ack-bg-a', messages: [], subagents: {} },
    },
  });
  foreground(() => handleWsInit('ack-bg', {
    messages: [], subagents: [], server_instance: 'ack-bg-a',
  }));
  await settleAcknowledgement();
  expect(store.get().sessions['ack-bg'].unseen).toBe(true);
  expect(store.get().sessions['ack-bg'].ackedThroughSeq || 0).toBe(0);
});


test('prompt resolutions clear pending prompts without touching unread state', () => {
  seedSession('s1');
  setState({ isMobile: true, activeSession: 's1', sessions: {
    s1: { id: 's1', messages: [], subagents: {}, unseen: true, unseenSeq: 7 },
  } });

  handleWsPermissionRequest('s1', { id: 'perm-1', tool_name: 'bash', args: {},});
  handleWsPermissionResolved('s1', { id: 'perm-1' });
  expect(store.get().sessions.s1).toMatchObject({
    pendingPerm: null,
    unseen: true,
    unseenSeq: 7,
  });

  handleWsAskUser('s1', { id: 'ask-1', questions: [],});
  expect(store.get().sessions.s1).toMatchObject({ pendingAsk: { id: 'ask-1' } });
  handleWsAskResolved('s1', { id: 'ask-1' });
  expect(store.get().sessions.s1).toMatchObject({
    pendingAsk: null,
    unseen: true,
    unseenSeq: 7,
  });
});

test('cancelled and aborted prompts clear pending asks', () => {
  seedSession('s1');
  setState({ isMobile: true, activeSession: 's1' });
  for (const id of ['cancelled', 'aborted']) {
    handleWsAskUser('s1', { id, questions: [] });
    handleWsAskResolved('s1', { id });
    expect(store.get().sessions.s1.pendingAsk).toBeNull();
  }
});

test('delta init appends while preserving the cached prefix array and rows', () => {
  const first = { role: 'user', _msg_id: 'one', content: [{ type: 'text', text: 'one' }] };
  const prefix = [first];
  const got = appendNormalizedHistoryDelta(prefix, [{
    role: 'assistant', msg_id: 'two', content: [{ type: 'text', text: 'two' }],
  }]);
  expect(got).toBe(prefix);
  expect(got[0]).toBe(first);
  expect(got).toHaveLength(2);
  expect(got[1]._msg_id).toBe('two');
});

test('delta init completes a tool whose call is in the cached prefix', () => {
  const prefix = [{ _type: 'tool_start', _msg_id: 'call-message', tool_call_id: 'tool-1', status: 'running', result: null }];
  appendNormalizedHistoryDelta(prefix, [{
    role: 'tool_result', tool_call_id: 'tool-1', content: [{ type: 'text', text: 'done' }],
  }]);
  expect(prefix[0]).toMatchObject({ status: 'done', result: 'done' });
});

test('handleWsInit appends a validated delta without replacing the prefix array', () => {
  const prefix = [{ role: 'user', _msg_id: 'one', content: [{ type: 'text', text: 'one' }] }];
  setState({ sessions: { s1: { id: 's1', messages: prefix, subagents: {} } } });
  handleWsInit('s1', {
    delta_base: 'one', messages: [{
      role: 'assistant', msg_id: 'two', content: [{ type: 'text', text: 'two' }],
    }], subagents: [], server_instance: 'instance-a',
  });
  expect(store.get().sessions.s1.messages).toBe(prefix);
  expect(prefix.map(message => message._msg_id)).toEqual(['one', 'two']);
});

test('handleWsInit rejects a delta whose base is not the durable local tail', () => {
  const base = { role: 'user', _msg_id: 'base', content: [{ type: 'text', text: 'base' }] };
  const marker = { _type: 'system', text: '✂ Context compacted' };
  setState({ sessions: { s1: { id: 's1', messages: [base, marker], subagents: {} } } });

  // Another client rewound the server tree to base. An empty delta is valid
  // server-side, but retaining the local marker would display a false path.
  handleWsInit('s1', {
    delta_base: 'base', messages: [], subagents: [], server_instance: 'instance-a',
  });

  expect(store.get().sessions.s1.messages).toEqual([]);
});

test('handleWsInit rejects a delta after any unidentifiable local row', () => {
  const base = { role: 'assistant', _msg_id: 'base', content: [{ type: 'text', text: 'base' }] };
  const local = { _type: 'tool_start', tool_call_id: 'live-tool', status: 'running' };
  setState({ sessions: { s1: { id: 's1', messages: [base, local], subagents: {} } } });

  handleWsInit('s1', {
    delta_base: 'base', messages: [], subagents: [], server_instance: 'instance-a',
  });

  expect(store.get().sessions.s1.messages).toEqual([]);
});

test('normalizeConversationProjection preserves persisted tool activity', async () => {
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

test('handleWsSubagentStart creates a running entry with async flag, thinking level, and origin tool call ID', async () => {
  seedSession('s1');
  handleWsSubagentStart('s1', { job_id: 'j1', origin_tool_call_id: 'toolu_1', task: 't', model: 'm', thinking: 'high', async: false });
  const sa = store.get().sessions.s1.subagents.j1;
  expect(sa.status).toBe('running');
  expect(sa.async).toBe(false);
  expect(sa.thinking).toBe('high');
  expect(sa.originToolCallId).toBe('toolu_1');
});

test('handleWsInit retains a subagent thinking level and origin tool call ID', async () => {
  seedSession('s1');
  handleWsInit('s1', {
    messages: [],
    subagents: [{ job_id: 'j1', origin_tool_call_id: 'toolu_init', task: 't', model: 'm', thinking: 'medium', status: 'running' }],
  });
  expect(store.get().sessions.s1.subagents.j1.thinking).toBe('medium');
  expect(store.get().sessions.s1.subagents.j1.originToolCallId).toBe('toolu_init');
});

test('subagents without a thinking level normalize to off', async () => {
  seedSession('s1');
  handleWsSubagentStart('s1', { job_id: 'j1', task: 't', model: 'm', async: false });
  expect(store.get().sessions.s1.subagents.j1.thinking).toBe('off');

  handleWsInit('s1', {
    messages: [],
    subagents: [{ job_id: 'j1', task: 't', model: 'm', status: 'running' }],
  });
  expect(store.get().sessions.s1.subagents.j1.thinking).toBe('off');
});

test('handleWsSubagentStart flips async without touching a running status', async () => {
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
test('handleWsSubagentStart does not downgrade a terminal status back to running', async () => {
  seedSession('s1');
  handleWsSubagentStart('s1', { job_id: 'j1', task: 't', model: 'm', async: false });
  handleWsSubagentEnd('s1', { job_id: 'j1', status: 'completed' });
  // Late-arriving promote echo.
  handleWsSubagentStart('s1', { job_id: 'j1', async: true });
  const sa = store.get().sessions.s1.subagents.j1;
  expect(sa.status).toBe('completed');
  expect(sa.async).toBe(true);
});

test('terminal subagent outcome is keyed by job ID and preserves result/error semantics', async () => {
  let messages = upsertTerminalSubagentOutcome([], { task: 'inspect', accentIndex: 2 }, {
    job_id: 'j1', status: 'completed', result: 'the actual child result', finished_at_ms: 10,
  });
  // A completion delivery replay must update, never add a second terminal row.
  messages = upsertTerminalSubagentOutcome(messages, { task: 'inspect', accentIndex: 2 }, {
    job_id: 'j1', status: 'completed', result: 'the actual child result', finished_at_ms: 10,
  });
  expect(messages).toHaveLength(1);
  expect(messages[0]).toMatchObject({ tool_call_id: 'subagent-j1', status: 'done', result: 'the actual child result', error: '' });

  const failed = upsertTerminalSubagentOutcome([], { task: 'inspect' }, {
    job_id: 'j2', status: 'failed', error: 'network unavailable',
  })[0];
  expect(failed).toMatchObject({ status: 'error', result: '', error: 'network unavailable' });

  const cancelled = upsertTerminalSubagentOutcome([], { task: 'inspect' }, {
    job_id: 'j3', status: 'cancelled', result: 'must not leak', error: 'must not leak',
  })[0];
  expect(cancelled).toMatchObject({ status: 'cancelled', result: '', error: '' });
});

test('terminal upsert keeps legacy parent outcome when old persisted fields are absent', async () => {
  const messages = upsertTerminalSubagentOutcome([{
    _type: 'tool_start', tool_call_id: 'subagent-old', tool_name: 'subagent',
    status: 'error', result: 'legacy failure detail', args: { task: 'old work' },
  }], { task: 'old work' }, {
    job_id: 'old', status: 'failed', error: '',
  });
  expect(messages).toHaveLength(1);
  expect(messages[0]).toMatchObject({ result: '', error: 'legacy failure detail' });
});

test('subagent_end creates the terminal card when a waiter owns model delivery', async () => {
  seedSession('s1');
  handleWsSubagentStart('s1', { job_id: 'waited', task: 'waiter path', model: 'm', async: true });
  // No subagent_complete event: subagent_wait claimed delivery to the model.
  handleWsSubagentEnd('s1', {
    job_id: 'waited', status: 'completed', result: 'wait-owned result', finished_at_ms: 12,
  });
  const cards = store.get().sessions.s1.messages.filter(m => m.tool_call_id === 'subagent-waited');
  expect(cards).toHaveLength(1);
  expect(cards[0].result).toBe('wait-owned result');
});

test('init restores persisted waiter-owned terminal outcomes without reviving the Live Dock', async () => {
  seedSession('s1');
  handleWsInit('s1', {
    messages: [], subagents: [],
    subagent_outcomes: [{ job_id: 'saved', task: 'saved work', status: 'failed', error: 'saved error' }],
  });
  const session = store.get().sessions.s1;
  expect(session.messages).toContainEqual(expect.objectContaining({ tool_call_id: 'subagent-saved', error: 'saved error' }));
  expect(liveTrayAgents(session)).toHaveLength(0);
});

test('handleWsInit dedups restored subagent cards against a running snapshot entry', async () => {
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

test('handleWsInit suppresses a canonicalized async legacy card in both stream and dock', async () => {
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

test('handleWsInit only correlates legacy cards to a unique live job', async () => {
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

test('synthetic tool ids are scoped to their persisted message across separately normalized pages', () => {
  const shell = (msgId) => ({ role: 'shell', msg_id: msgId, content: [{ type: 'text', text: '$ pwd\n/work' }] });
  const subagent = (msgId) => ({
    role: 'user', msg_id: msgId, custom: { source: 'subagent', subagent_task: 'review', subagent_status: 'completed' }, content: [],
  });
  const bash = (msgId) => ({
    role: 'user', msg_id: msgId, custom: { source: 'bash_job', bash_command: 'pwd', bash_status: 'completed' }, content: [],
  });

  for (const [name, make] of [['shell', shell], ['subagent', subagent], ['bash_complete', bash]]) {
    const first = normalizeHistory([make(`${name}-first`)])[0].tool_call_id;
    const second = normalizeHistory([make(`${name}-second`)])[0].tool_call_id;
    expect(first).not.toBe(second);
  }
});

test('handleWsInit does not conflate a legacy card with multiple live jobs sharing its task', async () => {
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

test('handleWsInit keeps a distinct live subagent when a legacy card has another task', async () => {
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

test('handleWsInit preserves the bounded-history marker', async () => {
  seedSession('s1');
  handleWsInit('s1', {
    messages: [{ role: 'assistant', msg_id: 'latest', content: [{ type: 'text', text: 'latest' }] }],
    history_truncated: true,
  });
  const session = store.get().sessions.s1;
  expect(session.historyTruncated).toBe(true);
  expect(session.messages).toHaveLength(1);
});

test('handleWsInit clears a stuck subagentCount when no live jobs remain', async () => {
  seedSession('s1');
  // An async job finished while this pane had no WS, so the terminal count=0
  // event was missed and the badge/dot stayed stuck at 1.
  setState({ sessions: { s1: { ...store.get().sessions.s1, subagentCount: 1 } } });
  handleWsInit('s1', { messages: [], subagents: [] });
  expect(store.get().sessions.s1.subagentCount).toBe(0);
});

test('handleWsInit recomputes subagentCount from live async jobs in the snapshot', async () => {
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

test('handleWsInit replaces server-ID steers with the authoritative snapshot', async () => {
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

test('handleWsInit keeps an in-flight local chip (ID not yet in snapshot) but adopts snapshot steers', async () => {
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

test('handleWsInit drops a confirmed local chip whose ID the server already dropped', async () => {
  seedSession('s1');
  setState({ sessions: { s1: { ...store.get().sessions.s1, pendingSteers: [{ id: 'c-gone', text: 'delivered already', confirmed: true }] } } });

  // The chip was confirmed (POST returned) but the snapshot no longer lists it,
  // so the server delivered/cancelled it — the stale local chip must not linger.
  handleWsInit('s1', { messages: [], pending_steers: [] });

  expect(store.get().sessions.s1.pendingSteers).toBeNull();
});

test('handleWsInit keeps an unconfirmed in-flight chip absent from the snapshot', async () => {
  seedSession('s1');
  setState({ sessions: { s1: { ...store.get().sessions.s1, pendingSteers: [{ id: 'c-inflight', text: 'just sent' }] } } });

  // The POST hasn't returned (confirmed !== true) and the snapshot predates it,
  // so the chip must survive the reconnect.
  handleWsInit('s1', { messages: [], pending_steers: [] });

  const steers = store.get().sessions.s1.pendingSteers;
  expect(steers).toHaveLength(1);
  expect(steers[0].id).toBe('c-inflight');
});

test('handleWsSteer removes the queued chip by ID, not by text', async () => {
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

test('handleWsSteer dedups the injected user message by MsgID', async () => {
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

test('handleWsSteer keeps the content blocks of a queued send with attachments', async () => {
  seedSession('s1');
  setState({ sessions: { s1: { ...store.get().sessions.s1, messages: [], pendingSteers: [{ id: 'q1', text: 'mira esto' }] } } });

  const content = [
    { type: 'image', attachment_id: 'a1', mime_type: 'image/png' },
    { type: 'text', text: 'mira esto' },
  ];
  handleWsSteer('s1', { id: 'q1', msg_id: 'm9', text: 'mira esto', content });

  const sess = store.get().sessions.s1;
  expect(sess.messages).toHaveLength(1);
  expect(sess.messages[0].content).toEqual(content);
  expect(sess.messages[0]._steer_id).toBe('q1');
  expect(sess.pendingSteers).toBeNull();
});

test('handleWsUserMessage appends a prompt sent from another client', async () => {
  seedSession('s1');
  setState({ sessions: { s1: { ...store.get().sessions.s1, messages: [] } } });

  handleWsUserMessage('s1', { msg_id: 'm1', text: 'dictado desde el móvil' });

  const messages = store.get().sessions.s1.messages;
  expect(messages).toHaveLength(1);
  expect(messages[0].role).toBe('user');
  expect(messages[0]._msg_id).toBe('m1');
  expect(messages[0].content).toEqual([{ type: 'text', text: 'dictado desde el móvil' }]);
});

test('secret batch delivery uses trusted metadata and never renders the backend note text', async () => {
  seedSession('s1');
  setState({ sessions: { s1: { ...store.get().sessions.s1, messages: [] } } });
  const text = 'A secret batch is available in /tmp/moa-secrets-42/batch-a (aliases: db-produccion, api-key). Install each secret where its relevant client expects it, then delete these files. Never print a value or copy one into the repository or into a command that would echo it.';

  handleWsUserMessage('s1', { msg_id: 'secret-1', text, custom: {
    source: 'secret_batch', secret_aliases: ['db-produccion', 'api-key'],
  } });

  expect(store.get().sessions.s1.messages).toEqual([{
    _type: 'secret_batch', _msg_id: 'secret-1', aliases: ['db-produccion', 'api-key'],
  }]);
  expect(JSON.stringify(store.get().sessions.s1.messages)).not.toContain('/tmp/moa-secrets');
});

test('ordinary text that resembles a secret note stays a user message', async () => {
  seedSession('s1');
  setState({ sessions: { s1: { ...store.get().sessions.s1, messages: [] } } });
  const text = 'A secret batch is available in /tmp with space (aliases: db). Install each secret where its relevant client expects it, then delete these files. Never print a value or copy one into the repository or into a command that would echo it.';

  handleWsUserMessage('s1', { msg_id: 'ordinary', text });

  expect(store.get().sessions.s1.messages[0]).toMatchObject({ role: 'user', _msg_id: 'ordinary' });
});

test('handleWsUserMessage dedups against the optimistic echo by MsgID', async () => {
  seedSession('s1');
  setState({ sessions: { s1: { ...store.get().sessions.s1, messages: [
    { role: 'user', _msg_id: 'm1', content: [{ type: 'text', text: 'ya pintado' }] },
  ] } } });

  handleWsUserMessage('s1', { msg_id: 'm1', text: 'ya pintado' });

  expect(store.get().sessions.s1.messages).toHaveLength(1);
});

test('handleWsUserMessage keeps the content blocks of a send with attachments', async () => {
  seedSession('s1');
  setState({ sessions: { s1: { ...store.get().sessions.s1, messages: [] } } });

  const content = [
    { type: 'image', attachment_id: 'a1', mime_type: 'image/png' },
    { type: 'text', text: 'mira esto' },
  ];
  handleWsUserMessage('s1', { msg_id: 'm2', content });

  expect(store.get().sessions.s1.messages[0].content).toEqual(content);
});

test('model-facing subagent completion user/steer notifications are hidden while structured lifecycle owns presentation', async () => {
  seedSession('s1');
  setState({ sessions: { s1: { ...store.get().sessions.s1, messages: [] } } });
  handleWsSubagentStart('s1', { job_id: 'live', task: 'inspect', model: 'm', async: true });
  const text = '[subagent completed] Job live finished.\nTask: inspect\n\nResult:\nmodel notification';

  handleWsSteer('s1', { id: 'notification', msg_id: 'steer-notification', text });
  expect(store.get().sessions.s1.messages).toHaveLength(0);
  expect(store.get().sessions.s1.pendingSteers).toBeNull();

  handleWsUserMessage('s1', { msg_id: 'user-notification', text });
  expect(store.get().sessions.s1.messages).toHaveLength(0);

  handleWsSubagentEnd('s1', { job_id: 'live', status: 'completed', result: 'structured result', finished_at_ms: 20 });
  expect(store.get().sessions.s1.messages).toHaveLength(1);
  expect(store.get().sessions.s1.messages[0]).toMatchObject({ tool_call_id: 'subagent-live', result: 'structured result' });
});

test('persisted terminal outcomes restore chronologically by finished_at_ms', async () => {
  seedSession('s1');
  handleWsInit('s1', {
    messages: [], subagents: [],
    // Deliberately newest-first, matching SubagentStore.List.
    subagent_outcomes: [
      { job_id: 'late', task: 'late', status: 'completed', result: 'late', finished_at_ms: 30 },
      { job_id: 'early', task: 'early', status: 'completed', result: 'early', finished_at_ms: 10 },
    ],
  });
  expect(store.get().sessions.s1.messages.map(m => m.tool_call_id)).toEqual(['subagent-early', 'subagent-late']);
});

test('persisted terminal outcomes insert before parent messages written after completion', async () => {
  seedSession('s1');
  handleWsInit('s1', {
    messages: [
      { role: 'user', msg_id: 'before', timestamp: 10, content: [{ type: 'text', text: 'before child' }] },
      { role: 'assistant', msg_id: 'after', timestamp: 30, content: [{ type: 'text', text: 'after child' }] },
    ],
    subagents: [],
    subagent_outcomes: [{ job_id: 'waited', task: 'waited', status: 'completed', result: 'child result', finished_at_ms: 20_000 }],
  });
  const messages = store.get().sessions.s1.messages;
  expect(messages.map(m => m.tool_call_id || m._msg_id)).toEqual(['before', 'subagent-waited', 'after']);
});

test('async notification outcome uses the same completion position live and after reload', async () => {
  const rawHistory = [
    { role: 'user', msg_id: 'before', timestamp: 10, content: [{ type: 'text', text: 'before' }] },
    { role: 'user', msg_id: 'notification', timestamp: 30, custom: {
      source: 'subagent', subagent_job_id: 'async', subagent_task: 'async work',
      subagent_status: 'completed', subagent_result: 'notification result',
    }, content: [{ type: 'text', text: 'model delivery' }] },
    { role: 'assistant', msg_id: 'after', timestamp: 40, content: [{ type: 'text', text: 'after' }] },
  ];
  const outcome = { job_id: 'async', task: 'async work', status: 'completed', result: 'structured result', finished_at_ms: 20_000 };

  seedSession('live');
  setState({ sessions: { ...store.get().sessions, live: { id: 'live', messages: normalizeHistory(rawHistory), subagents: {} } } });
  handleWsSubagentStart('live', { job_id: 'async', task: 'async work', model: 'm', async: true });
  handleWsSubagentEnd('live', outcome);
  const liveOrder = store.get().sessions.live.messages.map(m => m.tool_call_id || m._msg_id);

  seedSession('reload');
  handleWsInit('reload', { messages: rawHistory, subagents: [], subagent_outcomes: [outcome] });
  const reloadOrder = store.get().sessions.reload.messages.map(m => m.tool_call_id || m._msg_id);

  expect(liveOrder).toEqual(['before', 'subagent-async', 'after']);
  expect(reloadOrder).toEqual(liveOrder);
});

test('handleWsSteersCanceled clears the shared queue on every client', async () => {
  seedSession('s1');
  setState({ sessions: { s1: { ...store.get().sessions.s1, pendingSteers: [
    { id: 'srv', text: 'queued' },
    { id: 'c-local', text: 'just typed' },
  ] } } });

  handleWsSteersCanceled('s1');

  expect(store.get().sessions.s1.pendingSteers).toBeNull();
});

test('handleWsCommandQueued appends a command chip and confirms an optimistic one by ID', async () => {
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

test('handleWsCommandDequeued removes the command chip; a failure surfaces a toast', async () => {
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

test('mergeSteers carries command/images through a reconnect snapshot', async () => {
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

test('handleWsRunEnd keeps genuinely queued steers (mostrar la verdad)', async () => {
  seedSession('s1');
  setState({ sessions: { s1: { ...store.get().sessions.s1, messages: [], state: 'running', pendingSteers: [{ id: 'q1', text: 'do this next' }] } } });

  handleWsRunEnd('s1');

  const steers = store.get().sessions.s1.pendingSteers;
  expect(steers).toHaveLength(1);
  expect(steers[0].id).toBe('q1');
});




test('handleWsInit clears a stale compacting spinner from the snapshot', async () => {
  seedSession('s1');
  setState({ sessions: { s1: { ...store.get().sessions.s1, compacting: true } } });

  // Reconnect: the server's snapshot says no compaction is in progress.
  handleWsInit('s1', { messages: [] });

  expect(store.get().sessions.s1.compacting).toBe(false);
});

// Regression: a compaction still running at reconnect must restore
// the spinner from the snapshot.
test('handleWsInit restores an in-progress compacting spinner from the snapshot', async () => {
  seedSession('s1');
  handleWsInit('s1', { messages: [], compacting: true });

  expect(store.get().sessions.s1.compacting).toBe(true);
});

// Regression: a reconnect during generation must restore the whole
// streamed-so-far reply from the snapshot, not start from the next delta.
test('handleWsInit restores the in-flight streamed reply from the snapshot', async () => {
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
test('handleWsInit clears streaming buffers when nothing is in flight', async () => {
  seedSession('s1');
  setState({ sessions: { s1: { ...store.get().sessions.s1, streamingText: 'stale', thinkingText: 'stale' } } });

  handleWsInit('s1', { messages: [] });

  expect(store.get().sessions.s1.streamingText).toBe(null);
  expect(store.get().sessions.s1.thinkingText).toBe(null);
});

// Regression: persisted goal-lifecycle markers (role "goal") must
// rebuild as system lines so a reopened conversation shows the goal record.
test('normalizeHistory renders role "goal" markers as system lines', async () => {
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

test('normalizeHistory preserves the durable compaction marker contract', () => {
  const [marker] = normalizeHistory([{
    role: 'session_event', msg_id: 'compact-entry',
    content: [{ type: 'text', text: '✂ Context compacted (24K tokens summarized)' }],
    custom: {
      type: 'compaction_marker', summary: 'Keep the plan.', tokens_before: 24236,
      read_files: ['a.go'], modified_files: ['b.go'],
    },
  }]);
  expect(marker).toEqual({
    _type: 'compaction_marker', _msg_id: 'compact-entry', timestamp: undefined,
    summary: 'Keep the plan.', tokensBefore: 24236, readFiles: ['a.go'], modifiedFiles: ['b.go'],
  });
});

test('live compact command appends the same durable marker shape as persisted history', () => {
  seedSession('s1');
  setState({ sessions: { s1: { ...store.get().sessions.s1, messages: [] } } });
  const raw = {
    role: 'session_event', msg_id: 'compact-entry', content: [{ type: 'text', text: '✂ Context compacted' }],
    custom: { type: 'compaction_marker', summary: 'Keep the plan.', tokens_before: 24000, read_files: ['a.go'], modified_files: ['b.go'] },
  };
  handleWsCommand('s1', { command: 'compact', messages: [raw] });
  expect(store.get().sessions.s1.messages).toEqual(normalizeHistory([raw]));
});

test('live compact command without a durable marker waits for history instead of inventing a transient system line', () => {
  seedSession('s1');
  setState({ sessions: { s1: { ...store.get().sessions.s1, messages: [] } } });
  handleWsCommand('s1', { command: 'compact', messages: [] });
  expect(store.get().sessions.s1.messages).toEqual([]);
});

test('live compaction end appends the durable marker normalized like init history', () => {
  seedSession('s1');
  const raw = {
    role: 'session_event', msg_id: 'compact-entry', content: [{ type: 'text', text: '✂ Context compacted' }],
    custom: { type: 'compaction_marker', summary: 'Keep the plan.', tokens_before: 24000, read_files: ['a.go'], modified_files: ['b.go'] },
  };
  handleWsCompactionEnd('s1', { marker: raw });
  expect(store.get().sessions.s1.messages).toEqual(normalizeHistory([raw]));
  expect(projectStream(store.get().sessions.s1).find(block => block.kind === 'compaction')).toMatchObject({ id: 'compaction-compact-entry-0', summary: 'Keep the plan.' });
});

test('init after a live compaction marker keeps exactly one durable card', () => {
  seedSession('s1');
  const raw = {
    role: 'session_event', msg_id: 'compact-entry', content: [{ type: 'text', text: '✂ Context compacted' }],
    custom: { type: 'compaction_marker', summary: 'Keep the plan.', tokens_before: 24000 },
  };
  handleWsCompactionEnd('s1', { marker: raw });
  handleWsInit('s1', { messages: [raw], subagents: [] });
  const cards = projectStream(store.get().sessions.s1).filter(block => block.kind === 'compaction');
  expect(cards).toHaveLength(1);
  expect(cards[0].id).toBe('compaction-compact-entry-0');
});

test('a compaction end without marker remains safe until init supplies the durable card', () => {
  seedSession('s1');
  const raw = {
    role: 'session_event', msg_id: 'compact-entry', content: [{ type: 'text', text: '✂ Context compacted' }],
    custom: { type: 'compaction_marker', summary: 'Keep the plan.', tokens_before: 24000 },
  };
  handleWsCompactionEnd('s1', {});
  expect(store.get().sessions.s1.messages).toEqual([]);
  handleWsInit('s1', { messages: [raw], subagents: [] });
  expect(projectStream(store.get().sessions.s1).filter(block => block.kind === 'compaction')).toHaveLength(1);
});

// Bug #7 parity: a fresh goal activation shows a live "start" line (matching the
// persisted marker rendered on reopen); a re-announcement must not duplicate it.
test('handleWsGoalChange adds a live start line once on fresh activation', async () => {
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
test('handleWsGoalVerify toggles goalVerifying', async () => {
  seedSession('s1');
  setState({ sessions: { s1: { ...store.get().sessions.s1, goalActive: true } } });

  handleWsGoalVerify('s1', { active: true, iteration: 2 });
  expect(store.get().sessions.s1.goalVerifying).toBe(true);

  handleWsGoalVerify('s1', { active: false, iteration: 2 });
  expect(store.get().sessions.s1.goalVerifying).toBe(false);
});

import { getToasts } from './notifications.js';

// Bug: an OpenAI usage-limit (429) ends the run in the "error" state. The web
// must surface the reason (parity with the TUI's run-end error block), not stay
// silent. The session is kept visible so the focused-tile path runs (no
// navigator-dependent attention notification).
test('handleWsStateChange surfaces a quota error as a toast', async () => {
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
test('handleWsStateChange does not toast on a normal idle end', async () => {
  setState({ sessions: { s1: { id: 's1', state: 'running', subagents: {} } }, isMobile: true, activeSession: 's1' });
  const before = getToasts().length;
  handleWsStateChange('s1', { state: 'idle' });
  expect(getToasts().length).toBe(before);
});

test('handleWsBashComplete adds a bash card to the chat', async () => {
  setState({ sessions: { s1: { id: 's1', subagents: {}, messages: [] } } });
  handleWsBashComplete('s1', { job_id: 'bash-1', command: 'sleep 5; echo done', status: 'completed', text: '[bash job completed] Job bash-1 finished.\nCommand: sleep 5; echo done\n\nOutput:\ndone' });
  const msgs = store.get().sessions.s1.messages;
  const card = msgs[msgs.length - 1];
  expect(card._type).toBe('tool_start');
  expect(card.tool_name).toBe('bash');
  expect(card.status).toBe('done');
  expect(card.args.command).toBe('sleep 5; echo done');
});

test('owned async bash stays in its subagent transcript, not the root dock or ledger', async () => {
  seedSession('s1');
  handleWsSubagentStart('s1', { job_id: 'child-1', task: 'Inspect', model: 'm', async: true });
  handleWsBashJobStart('s1', {
    job_id: 'bash-1', owner_agent_id: 'child-1', command: 'go test ./...', cwd: '/work', status: 'running',
  });
  handleWsBashJobEnd('s1', {
    job_id: 'bash-1', owner_agent_id: 'child-1', status: 'completed', output: 'ok\n',
  });
  handleWsBashComplete('s1', {
    job_id: 'bash-1', owner_agent_id: 'child-1', command: 'go test ./...', status: 'completed', text: 'Bash completed',
  });

  const session = store.get().sessions.s1;
  expect(session.subagents['bash-1']).toBeUndefined();
  expect(session.subagents['child-1'].messages).toContainEqual(expect.objectContaining({
    tool_call_id: 'bash-1', tool_name: 'bash', status: 'done', result: 'ok\n',
  }));
  expect(session.messages || []).toEqual([]);
  expect(liveTrayAgents(session).filter(chip => chip.kind === 'bash')).toEqual([]);
});

test('root async bash remains a root dock job and completion ledger card', async () => {
  seedSession('s1');
  handleWsBashJobStart('s1', { job_id: 'bash-1', command: 'go test ./...', cwd: '/work', status: 'running' });
  let session = store.get().sessions.s1;
  expect(liveTrayAgents(session)).toContainEqual(expect.objectContaining({ id: 'bash-1', kind: 'bash' }));

  handleWsBashJobEnd('s1', { job_id: 'bash-1', status: 'completed', output: 'ok\n' });
  handleWsBashComplete('s1', { job_id: 'bash-1', command: 'go test ./...', status: 'completed', text: 'Bash completed' });
  session = store.get().sessions.s1;
  expect(session.messages).toContainEqual(expect.objectContaining({ tool_call_id: 'bash-complete-bash-1', tool_name: 'bash' }));
});

test('init routes an owned bash snapshot into its subagent transcript', async () => {
  seedSession('s1');
  handleWsInit('s1', {
    messages: [],
    subagents: [{ job_id: 'child-1', task: 'Inspect', model: 'm', status: 'running', async: true }],
    bash_jobs: [{ job_id: 'bash-1', owner_agent_id: 'child-1', command: 'go test ./...', cwd: '/work', status: 'running', output: 'live\n' }],
  });
  const session = store.get().sessions.s1;
  expect(session.subagents['bash-1']).toBeUndefined();
  expect(session.subagents['child-1'].messages).toContainEqual(expect.objectContaining({
    tool_call_id: 'bash-1', tool_name: 'bash', streamingResult: 'live\n',
  }));
  expect(liveTrayAgents(session).filter(chip => chip.kind === 'bash')).toEqual([]);
});

test('init retains a terminal bash owner so its result stays under that child', async () => {
  seedSession('s1');
  handleWsInit('s1', {
    messages: [],
    subagents: [{
      job_id: 'child-1', task: 'Inspect', model: 'm', status: 'completed', async: true,
      messages: [{ role: 'assistant', content: [{ type: 'text', text: 'Inspection complete.' }] }],
    }],
    bash_jobs: [{ job_id: 'bash-1', owner_agent_id: 'child-1', command: 'go test ./...', cwd: '/work', status: 'running', output: 'live\n' }],
  });

  let session = store.get().sessions.s1;
  expect(session.subagents['child-1']).toMatchObject({ task: 'Inspect', model: 'm', status: 'completed' });
  expect(session.subagents['child-1'].messages).toContainEqual(expect.objectContaining({
    tool_call_id: 'bash-1', tool_name: 'bash', streamingResult: 'live\n',
  }));
  expect(session.messages).toEqual([]);
  expect(liveTrayAgents(session)).toEqual([]);

  handleWsBashJobEnd('s1', {
    job_id: 'bash-1', owner_agent_id: 'child-1', status: 'completed', output: 'ok\n',
  });
  session = store.get().sessions.s1;
  expect(session.subagents['child-1'].status).toBe('completed');
  expect(session.subagents['child-1'].messages).toContainEqual(expect.objectContaining({
    tool_call_id: 'bash-1', status: 'done', result: 'ok\n',
  }));
  expect(liveTrayAgents(session)).toEqual([]);
});

test('a start-before-subagent placeholder becomes terminal when its bash ends', async () => {
  seedSession('s1');
  handleWsBashJobStart('s1', {
    job_id: 'bash-1', owner_agent_id: 'child-1', command: 'go test ./...', status: 'running',
  });
  expect(store.get().sessions.s1.subagents['child-1']).toMatchObject({
    status: 'running', syntheticOwnedBashOwner: true,
  });

  handleWsBashJobEnd('s1', {
    job_id: 'bash-1', owner_agent_id: 'child-1', status: 'completed', output: 'ok\n',
  });
  const owner = store.get().sessions.s1.subagents['child-1'];
  expect(owner.status).toBe('completed');
  expect(owner.messages).toContainEqual(expect.objectContaining({ tool_call_id: 'bash-1', status: 'done', result: 'ok\n' }));
  expect(liveTrayAgents(store.get().sessions.s1)).toEqual([]);
});

test('normalizeHistory carries the job ID the subagent tool recorded on its result', async () => {
  const raw = [
    { role: 'assistant', msg_id: 'a1', content: [{ type: 'tool_call', tool_call_id: 'toolu_1', tool_name: 'subagent', arguments: { task: 'work' } }] },
    { role: 'tool_result', tool_call_id: 'toolu_1', custom: { subagent_job_id: 'sa-1' }, content: [{ type: 'text', text: 'done' }] },
  ];
  const out = normalizeHistory(raw);
  expect(out[0]).toEqual(expect.objectContaining({ tool_name: 'subagent', subagentJobId: 'sa-1' }));
});

test('normalizeHistory recovers a completed subagent job ID from its notification', async () => {
  const raw = [{
    role: 'user',
    content: [{ type: 'text', text: '[subagent completed] Job sa-9 finished.\nTask: review\nwith reproduction steps\n\nResult (truncated — use subagent_status for full output):\nlast line' }],
  }];
  const out = normalizeHistory(raw);
  expect(out[0]).toEqual(expect.objectContaining({
    tool_name: 'subagent', tool_call_id: 'subagent-sa-9', subagentJobId: 'sa-9',
    args: { task: 'review\nwith reproduction steps' }, result: 'last line',
  }));
});

test('normalizeHistory reloads a bash_job custom notification as a bash card', async () => {
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

test('normalizeHistory reloads a prefix-based bash notification (no custom)', async () => {
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

test('handleWsRateLimit stores OpenAI usage globally without touching Anthropic', async () => {
  setState({
    sessions: { s1: { id: 's1', provider: 'openai', subagents: {} } },
    usage: { available: true, five_hour: { utilization: 10 }, seven_day: { utilization: 20 } },
  });

  handleWsRateLimit('s1', { five_hour_pct: 40, seven_day_pct: 51, on_overage: false });

  const sess = store.get().sessions.s1;
  expect(sess.rlFiveHourPct).toBe(40);
  expect(sess.rlSevenDayPct).toBe(51);
  expect(store.get().usage.providers.openai.five_hour.utilization).toBe(40);
  expect(store.get().usage.providers.openai.seven_day.utilization).toBe(51);
  // Anthropic's legacy global snapshot must be untouched by an OpenAI session.
  expect(store.get().usage.five_hour.utilization).toBe(10);
  expect(store.get().usage.seven_day.utilization).toBe(20);
});

test('handleWsRateLimit patches the global snapshot for Anthropic sessions', async () => {
  setState({
    sessions: { s1: { id: 's1', provider: 'anthropic', subagents: {} } },
    usage: { available: true, five_hour: { utilization: 10 }, seven_day: { utilization: 20 } },
  });

  handleWsRateLimit('s1', { five_hour_pct: 40, seven_day_pct: 51, on_overage: false });

  expect(store.get().usage.five_hour.utilization).toBe(40);
  expect(store.get().usage.seven_day.utilization).toBe(51);
  expect(store.get().sessions.s1.rlFiveHourPct).toBe(40);
});

test('handleWsRateLimit ignores unknown windows (pct < 0)', async () => {
  setState({
    sessions: { s1: { id: 's1', provider: 'openai', subagents: {} } },
    usage: null,
  });

  handleWsRateLimit('s1', { five_hour_pct: 40, seven_day_pct: -1, on_overage: false });

  const sess = store.get().sessions.s1;
  expect(sess.rlFiveHourPct).toBe(40);
  expect(sess.rlSevenDayPct).toBeUndefined();
  expect(store.get().usage.providers.openai.five_hour.utilization).toBe(40);
  expect(store.get().usage.providers.openai.seven_day).toBeUndefined();
});

test('handleWsRateLimit isolates providers in a mixed layout', async () => {
  setState({
    sessions: {
      a: { id: 'a', provider: 'anthropic', subagents: {} },
      o: { id: 'o', provider: 'openai', subagents: {} },
    },
    usage: { available: true, five_hour: { utilization: 5 }, seven_day: { utilization: 6 } },
  });

  // OpenAI updates its provider-qualified global snapshot, not Anthropic's.
  handleWsRateLimit('o', { five_hour_pct: 80, seven_day_pct: 90, on_overage: false });
  expect(store.get().sessions.o.rlFiveHourPct).toBe(80);
  expect(store.get().usage.five_hour.utilization).toBe(5);
  expect(store.get().usage.providers.openai.five_hour.utilization).toBe(80);

  // Anthropic patches its global snapshot; OpenAI's provider data stays put.
  handleWsRateLimit('a', { five_hour_pct: 30, seven_day_pct: 40, on_overage: false });
  expect(store.get().usage.five_hour.utilization).toBe(30);
  expect(store.get().usage.providers.openai.five_hour.utilization).toBe(80);
  expect(store.get().sessions.o.rlFiveHourPct).toBe(80);
  expect(store.get().sessions.a.rlFiveHourPct).toBe(30);
});

// --- Per-run logical token tally ---
test('handleWsRunTokens sets authoritative logical traffic totals', async () => {
  setState({ sessions: { s1: { id: 's1', subagents: {}, messages: [] } } });
  handleWsRunTokens('s1', { up: 1200, down: 300 });
  expect(store.get().sessions.s1.runTokensUp).toBe(1200);
  expect(store.get().sessions.s1.runTokensDown).toBe(300);
});

test('handleWsInit rehydrates run token totals', async () => {
  setState({ sessions: { s1: { id: 's1', subagents: {}, messages: [], runTokensUp: 99, runTokensDown: 88 } } });
  handleWsInit('s1', { messages: [], run_tokens_up: 1200, run_tokens_down: 300 });
  expect(store.get().sessions.s1.runTokensUp).toBe(1200);
  expect(store.get().sessions.s1.runTokensDown).toBe(300);
});

// --- Live MCP summary + panel re-fetch tick ---
import { handleWsMcpChange } from './ws-handlers.js';

test('handleWsMcpChange updates the summary and bumps mcpTick', async () => {
  setState({ sessions: { s1: { id: 's1', subagents: {}, messages: [] } } });
  handleWsMcpChange('s1', { total: 3, ready: 1, disabled: 1, unhealthy: 1, pending: 1 });
  const s = store.get().sessions.s1;
  expect(s.mcp).toEqual({ total: 3, ready: 1, disabled: 1, unhealthy: 1, pending: 1 });
  expect(s.mcpTick).toBe(1);
  // A second event increments the tick so an open panel re-fetches again.
  handleWsMcpChange('s1', { total: 3, ready: 2, disabled: 1, unhealthy: 0, pending: 0 });
  expect(store.get().sessions.s1.mcpTick).toBe(2);
  expect(store.get().sessions.s1.mcp.unhealthy).toBe(0);
});

test('handleWsInit keeps the finished subagent being viewed', async () => {
  // The init snapshot lists live jobs only. Reconnecting (screen sleep, network
  // change) must not delete a finished transcript the reader has open, or the
  // subagent view bounces back to the parent mid-read.
  setState({ sessions: { s1: { id: 's1', messages: [], viewingSubagent: 'sa-done',
    subagents: { 'sa-done': { jobId: 'sa-done', status: 'completed', messages: [] } } } } });

  handleWsInit('s1', { messages: [], subagents: [{ job_id: 'sa-live', status: 'running' }] });

  const subs = store.get().sessions.s1.subagents;
  expect(subs['sa-done']).toBeTruthy();
  expect(subs['sa-live']).toBeTruthy();
});

test('handleWsInit does not resurrect finished subagents nobody is viewing', async () => {
  setState({ sessions: { s1: { id: 's1', messages: [], viewingSubagent: null,
    subagents: { 'sa-old': { jobId: 'sa-old', status: 'completed', messages: [] } } } } });

  handleWsInit('s1', { messages: [], subagents: [] });

  expect(store.get().sessions.s1.subagents['sa-old']).toBeUndefined();
});

test('handleWsInit prefers the server copy when the viewed job is still live', async () => {
  setState({ sessions: { s1: { id: 's1', messages: [], viewingSubagent: 'sa-1',
    subagents: { 'sa-1': { jobId: 'sa-1', status: 'running', task: 'stale' } } } } });

  handleWsInit('s1', { messages: [], subagents: [{ job_id: 'sa-1', status: 'running', task: 'fresh' }] });

  expect(store.get().sessions.s1.subagents['sa-1'].task).toBe('fresh');
});

test('handleWsInit keeps the finished bash job being read in its detail view', async () => {
  // A reconnect (mobile screen sleep) happens constantly while watching a long
  // job. Once the job ends the server stops listing it, and dropping its entry
  // would eject the reader back to the conversation mid-read.
  setState({ sessions: { s1: { id: 's1', messages: [], viewingBashJob: 'bash-1',
    subagents: { 'bash-1': { jobId: 'bash-1', kind: 'bash', status: 'completed', task: 'go test ./...', messages: [] } } } } });

  handleWsInit('s1', { messages: [], subagents: [], bash_jobs: [] });

  expect(store.get().sessions.s1.subagents['bash-1']).toBeTruthy();
});

test('handleWsInit rebuilds a live bash job being read from the snapshot output', async () => {
  // The accumulated output travels in the snapshot, so the detail view repaints
  // itself after a reconnect without any special casing in the component.
  setState({ sessions: { s1: { id: 's1', messages: [], viewingBashJob: 'bash-1',
    subagents: { 'bash-1': { jobId: 'bash-1', kind: 'bash', status: 'running', task: 'go test ./...', messages: [] } } } } });

  handleWsInit('s1', {
    messages: [],
    bash_jobs: [{ job_id: 'bash-1', command: 'go test ./...', cwd: '/work', status: 'running', output: 'compiling\n' }],
  });

  const view = bashJobView(store.get().sessions.s1, 'bash-1');
  expect(view.command).toBe('go test ./...');
  expect(view.lines).toEqual(['compiling']);
  expect(view.canCancel).toBe(true);
});

test('a bash job ending while it is being read settles terminal instead of vanishing', async () => {
  seedSession('s1');
  handleWsBashJobStart('s1', { job_id: 'bash-1', command: 'go test ./...', cwd: '/work', status: 'running' });
  setState({ sessions: { ...store.get().sessions, s1: { ...store.get().sessions.s1, viewingBashJob: 'bash-1' } } });

  handleWsBashJobEnd('s1', { job_id: 'bash-1', status: 'failed', output: 'FAIL\n' });

  const view = bashJobView(store.get().sessions.s1, 'bash-1');
  expect(view.terminal).toBe(true);
  expect(view.outcome).toBe('failed');
  expect(view.canCancel).toBe(false);
  expect(view.lines).toEqual(['FAIL']);
});

// ── live tool calls in the init snapshot ──────────────────────────────────
// Regression for "switch conversation and come back → the live row degrades to
// a generic 'Calling'": a tool call still generating args or still executing is
// in no message history, so the snapshot carries it separately.

test('handleWsInit rebuilds a live row for a tool that is still executing', async () => {
  seedSession('s1');
  handleWsInit('s1', {
    messages: [],
    live_tools: [{
      tool_call_id: 'tc1', tool_name: 'bash', args: { command: 'go test ./...' },
      status: 'running', started_at_ms: 1_700_000_000_000,
    }],
  });
  const rows = store.get().sessions.s1.messages.filter(m => m._type === 'tool_start');
  expect(rows).toHaveLength(1);
  // The real tool name is what keeps liveVerb off its 'Calling' fallback.
  expect(rows[0]).toMatchObject({
    tool_call_id: 'tc1', tool_name: 'bash', status: 'running',
    args: { command: 'go test ./...' }, startedAt: 1_700_000_000_000,
  });
  expect(liveVerb(rows[0].tool_name)).toBe('Running');
});

test('handleWsInit restores a tool call whose arguments are still streaming', async () => {
  seedSession('s1');
  handleWsInit('s1', {
    messages: [],
    live_tools: [{ tool_call_id: 'tc1', tool_name: 'edit', args: { path: 'pkg/serve/ws.go' }, status: 'generating' }],
  });
  const [row] = store.get().sessions.s1.messages.filter(m => m._type === 'tool_start');
  expect(row).toMatchObject({ tool_name: 'edit', status: 'generating', args: { path: 'pkg/serve/ws.go' } });
  expect(liveVerb(row.tool_name)).toBe('Editing');
});

test('handleWsInit does not duplicate a live tool already present in history', async () => {
  seedSession('s1');
  handleWsInit('s1', {
    messages: [{
      role: 'assistant',
      content: [{ type: 'tool_call', tool_call_id: 'tc1', tool_name: 'bash', arguments: { command: 'sleep 120' } }],
    }],
    live_tools: [{ tool_call_id: 'tc1', tool_name: 'bash', args: { command: 'sleep 120' }, status: 'running', started_at_ms: 42 }],
  });
  const rows = store.get().sessions.s1.messages.filter(m => m._type === 'tool_start');
  expect(rows).toHaveLength(1);
  // The registry is authoritative for the live phase and the start anchor.
  expect(rows[0]).toMatchObject({ tool_call_id: 'tc1', status: 'running', startedAt: 42 });
});

test('a live event arriving after the snapshot updates the restored row instead of duplicating it', async () => {
  seedSession('s1');
  handleWsInit('s1', {
    messages: [],
    live_tools: [{ tool_call_id: 'tc1', tool_name: 'edit', args: { path: 'a.go' }, status: 'generating', started_at_ms: 7 }],
  });
  handleWsToolStart('s1', { tool_call_id: 'tc1', tool_name: 'edit', args: { path: 'a.go', oldText: 'x' } });
  const rows = store.get().sessions.s1.messages.filter(m => m._type === 'tool_start');
  expect(rows).toHaveLength(1);
  expect(rows[0]).toMatchObject({ status: 'running', args: { path: 'a.go', oldText: 'x' }, startedAt: 7 });

  handleWsToolEnd('s1', { tool_call_id: 'tc1', tool_name: 'edit', result: 'ok', is_error: false });
  const after = store.get().sessions.s1.messages.filter(m => m._type === 'tool_start');
  expect(after).toHaveLength(1);
  expect(after[0].status).toBe('done');
});

test('handleWsInit without live tools leaves history untouched', async () => {
  seedSession('s1');
  handleWsInit('s1', { messages: [], subagents: [] });
  expect(store.get().sessions.s1.messages).toHaveLength(0);
});
