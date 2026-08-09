import { test, expect, beforeEach } from 'bun:test';
import { store, setState } from './store.js';

const { api, getVersion, acknowledgeVisibleAttention, acknowledgeVisibleAttentionThrough } = await import('./api.js?timeout-test');

beforeEach(() => {
  setState({ sessions: {}, activeSession: null, isMobile: true });
});

test('getVersion bypasses the HTTP cache', async () => {
  const originalFetch = globalThis.fetch;
  let options;
  globalThis.fetch = (_, nextOptions) => {
    options = nextOptions;
    return Promise.resolve(new Response('{"current":"dev"}'));
  };
  try {
    await getVersion();
    expect(options.cache).toBe('no-store');
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('api aborts a request that exceeds its timeout', async () => {
  const originalFetch = globalThis.fetch;
  let signal;
  globalThis.fetch = (_, options) => new Promise((_, reject) => {
    signal = options.signal;
    signal.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')));
  });
  try {
    await expect(api('GET', '/api/sessions', undefined, { timeoutMs: 5 }))
      .rejects.toThrow('Request timed out after 5ms: GET /api/sessions');
    expect(signal.aborted).toBe(true);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('api leaves requests without a timeout pending and un-aborted', async () => {
  const originalFetch = globalThis.fetch;
  let options;
  let resolveFetch;
  globalThis.fetch = (_, nextOptions) => {
    options = nextOptions;
    return new Promise((resolve) => { resolveFetch = resolve; });
  };
  try {
    const request = api('POST', '/api/sessions', {}, { timeoutMs: 0 });
    await Promise.resolve();
    expect(options.signal).toBeUndefined();
    const result = await Promise.race([
      request.then(() => 'settled', () => 'rejected'),
      new Promise((resolve) => setTimeout(() => resolve('pending'), 10)),
    ]);
    expect(result).toBe('pending');
    resolveFetch(new Response('', { status: 204 }));
    await expect(request).resolves.toBeNull();
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('api clears its timeout after a successful response', async () => {
  const originalFetch = globalThis.fetch;
  const originalSetTimeout = globalThis.setTimeout;
  const originalClearTimeout = globalThis.clearTimeout;
  const timers = new Set();
  globalThis.fetch = () => Promise.resolve(new Response('{"ok":true}'));
  globalThis.setTimeout = (callback, delay) => {
    const timer = { callback, delay };
    timers.add(timer);
    return timer;
  };
  globalThis.clearTimeout = (timer) => timers.delete(timer);
  try {
    await expect(api('GET', '/api/sessions', undefined, { timeoutMs: 5 })).resolves.toEqual({ ok: true });
    expect(timers.size).toBe(0);
  } finally {
    globalThis.fetch = originalFetch;
    globalThis.setTimeout = originalSetTimeout;
    globalThis.clearTimeout = originalClearTimeout;
  }
});

test('repeated acknowledgements converge on one fenced occurrence', async () => {
  const originalFetch = globalThis.fetch;
  const reads = [];
  let resolveRead;
  globalThis.fetch = (path) => {
    reads.push(path);
    return new Promise((resolve) => { resolveRead = resolve; });
  };
  setState({ sessions: { s1: { id: 's1', unseen: true, unseenGen: 7, serverInstance: 'instance-a', subagents: {} } } });
  try {
    const first = acknowledgeVisibleAttention('s1', 7, 'instance-a');
    const second = acknowledgeVisibleAttention('s1', 7, 'instance-a');
    expect(reads).toEqual(['/api/sessions/s1/read?unseen_gen=7&server_instance=instance-a']);
    resolveRead(new Response('', { status: 204 }));
    await expect(first).resolves.toBe(true);
    await expect(second).resolves.toBe(true);
    expect(store.get().sessions.s1).toMatchObject({ unseen: false, lastAckedUnseenGen: 7 });
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('a confirmed newer acknowledgement clears an older local occurrence only', async () => {
  const originalFetch = globalThis.fetch;
  globalThis.fetch = () => Promise.resolve(new Response('', { status: 204 }));
  try {
    setState({ sessions: { s1: { id: 's1', unseen: true, unseenGen: 7, serverInstance: 'instance-a', subagents: {} } } });

    await expect(acknowledgeVisibleAttention('s1', 8, 'instance-a')).resolves.toBe(true);
    expect(store.get().sessions.s1).toMatchObject({ unseen: false, lastAckedUnseenGen: 8 });

    setState({ sessions: { s1: { ...store.get().sessions.s1, unseen: true, unseenGen: 9 } } });
    await expect(acknowledgeVisibleAttention('s1', 8, 'instance-a')).resolves.toBe(true);
    expect(store.get().sessions.s1).toMatchObject({ unseen: true, unseenGen: 9 });

    setState({ sessions: { s1: { ...store.get().sessions.s1, unseen: true, unseenGen: 0 } } });
    await expect(acknowledgeVisibleAttention('s1', 8, 'instance-a')).resolves.toBe(true);
    expect(store.get().sessions.s1).toMatchObject({ unseen: false, unseenGen: 0 });

    setState({ sessions: { s1: { ...store.get().sessions.s1, serverInstance: 'instance-b', unseen: true, unseenGen: 7 } } });
    await expect(acknowledgeVisibleAttention('s1', 8, 'instance-a')).resolves.toBe(false);
    expect(store.get().sessions.s1).toMatchObject({ unseen: true, serverInstance: 'instance-b' });
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('a background tab never acknowledges an occurrence', async () => {
  const originalFetch = globalThis.fetch;
  const originalDocument = globalThis.document;
  let calls = 0;
  globalThis.fetch = () => { calls += 1; return Promise.resolve(new Response('', { status: 204 })); };
  Object.defineProperty(globalThis, 'document', { configurable: true, value: { hidden: true } });
  setState({ sessions: { s1: { id: 's1', unseen: true, unseenGen: 7, serverInstance: 'instance-a', subagents: {} } } });
  try {
    const acknowledgement = acknowledgeVisibleAttention('s1', 7, 'instance-a');
    if (originalDocument === undefined) delete globalThis.document;
    else Object.defineProperty(globalThis, 'document', { configurable: true, value: originalDocument });
    await expect(acknowledgement).resolves.toBe(false);
    expect(calls).toBe(0);
  } finally {
    globalThis.fetch = originalFetch;
    if (originalDocument === undefined) delete globalThis.document;
    else Object.defineProperty(globalThis, 'document', { configurable: true, value: originalDocument });
  }
});

test('a cursor acknowledgement posts its selected init boundary', async () => {
  const originalFetch = globalThis.fetch;
  const reads = [];
  globalThis.fetch = (path) => {
    reads.push(path);
    return Promise.resolve(new Response('', { status: 204 }));
  };
  setState({ sessions: { s1: {
    id: 's1', unseen: true, unseenSeq: 7, attentionNamespace: 'instance-a:1', subagents: {},
  } } });
  try {
    await expect(acknowledgeVisibleAttentionThrough('s1', 42, 'instance-a:1')).resolves.toBe(true);
    expect(reads).toEqual(['/api/sessions/s1/read?through_seq=42&attention_namespace=instance-a%3A1']);
    expect(store.get().sessions.s1).toMatchObject({ ackedThroughSeq: 42, unseen: false });
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('a hidden tab never posts a cursor acknowledgement', async () => {
  const originalFetch = globalThis.fetch;
  const originalDocument = globalThis.document;
  let calls = 0;
  globalThis.fetch = () => { calls += 1; return Promise.resolve(new Response('', { status: 204 })); };
  Object.defineProperty(globalThis, 'document', { configurable: true, value: { hidden: true } });
  setState({ sessions: { s1: { id: 's1', attentionNamespace: 'instance-a:1', subagents: {} } } });
  try {
    await expect(acknowledgeVisibleAttentionThrough('s1', 42, 'instance-a:1')).resolves.toBe(false);
    expect(calls).toBe(0);
  } finally {
    globalThis.fetch = originalFetch;
    if (originalDocument === undefined) delete globalThis.document;
    else Object.defineProperty(globalThis, 'document', { configurable: true, value: originalDocument });
  }
});
