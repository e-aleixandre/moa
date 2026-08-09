import { test, expect } from 'bun:test';
import { store, setState } from './store.js';

const {
  api, getVersion, acknowledgeRenderedPendingAttention, acknowledgeVisibleAttention, StaleServerInstanceError,
} = await import('./api.js?timeout-test');

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
    await expect(api('GET', '/api/sessions', undefined, { timeoutMs: 5 }))
      .resolves.toEqual({ ok: true });
    expect(timers.size).toBe(0);
  } finally {
    globalThis.fetch = originalFetch;
    globalThis.setTimeout = originalSetTimeout;
    globalThis.clearTimeout = originalClearTimeout;
  }
});

test('a stale /read fence reports a typed stale-server-instance outcome', async () => {
  const originalFetch = globalThis.fetch;
  globalThis.fetch = () => Promise.resolve(new Response('stale instance', { status: 409 }));
  setState({
    isMobile: true,
    activeSession: 's1',
    sessions: {
      s1: { id: 's1', unseen: true, unseenGen: 7, serverInstance: 'instance-a', subagents: {} },
    },
  });
  try {
    await expect(acknowledgeRenderedPendingAttention('s1', {
      id: 'ask', unseenGen: 7, serverInstance: 'instance-a',
    })).rejects.toBeInstanceOf(StaleServerInstanceError);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('a rendered live completion posts its fenced receipt before local unseen exists', async () => {
  const originalFetch = globalThis.fetch;
  const reads = [];
  globalThis.fetch = (path) => {
    reads.push(path);
    return Promise.resolve(new Response('', { status: 204 }));
  };
  setState({
    isMobile: true, activeSession: 's1', drawerOpen: false, paletteOpen: false,
    conversationObscuringOverlayCount: 0,
    sessions: { s1: { id: 's1', serverInstance: 'instance-a', unseen: false, subagents: {} } },
  });
  try {
    await expect(acknowledgeVisibleAttention('s1', 7, 'instance-a', { renderedLive: true })).resolves.toBe(true);
    expect(reads).toEqual(['/api/sessions/s1/read?unseen_gen=7&server_instance=instance-a']);
    expect(store.get().sessions.s1).toMatchObject({ unseen: false, lastAckedUnseenGen: 7 });
  } finally {
    globalThis.fetch = originalFetch;
  }
});
