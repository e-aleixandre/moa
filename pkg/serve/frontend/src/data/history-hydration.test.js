import { test, expect, beforeEach } from 'bun:test';
import { store, setState } from './store.js';
import {
  beginHistoryHydration, confirmHistoryHydrationInit, finishHistoryHydration,
  HISTORY_DELTA_HYDRATION_GRACE_MS, HISTORY_HYDRATION_GRACE_MS,
} from './history-hydration.js';
import { handleWsInit } from './ws-handlers.js';
import {
  HISTORY_HYDRATION_TIMEOUT_MS, reconnectAll, syncConnections,
} from './api.js';
import { deleteSession, loadSessions } from './session-actions.js';
import { __resetBootForTests, afterVisibilityChange } from './tile-actions.js';
import {
  HistoryHydrationTail, historyHydrationTailVisible,
} from '../components/HistoryHydrationTail/HistoryHydrationTail.jsx';
import { StreamingSkeleton } from '../components/StreamingSkeleton/StreamingSkeleton.jsx';
import { Spinner } from '../primitives/index.js';

beforeEach(() => {
  setState({ sessions: {}, activeSession: null, isMobile: false });
});

test('history hydration starts on socket open and ends at init', () => {
  setState({ sessions: { s1: { id: 's1', messages: [{ role: 'user' }], subagents: {} } } });

  expect(beginHistoryHydration('s1')).toBe(true);
  expect(store.get().sessions.s1.historyPending).toBe(true);

  handleWsInit('s1', { messages: [], subagents: [] });
  expect(store.get().sessions.s1.historyPending).toBe(false);
});

test('a delta resume keeps an uncertain cached transcript behind its boundary', () => {
  setState({ sessions: { s1: {
    id: 's1', messages: [{ role: 'user', _msg_id: 'durable-base' }], subagents: {},
  } } });

  beginHistoryHydration('s1', { deltaResume: true });

  expect(store.get().sessions.s1).toMatchObject({ historyPending: true, historyHydrated: false });
});

test('a settled session opens without a catching-up tail', () => {
  setState({ sessions: { s1: {
    id: 's1', messages: [{ role: 'user', _msg_id: 'durable-base' }], subagents: {},
  } } });

  beginHistoryHydration('s1');

  expect(store.get().sessions.s1).toMatchObject({
    historyPending: true, historyTailNeeded: false,
  });
});

test('a delta resume gives uncertain cached history extra grace', () => {
  const originalSetTimeout = globalThis.setTimeout;
  const originalClearTimeout = globalThis.clearTimeout;
  const timers = [];
  globalThis.setTimeout = (callback, delay) => {
    const timer = { callback, delay };
    timers.push(timer);
    return timer;
  };
  globalThis.clearTimeout = (timer) => {
    const index = timers.indexOf(timer);
    if (index >= 0) timers.splice(index, 1);
  };
  try {
    setState({ sessions: { s1: {
      id: 's1', unseen: true, messages: [{ role: 'user', _msg_id: 'durable-base' }], subagents: {},
    } } });

    beginHistoryHydration('s1', { deltaResume: true });

    expect(timers[0].delay).toBe(HISTORY_DELTA_HYDRATION_GRACE_MS);
    expect(store.get().sessions.s1.historyTailReady).toBe(false);
    confirmHistoryHydrationInit('s1', { deltaBase: true });
    expect(store.get().sessions.s1).toMatchObject({
      historyTailNeeded: false, historyTailReady: false,
    });
  } finally {
    globalThis.setTimeout = originalSetTimeout;
    globalThis.clearTimeout = originalClearTimeout;
  }
});

test('a full-init fallback releases a required catching-up tail immediately', () => {
  setState({ sessions: { s1: {
    id: 's1', unseen: true, messages: [{ role: 'user', _msg_id: 'durable-base' }], subagents: {},
  } } });

  beginHistoryHydration('s1', { deltaResume: true });
  confirmHistoryHydrationInit('s1');

  expect(store.get().sessions.s1.historyTailReady).toBe(true);
});

test('socket resume token is sent only for a cached durable transcript', () => {
  const originalWebSocket = globalThis.WebSocket;
  const originalLocation = globalThis.location;
  class TestWebSocket {
    constructor(url) { this.url = url; TestWebSocket.instances.push(this); }
    close() { this.onclose?.(); }
  }
  TestWebSocket.instances = [];
  globalThis.WebSocket = TestWebSocket;
  globalThis.location = { protocol: 'http:', host: 'localhost' };
  try {
    setState({ sessions: { s1: { id: 's1', messages: [], subagents: {} } } });
    syncConnections(['s1']);
    expect(TestWebSocket.instances[0].url).not.toContain('since_msg');
    syncConnections([]);

    setState({ sessions: { s1: {
      id: 's1', messages: [{ role: 'user', _msg_id: 'durable-base' }], subagents: {},
    } } });
    syncConnections(['s1']);
    expect(TestWebSocket.instances[1].url).toContain('since_msg=durable-base');
    syncConnections([]);

    setState({ sessions: { s1: {
      id: 's1', messages: [
        { role: 'user', _msg_id: 'durable-base' },
        { _type: 'system', text: '✂ Context compacted' },
      ], subagents: {},
    } } });
    syncConnections(['s1']);
    expect(TestWebSocket.instances[2].url).not.toContain('since_msg');
    syncConnections([]);
  } finally {
    globalThis.WebSocket = originalWebSocket;
    globalThis.location = originalLocation;
  }
});

test('socket close and init timeout settle hydration', () => {
  const originalWebSocket = globalThis.WebSocket;
  const originalLocation = globalThis.location;
  const originalSetTimeout = globalThis.setTimeout;
  const originalClearTimeout = globalThis.clearTimeout;
  const timers = [];
  class TestWebSocket {
    constructor() { TestWebSocket.instances.push(this); }
    close() { this.onclose?.(); }
  }
  TestWebSocket.instances = [];
  globalThis.WebSocket = TestWebSocket;
  globalThis.location = { protocol: 'http:', host: 'localhost' };
  globalThis.setTimeout = (callback, delay) => {
    const timer = { callback, delay };
    timers.push(timer);
    return timer;
  };
  globalThis.clearTimeout = (timer) => {
    const index = timers.indexOf(timer);
    if (index >= 0) timers.splice(index, 1);
  };
  try {
    setState({ sessions: { s1: { id: 's1', messages: [], subagents: {} } } });
    syncConnections(['s1']);
    expect(store.get().sessions.s1.historyPending).toBe(true);

    TestWebSocket.instances[0].close();
    expect(store.get().sessions.s1.historyPending).toBe(false);
    expect(store.get().sessions.s1.historyStale).toBe(true);
    syncConnections([]);

    syncConnections(['s1']);
    expect(store.get().sessions.s1.historyPending).toBe(true);
    const timeout = timers.find((timer) => timer.delay === HISTORY_HYDRATION_TIMEOUT_MS);
    timeout.callback();
    expect(store.get().sessions.s1.historyPending).toBe(false);
    expect(store.get().sessions.s1.historyStale).toBe(true);
    syncConnections([]);
  } finally {
    globalThis.WebSocket = originalWebSocket;
    globalThis.location = originalLocation;
    globalThis.setTimeout = originalSetTimeout;
    globalThis.clearTimeout = originalClearTimeout;
  }
});

test('reconnectAll starts a clean hydration grace window', () => {
  const originalWebSocket = globalThis.WebSocket;
  const originalLocation = globalThis.location;
  const originalSetTimeout = globalThis.setTimeout;
  const originalClearTimeout = globalThis.clearTimeout;
  const timers = [];
  class TestWebSocket {
    constructor() { TestWebSocket.instances.push(this); }
    close() { this.onclose?.(); }
  }
  TestWebSocket.instances = [];
  globalThis.WebSocket = TestWebSocket;
  globalThis.location = { protocol: 'http:', host: 'localhost' };
  globalThis.setTimeout = (callback, delay) => {
    const timer = { callback, delay };
    timers.push(timer);
    return timer;
  };
  globalThis.clearTimeout = (timer) => {
    const index = timers.indexOf(timer);
    if (index >= 0) timers.splice(index, 1);
  };
  try {
    setState({ sessions: { s1: {
      id: 's1', messages: [{ role: 'assistant' }], subagents: {},
      serverInstance: 'instance-a', historyCacheInstance: 'instance-a', historyCacheGen: 7,
    } } });
    syncConnections(['s1']);
    const abandonedTimeout = timers.find(timer => timer.delay === HISTORY_HYDRATION_TIMEOUT_MS);

    // The previous socket saw an up-to-date cache; the replacement must
    // recompute risk after the roster learns of a newer occurrence.
    setState({ sessions: { s1: {
      ...store.get().sessions.s1,
      unseen: true, unseenGen: 8, serverUnseenInstance: 'instance-a',
    } } });
    reconnectAll();

    expect(TestWebSocket.instances).toHaveLength(2);
    expect(timers).not.toContain(abandonedTimeout);
    expect(store.get().sessions.s1).toMatchObject({
      historyPending: true, historyTailNeeded: true, historyTailReady: false,
    });
    const grace = timers.find(timer => timer.delay === HISTORY_HYDRATION_GRACE_MS);
    expect(grace).toBeDefined();
    grace.callback();
    expect(historyHydrationTailVisible(store.get().sessions.s1)).toBe(true);
    syncConnections([]);
  } finally {
    globalThis.WebSocket = originalWebSocket;
    globalThis.location = originalLocation;
    globalThis.setTimeout = originalSetTimeout;
    globalThis.clearTimeout = originalClearTimeout;
  }
});

test('a close after init revokes transcript authority', () => {
  const originalWebSocket = globalThis.WebSocket;
  const originalLocation = globalThis.location;
  class TestWebSocket {
    constructor() { TestWebSocket.instances.push(this); }
    close() { this.onclose?.(); }
  }
  TestWebSocket.instances = [];
  globalThis.WebSocket = TestWebSocket;
  globalThis.location = { protocol: 'http:', host: 'localhost' };
  try {
    setState({ sessions: { s1: { id: 's1', messages: [], subagents: {}, serverInstance: 'instance-a' } } });
    syncConnections(['s1']);
    TestWebSocket.instances[0].onmessage({ data: JSON.stringify({
      type: 'init', data: { messages: [], subagents: [], server_instance: 'instance-a' },
    }) });
    expect(store.get().sessions.s1).toMatchObject({ historyHydrated: true, historyStale: false });

    TestWebSocket.instances[0].close();
    expect(store.get().sessions.s1).toMatchObject({
      historyPending: false, historyHydrated: false, historyStale: true,
    });
    syncConnections([]);
  } finally {
    globalThis.WebSocket = originalWebSocket;
    globalThis.location = originalLocation;
  }
});




test('a poll does not restore stale history after a successful hydration', async () => {
  const originalFetch = globalThis.fetch;
  globalThis.fetch = (path) => {
    if (path === '/api/sessions') {
      return Promise.resolve(new Response(JSON.stringify([{
        id: 's1', title: 'Poll replacement', state: 'idle', cwd: '/x',
      }]), { status: 200 }));
    }
    return Promise.resolve(new Response('', { status: 204 }));
  };
  try {
    setState({
      isMobile: true,
      sessions: {
        s1: {
          id: 's1', messages: [{ role: 'assistant' }], subagents: {},
          historyStale: true, historyHydrated: false,
        },
      },
    });
    beginHistoryHydration('s1');
    handleWsInit('s1', { messages: [], subagents: [] });
    expect(store.get().sessions.s1).toMatchObject({ historyStale: false, historyHydrated: true });

    await loadSessions();

    expect(store.get().sessions.s1).toMatchObject({
      title: 'Poll replacement', historyStale: false, historyHydrated: true,
    });
  } finally {
    globalThis.fetch = originalFetch;
  }
});


test('an ordinary up-to-date session open never renders a hydration tail', () => {
  setState({ sessions: { s1: {
    id: 's1', messages: [{ role: 'assistant' }], subagents: {},
    serverInstance: 'instance-a', historyCacheInstance: 'instance-a', historyCacheGen: 7,
  } } });

  beginHistoryHydration('s1');

  expect(store.get().sessions.s1.historyTailNeeded).toBe(false);
  expect(historyHydrationTailVisible(store.get().sessions.s1)).toBe(false);
  finishHistoryHydration('s1');
});

test('an init landing within the grace delay never flashes a hydration tail', () => {
  const originalSetTimeout = globalThis.setTimeout;
  const originalClearTimeout = globalThis.clearTimeout;
  const timers = [];
  globalThis.setTimeout = (callback, delay) => {
    const timer = { callback, delay };
    timers.push(timer);
    return timer;
  };
  globalThis.clearTimeout = (timer) => {
    const index = timers.indexOf(timer);
    if (index >= 0) timers.splice(index, 1);
  };
  try {
    setState({ sessions: { s1: { id: 's1', messages: [], subagents: {} } } });
    beginHistoryHydration('s1');
    expect(historyHydrationTailVisible(store.get().sessions.s1)).toBe(false);
    expect(timers.some(timer => timer.delay === HISTORY_HYDRATION_GRACE_MS)).toBe(true);

    finishHistoryHydration('s1', { shown: true, shownInstance: 'instance-a' });

    expect(timers.some(timer => timer.delay === HISTORY_HYDRATION_GRACE_MS)).toBe(false);
    expect(historyHydrationTailVisible(store.get().sessions.s1)).toBe(false);
  } finally {
    globalThis.setTimeout = originalSetTimeout;
    globalThis.clearTimeout = originalClearTimeout;
  }
});

test('a transcript behind an unseen occurrence appears after the grace delay', () => {
  const originalSetTimeout = globalThis.setTimeout;
  const originalClearTimeout = globalThis.clearTimeout;
  const timers = [];
  globalThis.setTimeout = (callback, delay) => {
    const timer = { callback, delay };
    timers.push(timer);
    return timer;
  };
  globalThis.clearTimeout = (timer) => {
    const index = timers.indexOf(timer);
    if (index >= 0) timers.splice(index, 1);
  };
  try {
    setState({ sessions: { s1: {
      id: 's1', messages: [{ role: 'assistant' }], subagents: {},
      unseen: true, unseenGen: 8, serverInstance: 'instance-a', serverUnseenInstance: 'instance-a',
      historyCacheInstance: 'instance-a', historyCacheGen: 7,
    } } });
    beginHistoryHydration('s1');
    expect(historyHydrationTailVisible(store.get().sessions.s1)).toBe(false);

    timers.find(timer => timer.delay === HISTORY_HYDRATION_GRACE_MS).callback();

    expect(historyHydrationTailVisible(store.get().sessions.s1)).toBe(true);
    finishHistoryHydration('s1');
  } finally {
    globalThis.setTimeout = originalSetTimeout;
    globalThis.clearTimeout = originalClearTimeout;
  }
});

test('an unseen occurrence with unknown provenance shows the cached-history tail', () => {
  const originalSetTimeout = globalThis.setTimeout;
  const originalClearTimeout = globalThis.clearTimeout;
  const timers = [];
  globalThis.setTimeout = (callback, delay) => {
    const timer = { callback, delay };
    timers.push(timer);
    return timer;
  };
  globalThis.clearTimeout = (timer) => {
    const index = timers.indexOf(timer);
    if (index >= 0) timers.splice(index, 1);
  };
  try {
    setState({ sessions: { s1: {
      id: 's1', messages: [{ role: 'assistant' }], subagents: {},
      unseen: true, unseenGen: 7, serverInstance: 'instance-a', serverUnseenInstance: '',
      historyCacheInstance: 'instance-a', historyCacheGen: 7,
    } } });
    beginHistoryHydration('s1');

    expect(store.get().sessions.s1.historyTailNeeded).toBe(true);
    timers.find(timer => timer.delay === HISTORY_HYDRATION_GRACE_MS).callback();
    expect(historyHydrationTailVisible(store.get().sessions.s1)).toBe(true);
    finishHistoryHydration('s1');
  } finally {
    globalThis.setTimeout = originalSetTimeout;
    globalThis.clearTimeout = originalClearTimeout;
  }
});

test('a session without cached history renders conversation skeleton blocks, not a loading line', () => {
  const view = HistoryHydrationTail({ hasCachedTranscript: false });
  const skeletons = [];
  const spinners = [];
  const walk = (node) => {
    if (!node || typeof node !== 'object') return;
    if (node.type === StreamingSkeleton) skeletons.push(node);
    if (node.type === Spinner) spinners.push(node);
    const children = node.props?.children;
    if (Array.isArray(children)) children.forEach(walk);
    else walk(children);
  };
  walk(view);

  expect(skeletons).toHaveLength(4);
  expect(spinners).toHaveLength(0);
});

test('ordinary live sessions without a hydration boundary never render a tail', () => {
  expect(historyHydrationTailVisible({ historyPending: false })).toBe(false);
  expect(historyHydrationTailVisible({})).toBe(false);
  expect(historyHydrationTailVisible({ historyPending: true })).toBe(false);
  expect(historyHydrationTailVisible({ historyPending: true, historyTailNeeded: true })).toBe(false);
  expect(historyHydrationTailVisible({ historyPending: true, historyTailNeeded: true, historyTailReady: true })).toBe(true);
  expect(historyHydrationTailVisible({ historyStale: true })).toBe(true);
});
