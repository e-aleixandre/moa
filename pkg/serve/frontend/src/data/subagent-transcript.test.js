// subagent-transcript.test.js — run with `bun test`
//
// Covers the backfill of a subagent that was launched while its parent
// conversation was already open: its store entry only ever held the deltas
// received since, so the delegated task (a user message with
// custom.source === 'subagent_parent') never showed.
import { test, expect, beforeEach } from 'bun:test';

const { store, setState } = await import('./store.js');
const {
  hydrateSubagentTranscript, mergeSubagentTranscript, needsTranscriptHydration,
} = await import('./subagent-transcript.js');
const { subagentView } = await import('./subagent-view-model.js');
const { handleWsInit, handleWsSubagentEvent } = await import('./ws-handlers.js');
const { flushSubagentEvents } = await import('./ws/subagents.js');
const { liveTrayAgents } = await import('./stream-model.js');

let requests = [];
let responder = () => ({});

beforeEach(() => {
  requests = [];
  responder = () => ({});
  globalThis.fetch = (path) => {
    requests.push(path);
    const response = responder(path);
    if (response instanceof Response) return Promise.resolve(response);
    return Promise.resolve(new Response(JSON.stringify(response), { status: 200 }));
  };
  setState({ sessions: {}, tileTree: null, activeSession: null });
});

const seed = (subagent) => setState({
  sessions: { s1: { id: 's1', messages: [], subagents: { j1: { jobId: 'j1', status: 'running', async: true, messages: [], ...subagent } } } },
});

const taskItem = { id: 'm-task', role: 'user', source: 'subagent_parent', text: 'Investiga el bug' };
const assistantItem = (id, text) => ({ id, role: 'assistant', text });

const page = (messages, over = {}) => ({
  session_id: 's1', job_id: 'j1', task: 'Investiga el bug', status: 'running',
  order: 'newest_first', messages: [...messages].reverse(), has_more: false, ...over,
});

test('matching repeated tools never moves a new execution before its parent task', () => {
  const newTask = { ...taskItem, id: 'new-task', text: 'Continue the investigation' };
  const messages = mergeSubagentTranscript([
    taskItem,
    { id: 'tool:old:0', role: 'tool', tool: 'bash', action: 'bash', target: 'go test ./...', status: 'ok' },
    newTask,
  ], [
    { role: 'user', _msg_id: 'new-task', custom: { source: 'subagent_parent' }, content: [{ type: 'text', text: newTask.text }] },
    { _type: 'tool_start', tool_call_id: 'current-call', tool_name: 'bash', args: { command: 'go test ./...' }, status: 'done' },
  ]);
  expect(messages).toHaveLength(4);
  expect(messages[0]._msg_id).toBe(taskItem.id);
  expect(messages[1]._type).toBe('tool_start');
  expect(messages[2]._msg_id).toBe(newTask.id);
  expect(messages[3].tool_call_id).toBe('current-call');
});

test('opening a live subagent with an empty transcript shows the parent task', async () => {
  seed({ messages: [] });
  responder = () => page([taskItem, assistantItem('m1', 'Empiezo por el ws-handler')]);

  await hydrateSubagentTranscript('s1', 'j1');

  const messages = store.get().sessions.s1.subagents.j1.messages;
  expect(messages[0].custom.source).toBe('subagent_parent');
  expect(messages[0].content[0].text).toBe('Investiga el bug');
  // The task must still render as a parent waypoint, never as a user steer.
  const blocks = subagentView(store.get().sessions.s1, 'j1').blocks;
  expect(blocks[0].kind).toBe('waypoint');
  expect(blocks[0].fromParent).toBe(true);
  expect(blocks[0].steer).toBeUndefined();
});

test('deltas that arrive while the transcript is loading are neither lost nor duplicated', async () => {
  seed({ messages: [] });
  // The fetch resolves only after a live message_end has already landed in the
  // store, so the merge sees a live suffix the response also contains.
  responder = () => {
    const current = store.get().sessions.s1;
    setState({ sessions: { s1: { ...current, subagents: { ...current.subagents, j1: {
      ...current.subagents.j1,
      messages: [
        { role: 'assistant', _msg_id: 'm1', content: [{ type: 'text', text: 'Empiezo por el ws-handler' }] },
        { role: 'assistant', _msg_id: 'm2', content: [{ type: 'text', text: 'Encontrado' }] },
      ],
    } } } } });
    return page([taskItem, assistantItem('m1', 'Empiezo por el ws-handler')]);
  };

  await hydrateSubagentTranscript('s1', 'j1');

  const messages = store.get().sessions.s1.subagents.j1.messages;
  expect(messages.map((m) => m.content[0].text)).toEqual([
    'Investiga el bug', 'Empiezo por el ws-handler', 'Encontrado',
  ]);
});

test('an entry that already holds the parent task is not refetched', async () => {
  seed({ messages: [{ role: 'user', _msg_id: 'm-task', custom: { source: 'subagent_parent' }, content: [{ type: 'text', text: 'Investiga el bug' }] }] });

  expect(needsTranscriptHydration(store.get().sessions.s1.subagents.j1)).toBe(false);
  await hydrateSubagentTranscript('s1', 'j1');
  expect(requests).toEqual([]);
});

test('a viewed job missing from the live snapshot rechecks its persisted terminal status', async () => {
  const parentTask = {
    role: 'user', _msg_id: 'm-task', custom: { source: 'subagent_parent' },
    content: [{ type: 'text', text: 'Investiga el bug' }],
  };
  seed({ messages: [parentTask], transcriptHydrated: true });
  const current = store.get().sessions.s1;
  setState({ sessions: { s1: { ...current, viewingSubagent: 'j1' } } });

  // The launch has fallen outside the bounded history tail, so init cannot
  // attach this completed job's outcome. It is also absent from subagents,
  // which is authoritative for live jobs.
  handleWsInit('s1', {
    messages: Array.from({ length: 150 }, (_, i) => ({
      role: 'assistant', msg_id: `filler-${i}`, content: [{ type: 'text', text: `filler ${i}` }],
    })),
    subagents: [],
    subagent_outcomes: [],
  });

  expect(needsTranscriptHydration(store.get().sessions.s1.subagents.j1)).toBe(true);
  responder = () => page([taskItem, assistantItem('m1', 'Terminado')], {
    status: 'completed', finished_at: '2026-09-04T20:17:09Z',
  });
  await hydrateSubagentTranscript('s1', 'j1');

  const session = store.get().sessions.s1;
  expect(session.subagents.j1.status).toBe('completed');
  expect(liveTrayAgents(session)).toHaveLength(0);
});

test('a pruned subagent (404) leaves the live transcript untouched', async () => {
  seed({ messages: [{ role: 'assistant', _msg_id: 'm1', content: [{ type: 'text', text: 'vivo' }] }] });
  responder = () => new Response('not found', { status: 404 });

  await hydrateSubagentTranscript('s1', 'j1');

  const entry = store.get().sessions.s1.subagents.j1;
  expect(entry.messages).toHaveLength(1);
  expect(entry.transcriptHydrating).toBe(false);
  expect(entry.transcriptHydrated).toBeUndefined();
});

test('a response is not applied when the view was closed while it was in flight', async () => {
  seed({ messages: [] });
  responder = () => page([taskItem]);

  await hydrateSubagentTranscript('s1', 'j1', () => false);

  expect(store.get().sessions.s1.subagents.j1.messages).toEqual([]);
});

test('paging walks back until the parent task is reached', async () => {
  seed({ messages: [] });
  responder = (path) => (path.includes('cursor=')
    ? page([taskItem, assistantItem('m1', 'primera')])
    : page([assistantItem('m2', 'segunda')], { has_more: true, next_cursor: 'c1' }));

  await hydrateSubagentTranscript('s1', 'j1');

  expect(requests).toHaveLength(2);
  const messages = store.get().sessions.s1.subagents.j1.messages;
  expect(messages.map((m) => m.content[0].text)).toEqual(['Investiga el bug', 'primera', 'segunda']);
});

test('a repeated tool row is kept once per occurrence, not collapsed', () => {
  const fetched = [
    taskItem,
    { id: 'tool:m1:0', role: 'tool', tool: 'bash', action: 'bash', target: 'go test ./...', status: 'ok' },
    { id: 'tool:m2:0', role: 'tool', tool: 'bash', action: 'bash', target: 'go test ./...', status: 'ok' },
  ];
  const live = [
    { _type: 'tool_start', tool_call_id: 'call_2', tool_name: 'bash', args: { command: 'go test ./...' }, status: 'done' },
  ];

  const merged = mergeSubagentTranscript(fetched, live);

  expect(merged).toHaveLength(3);
  expect(merged[0].custom.source).toBe('subagent_parent');
  expect(merged.filter((m) => m._type === 'tool_start')).toHaveLength(2);
});

// ── The window before the child's first turn ────────────────────────────────
//
// A subagent_start reaches the client before the child's goroutine has written
// anything, so an immediate open can hit a 200 whose transcript is still
// empty. Certifying that answer as a complete hydration made the entry refuse
// to try again: the encargo then only appeared on a future reconnect.

test('an empty snapshot of a running child is not certified as hydrated', async () => {
  seed({ messages: [] });
  responder = () => page([]);

  await hydrateSubagentTranscript('s1', 'j1');

  const entry = store.get().sessions.s1.subagents.j1;
  expect(entry.transcriptHydrated).toBe(false);
  // Reopening the view must try again rather than live with an empty history.
  expect(needsTranscriptHydration(entry)).toBe(true);

  responder = () => page([taskItem, assistantItem('m1', 'Empiezo')]);
  await hydrateSubagentTranscript('s1', 'j1');
  const after = store.get().sessions.s1.subagents.j1;
  expect(after.transcriptHydrated).toBe(true);
  expect(after.messages.map((m) => m.content[0].text)).toEqual(['Investiga el bug', 'Empiezo']);
});

test('a terminal child without the sentinel is still certified', async () => {
  // An old transcript predates the provenance tag; no more history is coming,
  // so retrying forever would be a page storm with nothing to gain.
  seed({ messages: [] });
  responder = () => page([assistantItem('m1', 'legacy')], {
    status: 'completed', finished_at: '2026-09-04T20:17:09Z',
  });

  await hydrateSubagentTranscript('s1', 'j1');

  const entry = store.get().sessions.s1.subagents.j1;
  expect(entry.transcriptHydrated).toBe(true);
  expect(needsTranscriptHydration(entry)).toBe(false);
});

test('the announced task and its REST copy render once, in either order', async () => {
  const announced = {
    role: 'user', _msg_id: 'm-task', custom: { source: 'subagent_parent' },
    content: [{ type: 'text', text: 'Investiga el bug' }],
  };

  // Live first: the announcement already proves the transcript is complete,
  // so opening the view does not even hit REST — and cannot duplicate it.
  seed({ messages: [announced] });
  responder = () => page([taskItem, assistantItem('m1', 'Empiezo')]);
  expect(await hydrateSubagentTranscript('s1', 'j1')).toBe(false);
  expect(requests).toEqual([]);
  const liveFirst = store.get().sessions.s1.subagents.j1.messages;
  expect(liveFirst.filter((m) => m.custom?.source === 'subagent_parent')).toHaveLength(1);

  // And when a fetch does overlap the announcement, the merge keeps the row
  // the socket delivered but renders it at the position the server's
  // chronology gives it: the encargo above the answer it caused.
  const merged = mergeSubagentTranscript([taskItem, assistantItem('m1', 'Empiezo')], [announced]);
  expect(merged.filter((m) => m.custom?.source === 'subagent_parent')).toHaveLength(1);
  expect(merged.map((m) => m.content[0].text)).toEqual(['Investiga el bug', 'Empiezo']);

  // REST first, live after: the reducer dedups by msg_id.
  seed({ messages: [] });
  responder = () => page([taskItem]);
  await hydrateSubagentTranscript('s1', 'j1');
  globalThis.requestAnimationFrame = () => 1;
  handleWsSubagentEvent('s1', {
    job_id: 'j1',
    event: { type: 'user_message', data: { msg_id: 'm-task', text: 'Investiga el bug', custom: { source: 'subagent_parent' } } },
  });
  flushSubagentEvents();
  const restFirst = store.get().sessions.s1.subagents.j1.messages;
  expect(restFirst.filter((m) => m.custom?.source === 'subagent_parent')).toHaveLength(1);
});

test('a reload before or after the announcement shows the task exactly once', async () => {
  const initPayload = {
    messages: [], subagent_outcomes: [], bash_jobs: [],
    subagents: [{
      job_id: 'j1', task: 'Investiga el bug', status: 'running', async: true,
      messages: [
        { role: 'user', msg_id: 'm-task', custom: { source: 'subagent_parent' }, content: [{ type: 'text', text: 'Investiga el bug' }] },
      ],
    }],
  };

  // Reload BEFORE the client ever saw the announcement.
  seed({ messages: [] });
  handleWsInit('s1', initPayload);
  expect(
    store.get().sessions.s1.subagents.j1.messages.filter((m) => m.custom?.source === 'subagent_parent'),
  ).toHaveLength(1);

  // Reload AFTER it: the snapshot replaces the entry, still one copy.
  globalThis.requestAnimationFrame = () => 1;
  handleWsSubagentEvent('s1', {
    job_id: 'j1',
    event: { type: 'user_message', data: { msg_id: 'm-task', text: 'Investiga el bug', custom: { source: 'subagent_parent' } } },
  });
  flushSubagentEvents();
  handleWsInit('s1', initPayload);
  const messages = store.get().sessions.s1.subagents.j1.messages;
  expect(messages.filter((m) => m.custom?.source === 'subagent_parent')).toHaveLength(1);
});

test('a full history hydrates over live rows without duplicates or reordering', async () => {
  seed({ messages: [
    { role: 'assistant', _msg_id: 'm2', content: [{ type: 'text', text: 'segunda' }] },
    { role: 'assistant', _msg_id: 'm3', content: [{ type: 'text', text: 'tercera' }] },
  ] });
  responder = () => page([
    taskItem, assistantItem('m1', 'primera'), assistantItem('m2', 'segunda'), assistantItem('m3', 'tercera'),
  ]);

  await hydrateSubagentTranscript('s1', 'j1');

  const messages = store.get().sessions.s1.subagents.j1.messages;
  expect(messages.map((m) => m.content[0].text)).toEqual([
    'Investiga el bug', 'primera', 'segunda', 'tercera',
  ]);
  expect(store.get().sessions.s1.subagents.j1.transcriptHydrated).toBe(true);
});

// The real ordering hazard is not the pure helper but the asynchronous
// hydration path: the view opens before the encargo is announced, so the GET
// is already in flight when the live user_message arrives, and the REST
// response can be AHEAD of the events still queued on the client. Merging as
// if live rows were always a suffix put the encargo below the answer.
test('a GET in flight while the task is announced keeps the task above the answer', async () => {
  seed({ messages: [] });
  globalThis.requestAnimationFrame = () => 1;

  responder = () => {
    // The announcement lands (and is applied) while the request is in flight,
    // so live holds only the encargo…
    handleWsSubagentEvent('s1', {
      job_id: 'j1',
      event: { type: 'user_message', data: { msg_id: 'm-task', text: 'Investiga el bug', custom: { source: 'subagent_parent' } } },
    });
    flushSubagentEvents();
    // …while the server has already moved past the child's first turn.
    return page([taskItem, assistantItem('m1', 'Empiezo')]);
  };

  await hydrateSubagentTranscript('s1', 'j1');

  const hydrated = store.get().sessions.s1.subagents.j1;
  expect(hydrated.messages.map((m) => m.content[0].text)).toEqual([
    'Investiga el bug', 'Empiezo',
  ]);
  expect(hydrated.messages.filter((m) => m.custom?.source === 'subagent_parent')).toHaveLength(1);
  expect(hydrated.transcriptHydrated).toBe(true);
  // The task renders as a parent waypoint, above the child's answer.
  const blocks = subagentView(store.get().sessions.s1, 'j1').blocks;
  expect(blocks[0].kind).toBe('waypoint');
  expect(blocks[0].fromParent).toBe(true);

  // The message_end whose text the snapshot already carried arrives after the
  // merge: dedup by msg_id keeps it single, and the later turn still appends.
  handleWsSubagentEvent('s1', {
    job_id: 'j1', event: { type: 'message_end', data: { msg_id: 'm1', text: 'Empiezo' } },
  });
  handleWsSubagentEvent('s1', {
    job_id: 'j1', event: { type: 'message_end', data: { msg_id: 'm2', text: 'Encontrado' } },
  });
  flushSubagentEvents();

  const after = store.get().sessions.s1.subagents.j1.messages;
  expect(after.map((m) => m.content[0].text)).toEqual([
    'Investiga el bug', 'Empiezo', 'Encontrado',
  ]);
});

// Streaming deltas and repeated tool rows go through the same merge. A tool
// signature has no shared id across the two sources, so the occurrence-aware
// pairing is what keeps two identical bash calls from collapsing into one —
// while the live copy (with its result) is the one rendered.
test('an in-flight hydration preserves streamed deltas and repeated tool rows', async () => {
  seed({ messages: [] });
  globalThis.requestAnimationFrame = () => 1;

  const deliver = (evt) => handleWsSubagentEvent('s1', { job_id: 'j1', event: evt });
  responder = () => {
    deliver({ type: 'user_message', data: { msg_id: 'm-task', text: 'Investiga el bug', custom: { source: 'subagent_parent' } } });
    deliver({ type: 'tool_start', data: { tool_call_id: 'call_1', tool_name: 'bash', args: { command: 'go test ./...' } } });
    deliver({ type: 'tool_end', data: { tool_call_id: 'call_1', result: 'ok', is_error: false } });
    deliver({ type: 'tool_start', data: { tool_call_id: 'call_2', tool_name: 'bash', args: { command: 'go test ./...' } } });
    deliver({ type: 'tool_end', data: { tool_call_id: 'call_2', result: 'ok', is_error: false } });
    deliver({ type: 'text_delta', data: { delta: 'Ya ' } });
    deliver({ type: 'text_delta', data: { delta: 'casi' } });
    flushSubagentEvents();
    return page([
      taskItem,
      { id: 'tool:m1:0', role: 'tool', tool: 'bash', action: 'bash', target: 'go test ./...', status: 'ok' },
      { id: 'tool:m1:1', role: 'tool', tool: 'bash', action: 'bash', target: 'go test ./...', status: 'ok' },
    ]);
  };

  await hydrateSubagentTranscript('s1', 'j1');

  const entry = store.get().sessions.s1.subagents.j1;
  // Both bash rows survive as two rows, from the live copies that carry their
  // results — not collapsed, not duplicated into four.
  const tools = entry.messages.filter((m) => m._type === 'tool_start');
  expect(tools).toHaveLength(2);
  expect(tools.map((m) => m.tool_call_id)).toEqual(['call_1', 'call_2']);
  expect(tools.every((m) => m.status === 'done')).toBe(true);
  expect(entry.messages[0].custom.source).toBe('subagent_parent');
  // A delta mid-flight is not a message yet; the merge must not drop it.
  expect(entry.streamingText).toBe('Ya casi');

  deliver({ type: 'message_end', data: { msg_id: 'm-final', text: 'Ya casi listo' } });
  flushSubagentEvents();
  const after = store.get().sessions.s1.subagents.j1;
  expect(after.streamingText).toBeNull();
  expect(after.messages[after.messages.length - 1].content[0].text).toBe('Ya casi listo');
  expect(after.messages.filter((m) => m._type === 'tool_start')).toHaveLength(2);
});

// End to end over the real hydrate path: the REST projection caps text, so a
// hydrated answer can be a prefix of what the child actually said. The
// message_end that closes that same turn must complete the row, not be
// dropped as a duplicate — the rest of the response was lost otherwise.
test('a truncated hydrated answer is completed by its later message_end', async () => {
  const full = 'Empiezo por el ws-handler y sigo hasta el reducer';
  seed({ messages: [] });
  globalThis.requestAnimationFrame = () => 1;
  responder = () => page([taskItem, { ...assistantItem('m1', 'Empiezo por el ws'), truncated: true }]);

  await hydrateSubagentTranscript('s1', 'j1');

  const hydrated = store.get().sessions.s1.subagents.j1.messages;
  expect(hydrated.map((m) => m.content[0].text)).toEqual(['Investiga el bug', 'Empiezo por el ws']);

  handleWsSubagentEvent('s1', {
    job_id: 'j1', event: { type: 'message_end', data: { msg_id: 'm1', text: full } },
  });
  flushSubagentEvents();

  const after = store.get().sessions.s1.subagents.j1.messages;
  // One answer row, in its place, holding the complete text.
  expect(after.map((m) => m.content[0].text)).toEqual(['Investiga el bug', full]);
  expect(after.filter((m) => m.role === 'assistant')).toHaveLength(1);
});
