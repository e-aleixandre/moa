import { test, expect } from 'bun:test';

const { api } = await import('./api.js?timeout-test');

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
