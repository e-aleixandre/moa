import { test, expect, beforeEach } from 'bun:test';
import { store, setState } from './store.js';
import { beginHistoryHydration } from './history-hydration.js';
import { handleWsInit } from './ws-handlers.js';
import { HISTORY_HYDRATION_TIMEOUT_MS, acknowledgeVisibleAttention, syncConnections } from './api.js';
import { loadSessions } from './session-actions.js';
import { __resetBootForTests } from './tile-actions.js';
import { historyHydrationTailVisible } from '../components/HistoryHydrationTail/HistoryHydrationTail.jsx';

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
    setState({ sessions: { s1: { id: 's1', messages: [], subagents: {} } } });
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
        messages: [], subagents: [], server_instance: 'instance-a', attention_bound: false,
      },
    }) });

    expect(calls.some(path => String(path).includes('/read?'))).toBe(false);
    expect(store.get().sessions.s1).toMatchObject({ historyHydrated: true, historyStale: false, unseen: true });
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

test('attention remains until init has shown authoritative history, then acknowledges once', () => {
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

    handleWsInit('s1', { messages: [], subagents: [], unseen_gen: 7 });
    expect(store.get().sessions.s1.unseen).toBe(false);
    expect(calls).toEqual(['/api/sessions/s1/read?unseen_gen=7']);

    handleWsInit('s1', { messages: [], subagents: [], unseen_gen: 7 });
    expect(calls).toHaveLength(1);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('a stale roster restoration does not repeat an acknowledgement, but a newer generation does', () => {
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
        historyHydrated: true, historyShownGen: 7, serverInstance: 'instance-a',
      } },
    });

    expect(acknowledgeVisibleAttention('s1', 7, 'instance-a')).toBe(true);
    // A poll which began before POST /read can put this same occurrence back.
    setState({ sessions: { s1: { ...store.get().sessions.s1, unseen: true, unseenGen: 7 } } });
    expect(acknowledgeVisibleAttention('s1', 7, 'instance-a')).toBe(false);

    setState({ sessions: { s1: {
      ...store.get().sessions.s1, unseen: true, unseenGen: 8, historyShownGen: 8,
    } } });
    expect(acknowledgeVisibleAttention('s1', 8, 'instance-a')).toBe(true);
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

test('an unknown-at-open init renders history but withholds its acknowledgement', () => {
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
      serverInstance: 'instance-a', historyHydrated: true, unseen: true, unseenGen: 7,
    });
    expect(calls.filter((path) => path.includes('/read?'))).toEqual([]);
    syncConnections([]);
  } finally {
    globalThis.fetch = originalFetch;
    globalThis.WebSocket = originalWebSocket;
    globalThis.location = originalLocation;
  }
});

test('ordinary live sessions never render a hydration tail', () => {
  expect(historyHydrationTailVisible({ historyPending: false })).toBe(false);
  expect(historyHydrationTailVisible({})).toBe(false);
  expect(historyHydrationTailVisible({ historyPending: true })).toBe(true);
  expect(historyHydrationTailVisible({ historyStale: true })).toBe(true);
});
