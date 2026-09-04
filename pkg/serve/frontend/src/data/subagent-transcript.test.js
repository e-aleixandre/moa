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
const { handleWsInit } = await import('./ws-handlers.js');
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
