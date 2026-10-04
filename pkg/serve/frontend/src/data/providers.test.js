// providers.test.js — the shared provider status (badge) and the controller that
// opens Settings → Providers.
import { test, expect, beforeEach } from 'bun:test';
import {
  attentionLabel, claimProviderToast, consumeProviderSettings, getProviderStatus, loadProviderStatus,
  openProviderSettings, resetProviderStatusForTest, subscribeProviderSettings,
} from './providers.js';
import { api, attachStructuredError } from './api.js';

beforeEach(() => resetProviderStatusForTest());

const STATUS = {
  version: 1, can_admin: false, attention_count: 1,
  providers: [{ id: 'anthropic', source: 'store', kind: 'api_key', credential_generation: 'g', state: 'key_rejected', attention: true, action: 'ask_owner' }],
};

test('the badge reads attention_count from /api/providers/status', async () => {
  const seen = [];
  await loadProviderStatus({ request: (method, path) => { seen.push(`${method} ${path}`); return Promise.resolve(STATUS); } });
  expect(seen).toEqual(['GET /api/providers/status']);
  expect(getProviderStatus()).toMatchObject({ loaded: true, stale: false, canAdmin: false, attentionCount: 1 });
});

test('a failed read keeps the last known count instead of announcing zero', async () => {
  await loadProviderStatus({ request: () => Promise.resolve(STATUS) });
  await loadProviderStatus({ request: () => Promise.reject(new Error('offline')) });
  expect(getProviderStatus()).toMatchObject({ attentionCount: 1, stale: true, loaded: true });
});

test('concurrent reads share one request', async () => {
  let n = 0;
  const request = () => { n++; return new Promise((r) => setTimeout(() => r(STATUS), 5)); };
  await Promise.all([loadProviderStatus({ request }), loadProviderStatus({ request }), loadProviderStatus({ request })]);
  expect(n).toBe(1);
});

test('the label says attention, not sign-in', () => {
  expect(attentionLabel(1)).toBe('1 provider needs attention');
  expect(attentionLabel(2)).toBe('2 providers need attention');
  expect(attentionLabel(0)).toBe('');
});

test('a provider failure is toasted once per credential and class', () => {
  const d = { provider: 'openai', source: 'store', generation: 'g', class: 'reconnect', action: 'reconnect' };
  expect(claimProviderToast(d)).toBe(true);
  expect(claimProviderToast({ ...d })).toBe(false);
  expect(claimProviderToast({ ...d, generation: 'g2' })).toBe(true);
  expect(claimProviderToast(null)).toBe(true);
  expect(claimProviderToast(null)).toBe(true);
});

test('openProviderSettings hands the host IDs only, once', () => {
  const got = [];
  const off = subscribeProviderSettings((r) => { got.push(r); consumeProviderSettings(r.seq); });
  openProviderSettings('xai', 's1');
  expect(got).toHaveLength(1);
  expect(Object.keys(got[0]).sort()).toEqual(['provider', 'returnSessionId', 'seq']);
  expect(got[0]).toMatchObject({ provider: 'xai', returnSessionId: 's1' });
  off();
  // Consumed: a host mounting later does not reopen Settings by itself.
  const late = [];
  subscribeProviderSettings((r) => late.push(r));
  expect(late).toHaveLength(0);
});

test('api() keeps the plain-text error and adds the structured one beside it', async () => {
  const body = JSON.stringify({ error: 'That doesn\'t look like an API key. Paste the key again.', error_detail: { provider: 'openai', class: 'invalid_key', action: 'replace_key' } });
  const realFetch = globalThis.fetch;
  globalThis.fetch = () => Promise.resolve(new Response(body, { status: 400 }));
  try {
    const err = await api('POST', '/api/providers/openai/api-key', { key: 'x' }).catch((e) => e);
    expect(err.status).toBe(400);
    expect(err.message).toBe(`400: ${body}`);
    expect(err.userMessage).toBe('That doesn\'t look like an API key. Paste the key again.');
    expect(err.detail).toEqual({ provider: 'openai', class: 'invalid_key', action: 'replace_key' });

    globalThis.fetch = () => Promise.resolve(new Response('forbidden', { status: 403 }));
    const plain = await api('GET', '/api/x').catch((e) => e);
    expect(plain.message).toBe('403: forbidden');
    expect(plain.userMessage).toBeUndefined();
    expect(plain.detail).toBeUndefined();
  } finally {
    globalThis.fetch = realFetch;
  }
});

test('structured parsing ignores JSON that is not an error body', () => {
  const e = attachStructuredError(new Error('x'), '{"items":[1]}');
  expect(e.userMessage).toBeUndefined();
  expect(e.detail).toBeUndefined();
  expect(attachStructuredError(new Error('x'), '{broken').detail).toBeUndefined();
});
