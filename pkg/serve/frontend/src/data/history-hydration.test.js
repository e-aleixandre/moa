import { test, expect, beforeEach } from 'bun:test';
import { store, setState } from './store.js';
import {
  beginHistoryHydration, confirmHistoryHydrationInit, finishHistoryHydration,
  HISTORY_DELTA_HYDRATION_GRACE_MS, HISTORY_HYDRATION_GRACE_MS,
} from './history-hydration.js';
import { handleWsInit } from './ws-handlers.js';
import {
  HISTORY_HYDRATION_TIMEOUT_MS, acknowledgeVisibleAttention, reconnectAll, syncConnections,
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

test('an init without an attention bound retries instead of stranding its dot', async () => {
  const originalWebSocket = globalThis.WebSocket;
  const originalLocation = globalThis.location;
  const originalSetTimeout = globalThis.setTimeout;
  const originalClearTimeout = globalThis.clearTimeout;
  const originalFetch = globalThis.fetch;
  const timers = [];
  const calls = [];
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
  globalThis.fetch = (path) => {
    calls.push(path);
    return Promise.resolve(new Response(''));
  };
  try {
    setState({
      isMobile: true,
      activeSession: 's1',
      sessions: {
        s1: {
          id: 's1', messages: [], subagents: {}, unseen: true, unseenGen: 7,
          serverInstance: 'instance-a', serverUnseenInstance: 'instance-a',
        },
      },
    });
    syncConnections(['s1']);
    TestWebSocket.instances[0].onmessage({ data: JSON.stringify({
      type: 'init', data: {
        messages: [], subagents: [], server_instance: 'instance-a', attention_bound: false, unseen_gen: 7,
      },
    }) });

    expect(calls.some(path => String(path).includes('/read?'))).toBe(false);
    expect(store.get().sessions.s1).toMatchObject({
      historyHydrated: true, historyAckProven: false, historyStale: false, unseen: true,
    });
    const retry = timers.find(timer => timer.delay === 1000);
    expect(retry).toBeDefined();
    retry.callback();
    expect(TestWebSocket.instances).toHaveLength(2);

    TestWebSocket.instances[1].onmessage({ data: JSON.stringify({
      type: 'init', data: {
        messages: [], subagents: [], server_instance: 'instance-a', attention_bound: true, unseen_gen: 7,
      },
    }) });
    await Promise.resolve();
    await Promise.resolve();
    expect(calls.some(path => String(path).includes('/read?unseen_gen=7'))).toBe(true);
    syncConnections([]);
  } finally {
    globalThis.WebSocket = originalWebSocket;
    globalThis.location = originalLocation;
    globalThis.setTimeout = originalSetTimeout;
    globalThis.clearTimeout = originalClearTimeout;
    globalThis.fetch = originalFetch;
  }
});

test('repeated unbounded inits back off exponentially until a bounded recovery', () => {
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
    setState({ sessions: { s1: { id: 's1', messages: [], subagents: {}, serverInstance: 'instance-a' } } });
    syncConnections(['s1']);
    const unbound = () => TestWebSocket.instances.at(-1).onmessage({ data: JSON.stringify({
      type: 'init', data: { messages: [], subagents: [], server_instance: 'instance-a', attention_bound: false },
    }) });
    const takeRetry = (delay) => {
      const retry = timers.find(timer => timer.delay === delay);
      expect(retry).toBeDefined();
      retry.callback();
    };

    unbound();
    takeRetry(1000);
    unbound();
    takeRetry(2000);
    unbound();
    takeRetry(4000);
    expect(TestWebSocket.instances).toHaveLength(4);

    TestWebSocket.instances[3].onmessage({ data: JSON.stringify({
      type: 'init', data: { messages: [], subagents: [], server_instance: 'instance-a', attention_bound: true },
    }) });
    TestWebSocket.instances[3].close();
    expect(timers.find(timer => timer.delay === 1000)).toBeDefined();
    syncConnections([]);
  } finally {
    globalThis.WebSocket = originalWebSocket;
    globalThis.location = originalLocation;
    globalThis.setTimeout = originalSetTimeout;
    globalThis.clearTimeout = originalClearTimeout;
  }
});

test('a roster poll cannot launder a timed-out hydration boundary into an acknowledgement', async () => {
  const originalWebSocket = globalThis.WebSocket;
  const originalLocation = globalThis.location;
  const originalSetTimeout = globalThis.setTimeout;
  const originalClearTimeout = globalThis.clearTimeout;
  const originalFetch = globalThis.fetch;
  const timers = [];
  const calls = [];
  class TestWebSocket {
    constructor() { TestWebSocket.instances.push(this); }
    close() { this.onclose?.(); }
  }
  TestWebSocket.instances = [];
  globalThis.WebSocket = TestWebSocket;
  globalThis.location = { protocol: 'http:', host: 'localhost' };
  globalThis.fetch = (path) => {
    calls.push(path);
    if (path === '/api/sessions') {
      return Promise.resolve(new Response(JSON.stringify([{
        id: 's1', title: 'Changed by poll', state: 'idle', cwd: '/x',
        unseen: true, unseen_gen: 8, server_instance: 'instance-2',
      }]), { status: 200 }));
    }
    return Promise.resolve(new Response('', { status: 204 }));
  };
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
    setState({
      isMobile: true,
      activeSession: 's1',
      sessions: { s1: { id: 's1', messages: [{ role: 'assistant' }], unseen: true, unseenGen: 7, subagents: {} } },
    });
    __resetBootForTests();
    syncConnections(['s1']);

    const timeout = timers.find((timer) => timer.delay === HISTORY_HYDRATION_TIMEOUT_MS);
    timeout.callback();

    // This is the real poll path: its changed title/server occurrence rebuilds
    // the session object before afterVisibilityChange evaluates the read gate.
    await loadSessions();

    expect(calls.filter((path) => path.includes('/read?'))).toEqual([]);
    expect(store.get().sessions.s1).toMatchObject({ unseen: true, unseenGen: 8, historyStale: true });
    expect(store.get().sessions.s1.historyHydrated).toBe(false);
    expect(historyHydrationTailVisible(store.get().sessions.s1)).toBe(true);
    syncConnections([]);
  } finally {
    globalThis.WebSocket = originalWebSocket;
    globalThis.location = originalLocation;
    globalThis.setTimeout = originalSetTimeout;
    globalThis.clearTimeout = originalClearTimeout;
    globalThis.fetch = originalFetch;
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

test('a WebSocket constructor failure leaves an explicit stale boundary and retries', () => {
  const originalWebSocket = globalThis.WebSocket;
  const originalLocation = globalThis.location;
  const originalSetTimeout = globalThis.setTimeout;
  const originalClearTimeout = globalThis.clearTimeout;
  const timers = [];
  globalThis.WebSocket = class {
    constructor() { throw new Error('WebSocket unavailable'); }
  };
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

    expect(store.get().sessions.s1).toMatchObject({ historyPending: false, historyStale: true });
    expect(timers.some((timer) => timer.delay === 1000)).toBe(true);
    expect(timers.some((timer) => timer.delay === HISTORY_HYDRATION_TIMEOUT_MS)).toBe(false);
    syncConnections([]);
  } finally {
    globalThis.WebSocket = originalWebSocket;
    globalThis.location = originalLocation;
    globalThis.setTimeout = originalSetTimeout;
    globalThis.clearTimeout = originalClearTimeout;
  }
});

test('attention remains until init has shown authoritative history, then acknowledges once', async () => {
  const originalFetch = globalThis.fetch;
  const calls = [];
  globalThis.fetch = (path) => {
    calls.push(path);
    return Promise.resolve(new Response('', { status: 204 }));
  };
  try {
    setState({
      isMobile: true,
      activeSession: 's1',
      sessions: { s1: { id: 's1', messages: [{ role: 'assistant' }], unseen: true, unseenGen: 7, subagents: {} } },
    });
    beginHistoryHydration('s1');

    expect(store.get().sessions.s1.unseen).toBe(true);
    expect(calls).toHaveLength(0);

    handleWsInit('s1', { messages: [], subagents: [], unseen_gen: 7 }, { ackProven: true });
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(store.get().sessions.s1.unseen).toBe(false);
    expect(calls).toEqual(['/api/sessions/s1/read?unseen_gen=7']);

    handleWsInit('s1', { messages: [], subagents: [], unseen_gen: 7 }, { ackProven: true });
    expect(calls).toHaveLength(1);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

for (const obscurer of [
  { name: 'mobile drawer leave animation', closed: { drawerOpen: false }, open: { drawerOpen: true } },
  { name: 'command palette', closed: { paletteOpen: false }, open: { paletteOpen: true } },
  { name: 'modal sheet', closed: { conversationObscuringOverlayCount: 0 }, open: { conversationObscuringOverlayCount: 1 } },
  { name: 'subagent detail', scope: 'session', closed: { viewingSubagent: null }, open: { viewingSubagent: 'child' } },
  { name: 'bash detail', scope: 'session', closed: { viewingBashJob: null }, open: { viewingBashJob: 'job' } },
]) {
  test(`an ordinary receipt waits for ${obscurer.name} then retries its exact occurrence`, async () => {
    const originalFetch = globalThis.fetch;
    const reads = [];
    globalThis.fetch = (path) => {
      if (String(path).includes('/read?')) reads.push(path);
      return Promise.resolve(new Response('', { status: 204 }));
    };
    try {
      setState({
        isMobile: true, activeSession: 's1', drawerOpen: false, paletteOpen: false,
        conversationObscuringOverlayCount: 0,
        ...(obscurer.scope ? {} : obscurer.open),
        sessions: { s1: {
          id: 's1', messages: [], subagents: {}, unseen: true, unseenGen: 7,
          serverInstance: 'instance-a', ...(obscurer.scope ? obscurer.open : {}),
        } },
      });
      beginHistoryHydration('s1');
      handleWsInit('s1', {
        messages: [], subagents: [], unseen_gen: 7, server_instance: 'instance-a', attention_bound: true,
      }, { ackProven: true });
      await Promise.resolve();
      expect(reads).toEqual([]);

      setState({
        drawerOpen: false, paletteOpen: false, conversationObscuringOverlayCount: 0,
        sessions: { s1: { ...store.get().sessions.s1, ...obscurer.closed } },
      });
      await Promise.resolve();
      await Promise.resolve();
      expect(reads).toEqual(['/api/sessions/s1/read?unseen_gen=7&server_instance=instance-a']);
      expect(store.get().sessions.s1.unseen).toBe(false);
    } finally {
      globalThis.fetch = originalFetch;
    }
  });
}

test('a stale roster restoration does not repeat an acknowledgement, but a newer generation does', async () => {
  const originalFetch = globalThis.fetch;
  const calls = [];
  globalThis.fetch = (path) => {
    calls.push(path);
    return Promise.resolve(new Response('', { status: 204 }));
  };
  try {
    setState({
      isMobile: true,
      activeSession: 's1',
      sessions: { s1: {
        id: 's1', messages: [], subagents: {}, unseen: true, unseenGen: 7,
        historyHydrated: true, historyAckProven: true, historyShownGen: 7, serverInstance: 'instance-a',
      } },
    });

    expect(await acknowledgeVisibleAttention('s1', 7, 'instance-a')).toBe(true);
    // A poll which began before POST /read can put this same occurrence back.
    setState({ sessions: { s1: { ...store.get().sessions.s1, unseen: true, unseenGen: 7 } } });
    expect(await acknowledgeVisibleAttention('s1', 7, 'instance-a')).toBe(true);

    setState({ sessions: { s1: {
      ...store.get().sessions.s1, unseen: true, unseenGen: 8, historyShownGen: 8,
    } } });
    expect(await acknowledgeVisibleAttention('s1', 8, 'instance-a')).toBe(true);
    expect(calls).toEqual([
      '/api/sessions/s1/read?unseen_gen=7&server_instance=instance-a',
      '/api/sessions/s1/read?unseen_gen=8&server_instance=instance-a',
    ]);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('a newer roster generation arriving before init is not acknowledged as rendered', async () => {
  const originalFetch = globalThis.fetch;
  const originalWebSocket = globalThis.WebSocket;
  const originalLocation = globalThis.location;
  const calls = [];
  class TestWebSocket {
    constructor() { TestWebSocket.instances.push(this); }
    close() { this.onclose?.(); }
  }
  TestWebSocket.instances = [];
  globalThis.WebSocket = TestWebSocket;
  globalThis.location = { protocol: 'http:', host: 'localhost' };
  globalThis.fetch = (path) => {
    calls.push(path);
    if (path === '/api/sessions') {
      return Promise.resolve(new Response(JSON.stringify([{
        id: 's1', title: 'S1', state: 'idle', cwd: '/x',
        unseen: true, unseen_gen: 8, server_instance: 'instance-a',
      }]), { status: 200 }));
    }
    return Promise.resolve(new Response('', { status: 204 }));
  };
  try {
    setState({
      isMobile: true,
      activeSession: 's1',
      sessions: { s1: {
        id: 's1', state: 'idle', messages: [{ role: 'assistant' }], subagents: {},
        unseen: true, unseenGen: 7, serverInstance: 'instance-a',
      } },
    });
    __resetBootForTests();
    beginHistoryHydration('s1');

    await loadSessions(); // The roster's generation 8 wins the local state first.
    handleWsInit('s1', {
      messages: [], subagents: [], server_instance: 'instance-a', unseen_gen: 7,
    });

    expect(store.get().sessions.s1).toMatchObject({ unseen: true, unseenGen: 8, historyShownGen: 7 });
    expect(calls.filter((path) => path.includes('/read?'))).toEqual([]);
    syncConnections([]);
  } finally {
    globalThis.fetch = originalFetch;
    globalThis.WebSocket = originalWebSocket;
    globalThis.location = originalLocation;
  }
});

test('an init from the pre-restart socket cannot acknowledge the new server generation', async () => {
  const originalFetch = globalThis.fetch;
  const originalWebSocket = globalThis.WebSocket;
  const originalLocation = globalThis.location;
  const calls = [];
  class TestWebSocket {
    constructor() { TestWebSocket.instances.push(this); }
    close() { this.onclose?.(); }
  }
  TestWebSocket.instances = [];
  globalThis.WebSocket = TestWebSocket;
  globalThis.location = { protocol: 'http:', host: 'localhost' };
  globalThis.fetch = (path) => {
    calls.push(path);
    if (path === '/api/sessions') {
      return Promise.resolve(new Response(JSON.stringify([{
        id: 's1', title: 'S1', state: 'idle', cwd: '/x',
        unseen: true, unseen_gen: 1, server_instance: 'instance-b',
      }]), { status: 200 }));
    }
    return Promise.resolve(new Response('', { status: 204 }));
  };
  try {
    setState({
      isMobile: true,
      activeSession: 's1',
      sessions: { s1: {
        id: 's1', state: 'idle', messages: [{ role: 'assistant' }], subagents: {},
        unseen: true, unseenGen: 7, serverInstance: 'instance-a',
      } },
    });
    __resetBootForTests();
    syncConnections(['s1']);
    const oldSocket = TestWebSocket.instances[0];

    await loadSessions(); // Revokes authority and replaces the instance-a socket.
    expect(TestWebSocket.instances).toHaveLength(2);
    oldSocket.onmessage({ data: JSON.stringify({
      type: 'init', data: { messages: [], subagents: [], server_instance: 'instance-a', unseen_gen: 1 },
    }) });
    expect(calls.filter((path) => path.includes('/read?'))).toEqual([]);

    TestWebSocket.instances[1].onmessage({ data: JSON.stringify({
      type: 'init', data: { messages: [], subagents: [], server_instance: 'instance-b', unseen_gen: 1 },
    }) });
    expect(calls.filter((path) => path.includes('/read?'))).toEqual(['/api/sessions/s1/read?unseen_gen=1&server_instance=instance-b']);
    syncConnections([]);
  } finally {
    globalThis.fetch = originalFetch;
    globalThis.WebSocket = originalWebSocket;
    globalThis.location = originalLocation;
  }
});

test('an unknown-at-open socket cannot acknowledge or overwrite a roster-learned replacement', async () => {
  const originalFetch = globalThis.fetch;
  const originalWebSocket = globalThis.WebSocket;
  const originalLocation = globalThis.location;
  const calls = [];
  class TestWebSocket {
    constructor() { TestWebSocket.instances.push(this); }
    close() { this.onclose?.(); }
  }
  TestWebSocket.instances = [];
  globalThis.WebSocket = TestWebSocket;
  globalThis.location = { protocol: 'http:', host: 'localhost' };
  globalThis.fetch = (path) => {
    calls.push(path);
    if (path === '/api/sessions') {
      return Promise.resolve(new Response(JSON.stringify([{
        id: 's1', title: 'S1', state: 'idle', cwd: '/x',
        unseen: true, unseen_gen: 1, server_instance: 'instance-b',
      }]), { status: 200 }));
    }
    return Promise.resolve(new Response('', { status: 204 }));
  };
  try {
    setState({
      isMobile: true,
      activeSession: 's1',
      sessions: { s1: {
        id: 's1', state: 'idle', messages: [{ role: 'assistant' }], subagents: {},
      } },
    });
    __resetBootForTests();
    syncConnections(['s1']);
    const oldSocket = TestWebSocket.instances[0];

    await loadSessions(); // The socket was opened before the client knew A or B.
    oldSocket.onmessage({ data: JSON.stringify({
      type: 'init', data: { messages: [], subagents: [], server_instance: 'instance-a', unseen_gen: 7 },
    }) });

    expect(store.get().sessions.s1).toMatchObject({
      serverInstance: 'instance-b', unseen: true, unseenGen: 1,
    });
    expect(calls.filter((path) => path.includes('/read?'))).toEqual([]);
    syncConnections([]);
  } finally {
    globalThis.fetch = originalFetch;
    globalThis.WebSocket = originalWebSocket;
    globalThis.location = originalLocation;
  }
});

test('an unknown-at-open init cannot be acknowledged by a deferred visibility pass and converges on a proven init', async () => {
  const originalFetch = globalThis.fetch;
  const originalWebSocket = globalThis.WebSocket;
  const originalLocation = globalThis.location;
  const originalSetTimeout = globalThis.setTimeout;
  const originalClearTimeout = globalThis.clearTimeout;
  const calls = [];
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
  globalThis.fetch = (path) => {
    calls.push(path);
    return Promise.resolve(new Response('', { status: 204 }));
  };
  try {
    setState({
      isMobile: true,
      activeSession: 's1',
      sessions: { s1: { id: 's1', state: 'idle', messages: [], subagents: {}, unseen: true, unseenGen: 7 } },
    });
    syncConnections(['s1']);
    TestWebSocket.instances[0].onmessage({ data: JSON.stringify({
      type: 'init', data: { messages: [], subagents: [], server_instance: 'instance-a', unseen_gen: 7 },
    }) });

    expect(store.get().sessions.s1).toMatchObject({
      serverInstance: 'instance-a', historyHydrated: true, historyAckProven: false, unseen: true, unseenGen: 7,
    });
    expect(calls.filter((path) => path.includes('/read?'))).toEqual([]);

    // The common deferred path (triggered after a roster poll) must obey the
    // same proof fence as the immediate init acknowledgement.
    afterVisibilityChange();
    expect(calls.filter((path) => path.includes('/read?'))).toEqual([]);

    // The unproven socket was closed after rendering. Its retry opens against
    // the now-known instance, and that proven bounded init clears the dot.
    const retry = timers.find(timer => timer.delay === 1000);
    expect(retry).toBeDefined();
    retry.callback();
    expect(TestWebSocket.instances).toHaveLength(2);
    TestWebSocket.instances[1].onmessage({ data: JSON.stringify({
      type: 'init', data: {
        messages: [], subagents: [], server_instance: 'instance-a', attention_bound: true, unseen_gen: 7,
      },
    }) });
    await Promise.resolve();
    expect(calls.filter((path) => path.includes('/read?'))).toEqual([
      '/api/sessions/s1/read?unseen_gen=7&server_instance=instance-a',
    ]);
    syncConnections([]);
  } finally {
    globalThis.fetch = originalFetch;
    globalThis.WebSocket = originalWebSocket;
    globalThis.location = originalLocation;
    globalThis.setTimeout = originalSetTimeout;
    globalThis.clearTimeout = originalClearTimeout;
  }
});

test('deleting a session during the hydration grace clears its tail timer', async () => {
  const originalFetch = globalThis.fetch;
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
  globalThis.fetch = () => Promise.resolve(new Response('', { status: 204 }));
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
    setState({
      isMobile: true,
      activeSession: 's1',
      sessions: { s1: { id: 's1', state: 'idle', messages: [], subagents: {}, unseen: true, unseenGen: 7 } },
    });
    syncConnections(['s1']);
    const grace = timers.find(timer => timer.delay === HISTORY_HYDRATION_GRACE_MS);
    expect(grace).toBeDefined();

    await deleteSession('s1');

    expect(store.get().sessions.s1).toBeUndefined();
    expect(timers).not.toContain(grace);
  } finally {
    syncConnections([]);
    globalThis.fetch = originalFetch;
    globalThis.WebSocket = originalWebSocket;
    globalThis.location = originalLocation;
    globalThis.setTimeout = originalSetTimeout;
    globalThis.clearTimeout = originalClearTimeout;
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
