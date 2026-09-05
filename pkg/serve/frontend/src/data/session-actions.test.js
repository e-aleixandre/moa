// session-actions.test.js — run with `bun test`
//
// Verifies the session poll (loadSessions) preserves WS/live-only fields that
// the /api/sessions response doesn't carry — in particular the OpenAI usage
// percents, which have no poller to restore them (regression: they flickered
// away every poll tick).
import { test, expect, beforeEach } from 'bun:test';

let apiResponse = [];

const { store, setState } = await import('./store.js');
const { syncConnections } = await import('./api.js');
const { createSession, deleteSession, loadSessions, loadUsage, openPersistedSubagent, openBashJob, sendMessage, startPolling, stopPolling } = await import('./session-actions.js');
const { getToasts, removeToast } = await import('./notifications.js');
const { adoptAttentionNamespace, handleWsRunTokens, handleWsStateChange } = await import('./ws-handlers.js');

beforeEach(() => {
  apiResponse = [];
  globalThis.fetch = () => Promise.resolve(new Response(JSON.stringify(apiResponse), { status: 200 }));
  setState({ sessions: {}, tileTree: null, activeSession: null });
  getToasts().forEach(({ id }) => removeToast(id));
});

test('createSession publishes and selects the POST response without a roster GET', async () => {
  const calls = [];
  globalThis.fetch = (path, opts) => {
    calls.push({ path, opts });
    return Promise.resolve(new Response(JSON.stringify({
      id: 'created', title: 'New session', state: 'idle', provider: 'openai', cwd: '/work',
      thinking: 'low', permission_mode: 'ask', context_percent: 12,
    }), { status: 200 }));
  };
  setState({ isMobile: true });

  await createSession({ cwd: '/work' });

  expect(calls).toHaveLength(1);
  expect(calls[0].path).toBe('/api/sessions');
  expect(calls[0].opts.method).toBe('POST');
  expect(store.get().activeSession).toBe('created');
  expect(store.get().sessions.created).toMatchObject({
    id: 'created', title: 'New session', thinking: 'low', permissionMode: 'ask', contextPercent: 12,
  });
  setState({ isMobile: false });
});

test('an in-flight roster from before create cannot erase the created session', async () => {
  let resolveRoster;
  globalThis.fetch = (path, opts) => {
    if (opts.method === 'GET') return new Promise(resolve => { resolveRoster = resolve; });
    return Promise.resolve(new Response(JSON.stringify({
      id: 'created', title: 'New session', state: 'idle', provider: 'openai', cwd: '/work',
    }), { status: 200 }));
  };
  setState({ isMobile: true });

  const roster = loadSessions();
  await Promise.resolve();
  await createSession({ cwd: '/work' });
  resolveRoster(new Response(JSON.stringify([]), { status: 200 }));
  await roster;

  expect(store.get().sessions.created).toMatchObject({ id: 'created', title: 'New session' });
  expect(store.get().activeSession).toBe('created');
  setState({ isMobile: false });
});

test('a failed create does not add a roster entry', async () => {
  globalThis.fetch = () => Promise.resolve(new Response('nope', { status: 500 }));

  await expect(createSession({ cwd: '/work' })).rejects.toThrow('500');

  expect(store.get().sessions).toEqual({});
});

test('a failed delete surfaces an error toast', async () => {
  globalThis.fetch = () => Promise.resolve(new Response('nope', { status: 500 }));

  await expect(deleteSession('s1')).rejects.toThrow('500');

  expect(getToasts()).toEqual([expect.objectContaining({
    title: 'Could not delete session',
    type: 'error',
  })]);
});

test('a later roster reconciles a created session without duplicates', async () => {
  let roster = [];
  globalThis.fetch = (path, opts) => {
    if (opts.method === 'POST') return Promise.resolve(new Response(JSON.stringify({
      id: 'created', title: 'New session', state: 'idle', provider: 'openai', cwd: '/work',
    }), { status: 200 }));
    return Promise.resolve(new Response(JSON.stringify(roster), { status: 200 }));
  };
  setState({ isMobile: true });

  await createSession({ cwd: '/work' });
  store.get().sessions.created.streamingText = 'local WS text';
  roster = [{
    id: 'created', title: 'Server title', state: 'idle', provider: 'openai', cwd: '/work',
    updated: '2026-08-22T17:00:00Z',
  }];
  await loadSessions();

  expect(Object.keys(store.get().sessions)).toEqual(['created']);
  expect(store.get().sessions.created).toMatchObject({
    title: 'Server title', streamingText: 'local WS text', updated: Date.parse('2026-08-22T17:00:00Z'),
  });
  setState({ isMobile: false });
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

test('loadSessions restores an unread result from the serve snapshot', async () => {
  apiResponse = [{ id: 's3', title: 'S3', state: 'idle', provider: 'openai', cwd: '/z', unseen: true }];

  await loadSessions();

  expect(store.get().sessions.s3.unseen).toBe(true);
});


test('a roster poll never sets a read candidate or revives its acknowledged cursor occurrence', async () => {
  setState({ sessions: { s3: {
    id: 's3', state: 'idle', provider: 'openai', cwd: '/z', subagents: {},
    attentionNamespace: 'server-a:2', unseen: false, unseenSeq: 8,
    ackedThroughSeq: 8, readCandidateSeq: 5,
  } } });
  apiResponse = [{
    id: 's3', title: 'S3', state: 'idle', provider: 'openai', cwd: '/z',
    server_instance: 'server-a', attention_namespace: 'server-a:2', unseen: true, unseen_seq: 8,
  }];
  await loadSessions();
  expect(store.get().sessions.s3).toMatchObject({ unseen: false, unseenSeq: 8, ackedThroughSeq: 8, readCandidateSeq: 5 });
});

test('loadSessions ignores a stale namespace and atomically resets for a newer incarnation', async () => {
  setState({ sessions: { s3: {
    id: 's3', state: 'idle', provider: 'openai', cwd: '/z', subagents: {},
    attentionNamespace: 'server-a:2', unseen: true, unseenSeq: 9,
    ackedThroughSeq: 5, readCandidateSeq: 5,
  } } });
  apiResponse = [{
    id: 's3', title: 'S3', state: 'idle', provider: 'openai', cwd: '/z',
    attention_namespace: 'server-a:1', unseen: false, unseen_seq: 0,
  }];
  await loadSessions();
  expect(store.get().sessions.s3).toMatchObject({
    attentionNamespace: 'server-a:2', unseen: true, unseenSeq: 9, ackedThroughSeq: 5, readCandidateSeq: 5,
  });

  apiResponse = [{
    id: 's3', title: 'S3', state: 'idle', provider: 'openai', cwd: '/z',
    attention_namespace: 'server-a:3', unseen: false, unseen_seq: 0,
  }];
  await loadSessions();
  expect(store.get().sessions.s3).toMatchObject({
    attentionNamespace: 'server-a:3', unseen: false, unseenSeq: 0, ackedThroughSeq: 0, readCandidateSeq: 0,
  });
});

test('loadSessions accepts a namespace from a different server process', async () => {
  setState({ sessions: { s3: {
    id: 's3', state: 'idle', provider: 'openai', cwd: '/z', subagents: {},
    attentionNamespace: 'server-a:99', unseen: true, unseenSeq: 99,
    ackedThroughSeq: 99, readCandidateSeq: 99,
  } } });
  apiResponse = [{
    id: 's3', title: 'S3', state: 'idle', provider: 'openai', cwd: '/z',
    attention_namespace: 'server-b:1', unseen: true, unseen_seq: 1,
  }];
  await loadSessions();
  expect(store.get().sessions.s3).toMatchObject({
    attentionNamespace: 'server-b:1', unseen: true, unseenSeq: 1, ackedThroughSeq: 0, readCandidateSeq: 0,
  });
});

test('an older roster response is fully ignored after a newer process namespace is applied', async () => {
  const originalWebSocket = globalThis.WebSocket;
  const originalLocation = globalThis.location;
  let resolveOlderRoster;
  let rosterCalls = 0;
  class TestWebSocket {
    constructor() { TestWebSocket.instances.push(this); }
    close() { this.closeCount = (this.closeCount || 0) + 1; this.onclose?.(); }
  }
  TestWebSocket.instances = [];
  globalThis.WebSocket = TestWebSocket;
  globalThis.location = { protocol: 'http:', host: 'localhost' };
  globalThis.fetch = (path) => {
    if (String(path).includes('/read?')) return Promise.resolve(new Response('', { status: 204 }));
    rosterCalls += 1;
    if (rosterCalls === 1) return new Promise(resolve => { resolveOlderRoster = resolve; });
    return Promise.resolve(new Response(JSON.stringify([{
      id: 's3', title: 'S3 from B', state: 'idle', provider: 'openai', cwd: '/z',
      server_instance: 'server-b', attention_namespace: 'server-b:1', unseen: true, unseen_seq: 7,
    }]), { status: 200 }));
  };
  setState({
    sessions: { s3: {
      id: 's3', state: 'idle', provider: 'openai', cwd: '/z', subagents: {}, messages: [],
      attentionNamespace: 'server-a:1', serverInstance: 'server-a', unseen: true, unseenSeq: 9,
      ackedThroughSeq: 5, readCandidateSeq: 5,
    } },
    activeSession: 's3', isMobile: true,
  });

  try {
    syncConnections(['s3']);
    const oldSocket = TestWebSocket.instances[0];
    const older = loadSessions();
    await Promise.resolve();
    await loadSessions();

    const socketB = TestWebSocket.instances[1];
    expect(oldSocket.closeCount).toBe(1);
    expect(store.get().sessions.s3).toMatchObject({
      title: 'S3 from B', attentionNamespace: 'server-b:1', unseenSeq: 7,
      ackedThroughSeq: 0, readCandidateSeq: 0,
    });

    resolveOlderRoster(new Response(JSON.stringify([{
      id: 's3', title: 'S3 from A', state: 'idle', provider: 'openai', cwd: '/z',
      server_instance: 'server-a', attention_namespace: 'server-a:1', unseen: false, unseen_seq: 0,
    }]), { status: 200 }));
    await older;

    expect(TestWebSocket.instances).toHaveLength(2);
    expect(socketB.closeCount || 0).toBe(0);
    expect(store.get().sessions.s3).toMatchObject({
      title: 'S3 from B', attentionNamespace: 'server-b:1', unseen: true, unseenSeq: 7,
      ackedThroughSeq: 0, readCandidateSeq: 0,
    });
  } finally {
    syncConnections([]);
    globalThis.WebSocket = originalWebSocket;
    if (originalLocation === undefined) delete globalThis.location;
    else globalThis.location = originalLocation;
    setState({ isMobile: false });
  }
});

test('a restart roster converges on its new socket and resumes acknowledgement', async () => {
  const originalWebSocket = globalThis.WebSocket;
  const originalLocation = globalThis.location;
  const reads = [];
  class TestWebSocket {
    constructor() { TestWebSocket.instances.push(this); }
    close() { this.closeCount = (this.closeCount || 0) + 1; this.onclose?.(); }
  }
  TestWebSocket.instances = [];
  globalThis.WebSocket = TestWebSocket;
  globalThis.location = { protocol: 'http:', host: 'localhost' };
  globalThis.fetch = (path) => {
    if (String(path).includes('/read?')) {
      reads.push(path);
      return Promise.resolve(new Response('', { status: 204 }));
    }
    return Promise.resolve(new Response(JSON.stringify([{
      id: 's3', title: 'S3', state: 'idle', provider: 'openai', cwd: '/z',
      server_instance: 'server-b', attention_namespace: 'server-b:1', unseen: true, unseen_seq: 7,
    }]), { status: 200 }));
  };
  setState({
    sessions: { s3: {
      id: 's3', state: 'idle', provider: 'openai', cwd: '/z', subagents: {}, messages: [],
      attentionNamespace: 'server-a:1', serverInstance: 'server-a', unseen: true, unseenSeq: 9,
      ackedThroughSeq: 5, readCandidateSeq: 5,
    } },
    activeSession: 's3', isMobile: true,
  });

  try {
    syncConnections(['s3']);
    await loadSessions();
    const socketB = TestWebSocket.instances[1];
    socketB.onmessage({ data: JSON.stringify({ type: 'init', data: {
      messages: [], subagents: [], state: 'idle', server_instance: 'server-b',
      attention_namespace: 'server-b:1', last_seq: 7,
    } }) });
    await new Promise(resolve => setTimeout(resolve, 0));

    expect(socketB.closeCount || 0).toBe(0);
    expect(store.get().sessions.s3).toMatchObject({
      attentionNamespace: 'server-b:1', unseen: false, unseenSeq: 7,
      ackedThroughSeq: 7, readCandidateSeq: 7,
    });
    expect(reads).toEqual(['/api/sessions/s3/read?through_seq=7&attention_namespace=server-b%3A1']);
  } finally {
    syncConnections([]);
    globalThis.WebSocket = originalWebSocket;
    if (originalLocation === undefined) delete globalThis.location;
    else globalThis.location = originalLocation;
    setState({ isMobile: false });
  }
});

test('a late frame from a superseded attention incarnation cannot acknowledge the new cursor', async () => {
  const originalWebSocket = globalThis.WebSocket;
  const originalLocation = globalThis.location;
  const reads = [];
  class TestWebSocket {
    constructor() { TestWebSocket.instances.push(this); }
    close() { this.onclose?.(); }
  }
  TestWebSocket.instances = [];
  globalThis.WebSocket = TestWebSocket;
  globalThis.location = { protocol: 'http:', host: 'localhost' };
  globalThis.fetch = (path) => {
    if (String(path).includes('/read?')) reads.push(path);
    return Promise.resolve(new Response(JSON.stringify(apiResponse), { status: 200 }));
  };
  setState({
    sessions: { s3: {
      id: 's3', state: 'idle', provider: 'openai', cwd: '/z', subagents: {}, messages: [],
      attentionNamespace: 'server-a:1', serverInstance: 'server-a',
    } },
    activeSession: 's3', isMobile: true,
  });

  try {
    syncConnections(['s3']);
    const oldSocket = TestWebSocket.instances[0];
    oldSocket.onmessage({ data: JSON.stringify({
      type: 'init', data: {
        messages: [], subagents: [], server_instance: 'server-a', attention_namespace: 'server-a:1', last_seq: 4,
      },
    }) });
    reads.length = 0;

    apiResponse = [{
      id: 's3', title: 'S3', state: 'idle', provider: 'openai', cwd: '/z',
      server_instance: 'server-a', attention_namespace: 'server-a:2', unseen: false, unseen_seq: 0,
    }];
    await loadSessions();
    oldSocket.onmessage({ data: JSON.stringify({
      type: 'permission_request', seq: 5, data: { id: 'old', tool_name: 'Bash', args: {} },
    }) });
    await Promise.resolve();

    expect(store.get().sessions.s3).toMatchObject({
      attentionNamespace: 'server-a:2', unseen: false, unseenSeq: 0, ackedThroughSeq: 0,
      state: 'idle', pendingPerm: null,
    });
    expect(reads).toEqual([]);
  } finally {
    syncConnections([]);
    globalThis.WebSocket = originalWebSocket;
    if (originalLocation === undefined) delete globalThis.location;
    else globalThis.location = originalLocation;
    setState({ isMobile: false });
  }
});

test('a newer socket init adopts its incarnation before the roster catches up', () => {
  const originalWebSocket = globalThis.WebSocket;
  const originalLocation = globalThis.location;
  class TestWebSocket {
    constructor() { TestWebSocket.instances.push(this); }
    close() { this.closeCount = (this.closeCount || 0) + 1; this.onclose?.(); }
  }
  TestWebSocket.instances = [];
  globalThis.WebSocket = TestWebSocket;
  globalThis.location = { protocol: 'http:', host: 'localhost' };
  setState({
    sessions: { s3: {
      id: 's3', state: 'idle', provider: 'openai', cwd: '/z', subagents: {}, messages: [],
      attentionNamespace: 'server-a:1', serverInstance: 'server-a', unseen: true, unseenSeq: 9,
      ackedThroughSeq: 9, readCandidateSeq: 9,
    } },
    activeSession: null, isMobile: true,
  });

  try {
    syncConnections(['s3']);
    const socket = TestWebSocket.instances[0];
    socket.onmessage({ data: JSON.stringify({
      type: 'init', data: {
        messages: [], subagents: [], state: 'idle', server_instance: 'server-a',
        attention_namespace: 'server-a:2', last_seq: 4,
      },
    }) });

    expect(socket.closeCount || 0).toBe(0);
    expect(TestWebSocket.instances).toHaveLength(1);
    expect(store.get().sessions.s3).toMatchObject({
      attentionNamespace: 'server-a:2', unseen: false, unseenSeq: 0,
      ackedThroughSeq: 0, readCandidateSeq: 4,
    });
  } finally {
    syncConnections([]);
    globalThis.WebSocket = originalWebSocket;
    if (originalLocation === undefined) delete globalThis.location;
    else globalThis.location = originalLocation;
    setState({ isMobile: false });
  }
});

test('a delayed init from another process cannot replace the roster namespace', () => {
  const originalWebSocket = globalThis.WebSocket;
  const originalLocation = globalThis.location;
  class TestWebSocket {
    constructor() { TestWebSocket.instances.push(this); }
    close() { this.closeCount = (this.closeCount || 0) + 1; this.onclose?.(); }
  }
  TestWebSocket.instances = [];
  globalThis.WebSocket = TestWebSocket;
  globalThis.location = { protocol: 'http:', host: 'localhost' };
  setState({
    sessions: { s3: {
      id: 's3', state: 'idle', provider: 'openai', cwd: '/z', subagents: {}, messages: [],
      attentionNamespace: 'server-a:2', serverInstance: 'server-a', unseen: true, unseenSeq: 9,
      ackedThroughSeq: 5, readCandidateSeq: 5,
    } },
    activeSession: null, isMobile: true,
  });

  try {
    syncConnections(['s3']);
    const socket = TestWebSocket.instances[0];
    socket.onmessage({ data: JSON.stringify({
      type: 'init', data: {
        messages: [], subagents: [], state: 'idle', server_instance: 'server-b',
        attention_namespace: 'server-b:1', last_seq: 4,
      },
    }) });

    expect(socket.closeCount).toBe(1);
    expect(store.get().sessions.s3).toMatchObject({
      attentionNamespace: 'server-a:2', unseen: true, unseenSeq: 9,
      ackedThroughSeq: 5,
    });
  } finally {
    syncConnections([]);
    globalThis.WebSocket = originalWebSocket;
    if (originalLocation === undefined) delete globalThis.location;
    else globalThis.location = originalLocation;
    setState({ isMobile: false });
  }
});

test('adopting an identical attention namespace does not reset its cursor', () => {
  setState({ sessions: { s3: {
    id: 's3', attentionNamespace: 'server-a:2', unseen: true, unseenSeq: 9,
    ackedThroughSeq: 5, readCandidateSeq: 7,
  } } });

  adoptAttentionNamespace('s3', 'server-a:2');

  expect(store.get().sessions.s3).toMatchObject({
    attentionNamespace: 'server-a:2', unseen: true, unseenSeq: 9,
    ackedThroughSeq: 5, readCandidateSeq: 7,
  });
});

test('an init without a valid, matching attention namespace is neither adopted nor acknowledged', async () => {
  const originalWebSocket = globalThis.WebSocket;
  const originalLocation = globalThis.location;
  const reads = [];
  class TestWebSocket {
    constructor() { TestWebSocket.instances.push(this); }
    close() { this.closeCount = (this.closeCount || 0) + 1; this.onclose?.(); }
  }
  TestWebSocket.instances = [];
  globalThis.WebSocket = TestWebSocket;
  globalThis.location = { protocol: 'http:', host: 'localhost' };
  globalThis.fetch = (path) => {
    if (String(path).includes('/read?')) reads.push(path);
    return Promise.resolve(new Response('', { status: 204 }));
  };
  setState({
    sessions: { s3: {
      id: 's3', state: 'idle', provider: 'openai', cwd: '/z', subagents: {}, messages: [],
      attentionNamespace: 'server-a:1', serverInstance: 'server-a', unseen: true, unseenSeq: 9,
    } },
    activeSession: 's3', isMobile: true,
  });

  try {
    for (const [attention_namespace, server_instance] of [
      [undefined, 'server-a'], ['', 'server-a'], ['server-a:not-a-number', 'server-a'], ['server-b:1', 'server-a'],
    ]) {
      syncConnections([]);
      syncConnections(['s3']);
      const socket = TestWebSocket.instances.at(-1);
      socket.onmessage({ data: JSON.stringify({
        type: 'init', data: { messages: [], subagents: [], attention_namespace, server_instance, last_seq: 42 },
      }) });
      expect(socket.closeCount).toBe(1);
      expect(store.get().sessions.s3.attentionNamespace).toBe('server-a:1');
    }
    await Promise.resolve();
    expect(reads).toEqual([]);
  } finally {
    syncConnections([]);
    globalThis.WebSocket = originalWebSocket;
    if (originalLocation === undefined) delete globalThis.location;
    else globalThis.location = originalLocation;
    setState({ isMobile: false });
  }
});

test('loadSessions adopts the server state for a visible-but-saved session (just resumed)', async () => {
  // Regression: tapping a saved session makes it visible (activeSession) while
  // still 'saved'. resumeSession POSTs /resume (server flips it to idle) then
  // polls. A saved session has NO WS socket, so the poll must own its state —
  // otherwise wsOwns kept 'saved', leaving the dot grey and the stream empty
  // (no WS ever opens) until the app was reopened.
  setState({
    sessions: { s1: { id: 's1', state: 'saved', provider: 'anthropic', subagents: {}, messages: [] } },
    activeSession: 's1',
    isMobile: true,
  });
  apiResponse = [{ id: 's1', title: 'S1', state: 'idle', provider: 'anthropic', cwd: '/x' }];

  // resumeSession's poll makes the (now non-saved) session connectable, so
  // syncConnections opens a socket — stub location so openWs doesn't throw in
  // the jsdom-less test runner. There is no WebSocket either, so openWs falls
  // back to a delayed reconnect; drop that ownership before the stub goes away
  // or its timer outlives the test and dereferences a location that is gone.
  const savedLocation = globalThis.location;
  globalThis.location = { protocol: 'http:', host: 'localhost', search: '' };
  try {
    await loadSessions();
  } finally {
    syncConnections([]);
    if (savedLocation === undefined) delete globalThis.location;
    else globalThis.location = savedLocation;
    setState({ isMobile: false });
  }

  expect(store.get().sessions.s1.state).toBe('idle');
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

test('openPersistedSubagent reloads a terminal child instead of trusting its live cache', async () => {
  setState({ sessions: { s1: { id: 's1', subagents: {
    'job-1': { jobId: 'job-1', status: 'completed', messages: [{ role: 'assistant', content: [{ type: 'text', text: 'stale' }] }] },
  } } } });
  apiResponse = { task: 'complete transcript', status: 'completed', messages: [{ id: 'fresh', role: 'assistant', text: 'full transcript' }] };

  await openPersistedSubagent('s1', 'job-1');

  expect(store.get().sessions.s1.viewingSubagent).toBe('job-1');
  expect(store.get().sessions.s1.subagents['job-1'].messages[0]).toMatchObject({ _msg_id: 'fresh' });
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

test('sendMessage rejection does not overwrite a run that started while the POST was in flight', async () => {
  setState({
    sessions: {
      s1: {
        id: 's1', state: 'idle', subagents: {}, pendingSteers: null, messages: [], runEpoch: 4,
      },
    },
  });
  let resolveSend;
  globalThis.fetch = () => new Promise(resolve => { resolveSend = resolve; });

  const pending = sendMessage('s1', 'follow-up', []);
  handleWsStateChange('s1', { state: 'running' });
  resolveSend(new Response('rejected', { status: 409 }));

  await expect(pending).rejects.toThrow('409');
  const s1 = store.get().sessions.s1;
  expect(s1.state).toBe('running');
  expect(s1.runEpoch).toBe(5);
  expect(s1.messages).toHaveLength(0);
});

test('sendMessage rejection rolls back an invalid attachment while the session stays stopped', async () => {
  setState({
    sessions: {
      s1: {
        id: 's1', state: 'idle', subagents: {}, pendingSteers: null, messages: [],
        runTokensUp: 700, runTokensDown: 120,
      },
    },
  });
  globalThis.fetch = () => Promise.resolve(new Response('invalid attachment', { status: 400 }));

  await expect(sendMessage('s1', 'look', [
    { name: 'bad.png', mime: 'image/png', data: 'AAAA', isImage: true },
  ])).rejects.toThrow('400');

  const s1 = store.get().sessions.s1;
  expect(s1.state).toBe('idle');
  expect(s1.messages).toHaveLength(0);
  expect(s1.streamingText).toBeNull();
  expect(s1.thinkingText).toBeNull();
  expect(s1.runTokensUp).toBe(700);
  expect(s1.runTokensDown).toBe(120);
});

test('sendMessage replaces an accepted optimistic image with its durable descriptor', async () => {
  setState({
    sessions: {
      s1: { id: 's1', state: 'idle', subagents: {}, pendingSteers: null, messages: [] },
    },
  });
  apiResponse = {
    action: 'send',
    attachments: [{ id: 'att_x', mime: 'image/png', size: 123, name: 'a.png', url: '/api/sessions/s1/attachments/att_x' }],
  };

  await sendMessage('s1', 'look at this', [
    { name: 'a.png', mime: 'image/png', data: 'AAAA', isImage: true },
  ]);

  const content = store.get().sessions.s1.messages[0].content;
  expect(content[0]).toEqual({
    type: 'image', attachment_id: 'att_x', attachment_size: 123, mime_type: 'image/png', filename: 'a.png',
  });
  expect(content[0].data).toBeUndefined();
  expect(content[1]).toEqual({ type: 'text', text: 'look at this' });
});

test('sendMessage replaces an optimistic file chip with its durable document descriptor', async () => {
  setState({
    sessions: {
      s1: { id: 's1', state: 'idle', subagents: {}, pendingSteers: null, messages: [] },
    },
  });
  apiResponse = {
    action: 'send',
    attachments: [{ id: 'att_file', kind: 'file', mime: 'text/csv', size: 12, name: 'report.csv', url: '/api/sessions/s1/attachments/att_file' }],
  };

  await sendMessage('s1', 'review this', [
    { name: 'report.csv', mime: 'text/csv', data: 'YSxiCjEsMgo=', isImage: false },
  ]);

  expect(store.get().sessions.s1.messages[0].content[0]).toEqual({
    type: 'document', attachment_id: 'att_file', attachment_size: 12, mime_type: 'text/csv', filename: 'report.csv',
  });
});

test('sendMessage swaps mixed image and document attachments in their original order', async () => {
  setState({
    sessions: {
      s1: { id: 's1', state: 'idle', subagents: {}, pendingSteers: null, messages: [] },
    },
  });
  apiResponse = {
    action: 'send',
    attachments: [
      { id: 'att_image', kind: 'image', mime: 'image/png', size: 123, name: 'screen.png', url: '/api/sessions/s1/attachments/att_image' },
      { id: 'att_file', kind: 'file', mime: 'application/pdf', size: 456, name: 'report.pdf', url: '/api/sessions/s1/attachments/att_file' },
    ],
  };

  await sendMessage('s1', 'review both', [
    { name: 'screen.png', mime: 'image/png', data: 'AAAA', isImage: true },
    { name: 'report.pdf', mime: 'application/pdf', data: 'BBBB', isImage: false },
  ]);

  const content = store.get().sessions.s1.messages[0].content;
  expect(content).toEqual([
    { type: 'image', attachment_id: 'att_image', attachment_size: 123, mime_type: 'image/png', filename: 'screen.png' },
    { type: 'document', attachment_id: 'att_file', attachment_size: 456, mime_type: 'application/pdf', filename: 'report.pdf' },
    { type: 'text', text: 'review both' },
  ]);
  expect(content[0].data).toBeUndefined();
  expect(content[1].data).toBeUndefined();
});

test('sendMessage keeps an optimistic image inline when the response has no attachments', async () => {
  setState({
    sessions: {
      s1: { id: 's1', state: 'idle', subagents: {}, pendingSteers: null, messages: [] },
    },
  });
  apiResponse = { action: 'send' };

  await sendMessage('s1', 'look at this', [
    { name: 'a.png', mime: 'image/png', data: 'AAAA', isImage: true },
  ]);

  expect(store.get().sessions.s1.messages[0].content[0]).toEqual({
    type: 'image', data: 'AAAA', mime_type: 'image/png',
  });
});

test('sendMessage keeps a non-image optimistic echo as a file chip, never inline text', async () => {
  setState({
    sessions: {
      s1: { id: 's1', state: 'idle', subagents: {}, pendingSteers: null, messages: [] },
    },
  });
  apiResponse = { action: 'send' };

  await sendMessage('s1', 'look at this', [
    { name: 'notes.txt', mime: 'text/plain', data: 'aGVsbG8=', isImage: false },
  ]);

  expect(store.get().sessions.s1.messages[0].content[0]).toEqual({
    type: 'document', mime_type: 'text/plain', filename: 'notes.txt',
  });
});

test('sendMessage keeps optimistic images inline when the descriptor count does not match', async () => {
  setState({
    sessions: {
      s1: { id: 's1', state: 'idle', subagents: {}, pendingSteers: null, messages: [] },
    },
  });
  // Two images sent (both classified as image client-side) but the server only
  // stored one durably (e.g. the first failed magic-byte validation and became
  // text/disk). Positional pairing would mis-assign the descriptor, so the swap
  // must not run — the optimistic echo stays inline until a reload reconciles.
  apiResponse = {
    action: 'send',
    attachments: [{ id: 'att_y', mime: 'image/png', size: 9, name: 'b.png', url: '/api/sessions/s1/attachments/att_y' }],
  };

  await sendMessage('s1', 'two', [
    { name: 'a.svg', mime: 'image/svg+xml', data: 'AAAA', isImage: true },
    { name: 'b.png', mime: 'image/png', data: 'BBBB', isImage: true },
  ]);

  const content = store.get().sessions.s1.messages[0].content;
  expect(content[0]).toEqual({ type: 'image', data: 'AAAA', mime_type: 'image/svg+xml' });
  expect(content[1]).toEqual({ type: 'image', data: 'BBBB', mime_type: 'image/png' });
});

test('sendMessage keeps mixed optimistic attachments intact when the descriptor count is partial', async () => {
  setState({
    sessions: {
      s1: { id: 's1', state: 'idle', subagents: {}, pendingSteers: null, messages: [] },
    },
  });
  apiResponse = {
    action: 'send',
    attachments: [{ id: 'att_image', kind: 'image', mime: 'image/png', size: 123, name: 'screen.png', url: '/api/sessions/s1/attachments/att_image' }],
  };

  await sendMessage('s1', 'review both', [
    { name: 'screen.png', mime: 'image/png', data: 'AAAA', isImage: true },
    { name: 'report.pdf', mime: 'application/pdf', data: 'BBBB', isImage: false },
  ]);

  const content = store.get().sessions.s1.messages[0].content;
  expect(content[0]).toEqual({ type: 'image', data: 'AAAA', mime_type: 'image/png' });
  expect(content[1]).toEqual({ type: 'document', mime_type: 'application/pdf', filename: 'report.pdf' });
  expect(content[1].attachment_id).toBeUndefined();
  expect(content[2]).toEqual({ type: 'text', text: 'review both' });
});

test('sendMessage adopts the server-minted msg_id when the server re-minted ours', async () => {
  // The server refuses a msg_id that is malformed or already used in this
  // session's history and mints a fresh one. Without adopting it, the
  // authoritative user_message broadcast would not dedup against our optimistic
  // echo and the message would render twice.
  setState({
    sessions: {
      s1: { id: 's1', state: 'idle', subagents: {}, pendingSteers: null, messages: [] },
    },
  });
  apiResponse = { action: 'send', msg_id: 'server-minted-1' };

  await sendMessage('s1', 'hola', []);

  const messages = store.get().sessions.s1.messages;
  expect(messages.length).toBe(1);
  expect(messages[0]._msg_id).toBe('server-minted-1');
  expect(messages[0]._optimistic).toBe(false);
});

test('sendMessage drops its optimistic echo when the broadcast already landed under the effective id', async () => {
  // The user_message broadcast can beat the /send response. It inserted the
  // message under the effective ID, so our echo is now the duplicate.
  setState({
    sessions: {
      s1: { id: 's1', state: 'idle', subagents: {}, pendingSteers: null, messages: [] },
    },
  });
  apiResponse = { action: 'send', msg_id: 'server-minted-2' };
  const { updateSession } = await import('./store.js');

  const pending = sendMessage('s1', 'hola', []);
  // Simulate the broadcast arriving before the response resolves.
  const cur = store.get().sessions.s1;
  updateSession('s1', {
    messages: [...cur.messages, { role: 'user', _msg_id: 'server-minted-2', content: [{ type: 'text', text: 'hola' }] }],
  });
  await pending;

  const messages = store.get().sessions.s1.messages;
  expect(messages.length).toBe(1);
  expect(messages[0]._msg_id).toBe('server-minted-2');
});

test('sendMessage keeps its own msg_id when the server honored it', async () => {
  setState({
    sessions: {
      s1: { id: 's1', state: 'idle', subagents: {}, pendingSteers: null, messages: [] },
    },
  });
  let sentMsgId = null;
  apiResponse = { action: 'send' };
  await sendMessage('s1', 'hola', []);
  sentMsgId = store.get().sessions.s1.messages[0]._msg_id;
  expect(sentMsgId).toBeTruthy();
  expect(sentMsgId.startsWith('c-')).toBe(true);
  expect(store.get().sessions.s1.messages[0]._optimistic).toBe(false);
});

test('sendMessage turns its optimistic message into a chip when the server queued it', async () => {
  // Local state said idle, but a run (or a queued item) was there by the time
  // the server decided: the response says "steer". The optimistic message is
  // fiction — it must become the confirmed chip, not a phantom message that
  // disappears on reload.
  setState({
    sessions: {
      s1: {
        id: 's1', state: 'idle', subagents: {}, pendingSteers: null, messages: [],
        runTokensUp: 700, runTokensDown: 120, runStartedAtMs: 555,
      },
    },
  });
  apiResponse = { action: 'steer', steer_id: 'srv-steer-1' };

  const action = await sendMessage('s1', 'hola', []);

  const s1 = store.get().sessions.s1;
  expect(action).toBe('steer');
  expect(s1.messages).toHaveLength(0);
  expect(s1.pendingSteers).toHaveLength(1);
  expect(s1.pendingSteers[0]).toMatchObject({ id: 'srv-steer-1', text: 'hola', confirmed: true });
  // The optimistic "fresh run" patch is undone for the start time, but the
  // tally is left at 0/0: a run we didn't know about is in flight and 700/120
  // belongs to an older, finished run.
  expect(s1.runTokensUp).toBe(0);
  expect(s1.runTokensDown).toBe(0);
  expect(s1.runStartedAtMs).toBe(555);
});

test('sendMessage drops the queued chip when the steer was already delivered before the response', async () => {
  // The Steered event can beat the /send response: it moved the message into
  // the transcript, which is the authoritative removal — do not resurrect it.
  setState({
    sessions: {
      s1: { id: 's1', state: 'idle', subagents: {}, pendingSteers: null, messages: [] },
    },
  });
  apiResponse = { action: 'steer', steer_id: 'srv-steer-2' };
  const { updateSession } = await import('./store.js');

  const pending = sendMessage('s1', 'hola', []);
  const cur = store.get().sessions.s1;
  updateSession('s1', {
    messages: [
      ...cur.messages,
      { role: 'user', _msg_id: 'm-1', _steer_id: 'srv-steer-2', content: [{ type: 'text', text: 'hola' }] },
    ],
  });
  await pending;

  const s1 = store.get().sessions.s1;
  expect(s1.pendingSteers).toBeNull();
  expect(s1.messages).toHaveLength(1);
  expect(s1.messages[0]._steer_id).toBe('srv-steer-2');
});

test('sendMessage turns its optimistic chip into a message when the server started a run', async () => {
  // Local state said running, but the run had ended: the response says "send".
  // The chip is fiction — show the message under the effective msg_id instead.
  setState({
    sessions: {
      s1: { id: 's1', state: 'running', subagents: {}, pendingSteers: null, messages: [] },
    },
  });
  apiResponse = { action: 'send', msg_id: 'srv-msg-3' };

  const action = await sendMessage('s1', 'hola', []);

  const s1 = store.get().sessions.s1;
  expect(action).toBe('send');
  expect(s1.pendingSteers).toBeNull();
  expect(s1.messages).toHaveLength(1);
  expect(s1.messages[0]).toMatchObject({ role: 'user', _msg_id: 'srv-msg-3' });
  expect(s1.messages[0].content).toEqual([{ type: 'text', text: 'hola' }]);
});

test('sendMessage drops the chip without doubling the message when the broadcast won the race', async () => {
  setState({
    sessions: {
      s1: { id: 's1', state: 'running', subagents: {}, pendingSteers: null, messages: [] },
    },
  });
  apiResponse = { action: 'send', msg_id: 'srv-msg-4' };
  const { updateSession } = await import('./store.js');

  const pending = sendMessage('s1', 'hola', []);
  updateSession('s1', {
    messages: [{ role: 'user', _msg_id: 'srv-msg-4', content: [{ type: 'text', text: 'hola' }] }],
  });
  await pending;

  const s1 = store.get().sessions.s1;
  expect(s1.pendingSteers).toBeNull();
  expect(s1.messages).toHaveLength(1);
  expect(s1.messages[0]._msg_id).toBe('srv-msg-4');
});

test('sendMessage adopts durable descriptors when the chip prediction lost to a run', async () => {
  // We predicted a chip (local state said running) but the server started a run
  // and stored the attachments. The adopted message must carry the durable
  // descriptors: rebuilding local blocks would render "not available on this
  // device", and the user_message broadcast dedups by msg_id so it never
  // repairs them.
  setState({
    sessions: {
      s1: { id: 's1', state: 'running', subagents: {}, pendingSteers: null, messages: [] },
    },
  });
  apiResponse = {
    action: 'send',
    msg_id: 'srv-msg-5',
    attachments: [
      { id: 'att_image', kind: 'image', mime: 'image/png', size: 123, name: 'screen.png', url: '/api/sessions/s1/attachments/att_image' },
      { id: 'att_file', kind: 'file', mime: 'application/pdf', size: 456, name: 'report.pdf', url: '/api/sessions/s1/attachments/att_file' },
    ],
  };

  await sendMessage('s1', 'review both', [
    { name: 'screen.png', mime: 'image/png', data: 'AAAA', isImage: true },
    { name: 'report.pdf', mime: 'application/pdf', data: 'BBBB', isImage: false },
  ]);

  const s1 = store.get().sessions.s1;
  expect(s1.pendingSteers).toBeNull();
  expect(s1.messages).toHaveLength(1);
  expect(s1.messages[0].content).toEqual([
    { type: 'image', attachment_id: 'att_image', attachment_size: 123, mime_type: 'image/png', filename: 'screen.png' },
    { type: 'document', attachment_id: 'att_file', attachment_size: 456, mime_type: 'application/pdf', filename: 'report.pdf' },
    { type: 'text', text: 'review both' },
  ]);
});

test('sendMessage keeps the adopted chip-to-message blocks local when the descriptor count is partial', async () => {
  setState({
    sessions: {
      s1: { id: 's1', state: 'running', subagents: {}, pendingSteers: null, messages: [] },
    },
  });
  apiResponse = {
    action: 'send',
    msg_id: 'srv-msg-6',
    attachments: [{ id: 'att_image', kind: 'image', mime: 'image/png', size: 123, name: 'screen.png', url: '/api/sessions/s1/attachments/att_image' }],
  };

  await sendMessage('s1', 'review both', [
    { name: 'screen.png', mime: 'image/png', data: 'AAAA', isImage: true },
    { name: 'report.pdf', mime: 'application/pdf', data: 'BBBB', isImage: false },
  ]);

  const content = store.get().sessions.s1.messages[0].content;
  expect(content[0]).toEqual({ type: 'image', data: 'AAAA', mime_type: 'image/png' });
  expect(content[1]).toEqual({ type: 'document', mime_type: 'application/pdf', filename: 'report.pdf' });
});

test('sendMessage does not overwrite a live token tally when the server queued the message', async () => {
  // We predicted a run and zeroed the tally; the server queued instead. Real
  // run_tokens for the run actually in flight landed while the POST was open:
  // they must survive untouched.
  setState({
    sessions: {
      s1: {
        id: 's1', state: 'idle', subagents: {}, pendingSteers: null, messages: [],
        runTokensUp: 700, runTokensDown: 120, runStartedAtMs: 555,
      },
    },
  });
  apiResponse = { action: 'steer', steer_id: 'srv-steer-3' };

  const pending = sendMessage('s1', 'hola', []);
  handleWsRunTokens('s1', { up: 1500, down: 300 });
  await pending;

  const s1 = store.get().sessions.s1;
  expect(s1.runTokensUp).toBe(1500);
  expect(s1.runTokensDown).toBe(300);
  expect(s1.pendingSteers).toHaveLength(1);
});

test('sendMessage does not restore the previous run when a new run started during the POST', async () => {
  // The new run's counters are still 0/0, exactly what our optimistic patch
  // left behind: comparing values cannot tell them apart, so the runStartedAtMs
  // restore must key off "a WS event wrote the per-run fields" instead.
  setState({
    sessions: {
      s1: {
        id: 's1', state: 'idle', subagents: {}, pendingSteers: null, messages: [],
        runTokensUp: 700, runTokensDown: 120, runStartedAtMs: 555,
      },
    },
  });
  apiResponse = { action: 'steer', steer_id: 'srv-steer-4' };

  const pending = sendMessage('s1', 'hola', []);
  handleWsStateChange('s1', { state: 'running' });
  await pending;

  const s1 = store.get().sessions.s1;
  expect(s1.runTokensUp).toBe(0);
  expect(s1.runTokensDown).toBe(0);
  expect(s1.runStartedAtMs).not.toBe(555);
  expect(s1.pendingSteers).toHaveLength(1);
});

test('sendMessage leaves the tally to the run in flight when the response beats its state_change', async () => {
  // Worst case: another client already started a run, our snapshot still says
  // idle, and the POST resolves BEFORE that run's state_change frame arrives —
  // so the epoch guard sees no change. Restoring 700/120 here would show the
  // previous run's totals, and the late state_change wouldn't reset them
  // (it sees us already "running"). The tally must stay 0/0 until run_tokens.
  setState({
    sessions: {
      s1: {
        id: 's1', state: 'idle', subagents: {}, pendingSteers: null, messages: [],
        runTokensUp: 700, runTokensDown: 120, runStartedAtMs: 555,
      },
    },
  });
  apiResponse = { action: 'steer', steer_id: 'srv-steer-5' };

  await sendMessage('s1', 'hola', []);

  let s1 = store.get().sessions.s1;
  expect(s1.runTokensUp).toBe(0);
  expect(s1.runTokensDown).toBe(0);

  // The delayed frames for the run that was already in flight.
  handleWsStateChange('s1', { state: 'running' });
  handleWsRunTokens('s1', { up: 2400, down: 610 });

  s1 = store.get().sessions.s1;
  expect(s1.runTokensUp).toBe(2400);
  expect(s1.runTokensDown).toBe(610);
  expect(s1.pendingSteers).toHaveLength(1);
});

test('openBashJob opens a live background job and closes any subagent view', () => {
  setState({
    sessions: {
      s1: {
        id: 's1', subagents: { 'bash-1': { jobId: 'bash-1', kind: 'bash', status: 'running', messages: [] } },
        viewingSubagent: 'sa-1',
      },
    },
  });

  openBashJob('s1', 'bash-1');

  const s1 = store.get().sessions.s1;
  expect(s1.viewingBashJob).toBe('bash-1');
  // Only one thing is being looked at: opening the job leaves the subagent.
  expect(s1.viewingSubagent).toBeNull();
});

test('openBashJob stores detailReturnView when opening from grid', () => {
  setState({
    sessions: {
      s1: {
        id: 's1',
        subagents: { 'bash-1': { jobId: 'bash-1', kind: 'bash', status: 'running', messages: [] } },
      },
    },
  });

  openBashJob('s1', 'bash-1', { returnView: 'grid' });

  const s1 = store.get().sessions.s1;
  expect(s1.viewingBashJob).toBe('bash-1');
  expect(s1.detailReturnView).toBe('grid');
});

test('openBashJob is a no-op for a job the store never had (no disk fallback)', () => {
  setState({ sessions: { s1: { id: 's1', subagents: {} } } });

  openBashJob('s1', 'bash-gone');

  expect(store.get().sessions.s1.viewingBashJob).toBeUndefined();
});

test('loadSessions preserves the open bash job view across a poll', async () => {
  setState({ sessions: { s1: { id: 's1', state: 'idle', subagents: {}, viewingBashJob: 'bash-1' } } });
  apiResponse = [{ id: 's1', title: 'S1', state: 'idle', cwd: '/x' }];

  await loadSessions();

  expect(store.get().sessions.s1.viewingBashJob).toBe('bash-1');
});

test('setState does not notify when every patched field is already the current value', () => {
  let n = 0;
  const unsub = store.subscribe(() => { n++; });
  setState({ activeSession: null, tileTree: null });
  expect(n).toBe(0);
  setState({ activeSession: 's1' });
  expect(n).toBe(1);
  unsub();
});

test('an unchanged roster poll does not replace session objects or notify', async () => {
  apiResponse = [{ id: 's1', title: 'S1', state: 'idle', provider: 'openai', cwd: '/x' }];
  await loadSessions();
  const sessions = store.get().sessions;
  const s1 = sessions.s1;
  let n = 0;
  const unsub = store.subscribe(() => { n++; });
  await loadSessions();
  unsub();
  expect(n).toBe(0);
  expect(store.get().sessions).toBe(sessions);
  expect(store.get().sessions.s1).toBe(s1);
});

test('loadUsage does not notify when the snapshot is unchanged', async () => {
  const snap = { plans: [{ used: 1, limit: 2 }] };
  globalThis.fetch = () => Promise.resolve(new Response(JSON.stringify(snap), { status: 200 }));
  await loadUsage();
  let n = 0;
  const unsub = store.subscribe(() => { n++; });
  await loadUsage();
  unsub();
  expect(n).toBe(0);
});

test('desktop and mobile roster polls run every 15s with the inbox refresh', () => {
  const intervals = [];
  const orig = globalThis.setInterval;
  globalThis.setInterval = (fn, ms) => { intervals.push(ms); return 1; };
  try {
    setState({ isMobile: false });
    startPolling();
    setState({ isMobile: true });
    startPolling();
    expect(intervals).toEqual([15000, 15000]);
  } finally {
    globalThis.setInterval = orig;
    stopPolling();
    setState({ isMobile: false });
  }
});

test('a roster poll that lost the artifacts owner closes its drawer, and another owner is not switched', async () => {
  const { openArtifactsList, artifactsSlice } = await import('./artifacts.js');
  const { ARTIFACTS_CLOSED } = await import('./artifacts-model.js');
  setState({ artifacts: ARTIFACTS_CLOSED, sessions: {}, tileTree: null, activeSession: null });

  // Two conversations exist; the drawer is opened on the one that will be
  // deleted from another client.
  apiResponse = [
    { id: 'gone', title: 'deleted elsewhere', state: 'idle' },
    { id: 'kept', title: 'still here', state: 'saved' },
  ];
  await loadSessions();
  openArtifactsList('gone');
  await Promise.resolve();
  expect(artifactsSlice(store.get()).ownerSessionId).toBe('gone');

  // The authoritative roster no longer lists it. 'kept' is still there and is
  // NOT adopted as the new owner: the drawer closes instead of switching.
  apiResponse = [{ id: 'kept', title: 'still here', state: 'saved' }];
  await loadSessions();
  expect(artifactsSlice(store.get()).view).toBeNull();
  expect(artifactsSlice(store.get()).ownerSessionId).toBeNull();

  // A saved conversation that stays in the roster keeps its drawer open: it is
  // unloaded, not deleted, and the artifacts API still answers for it.
  openArtifactsList('kept');
  await Promise.resolve();
  await loadSessions();
  expect(artifactsSlice(store.get()).ownerSessionId).toBe('kept');
});

test('openPersistedSubagent restores the title the sidecar saved for the child', async () => {
  setState({ sessions: { s1: { id: 's1', subagents: {} } } });
  apiResponse = {
    task: '\n# Delivery review\n\nVerify the promised work.',
    title: 'Review Promised Delivery Work',
    status: 'completed',
    messages: [{ id: 'm1', role: 'assistant', text: 'done' }],
  };

  await openPersistedSubagent('s1', 'job-1');

  expect(store.get().sessions.s1.subagents['job-1'].title).toBe('Review Promised Delivery Work');
});

test('openPersistedSubagent keeps a known title when an older server omits it', async () => {
  setState({ sessions: { s1: { id: 's1', subagents: {
    'job-1': { jobId: 'job-1', status: 'completed', title: 'Review Promised Delivery Work', messages: [] },
  } } } });
  apiResponse = { task: 'inspect', status: 'completed', messages: [] };

  await openPersistedSubagent('s1', 'job-1');

  expect(store.get().sessions.s1.subagents['job-1'].title).toBe('Review Promised Delivery Work');
});
