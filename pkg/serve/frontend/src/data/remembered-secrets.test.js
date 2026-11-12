// remembered-secrets.test.js — run with `bun test`
//
// The remembered-secrets slice: what it keeps from the wire, what counts as
// remembered, and that Forget wins over a slower status read.
import { test, expect, beforeEach, afterEach } from 'bun:test';
import { store, setState } from './store.js';
import {
  REMEMBERED_INITIAL, _resetRememberedForTest, forgetRemembered, lastDeadline, loadRemembered,
  normalizeRemembered, rememberedAliases, rememberedRuns, rememberedSlice,
} from './remembered-secrets.js';
import { buildAllowBody, holdChoices, normalizeSecretRequest } from './secret-action.js';

const originalFetch = globalThis.fetch;
const inHours = (h) => new Date(Date.now() + h * 3600_000).toISOString();
const entry = (alias, h, kind = 'password') => ({ alias, kind, expiresAt: inHours(h), rememberedAt: inHours(0), uses: 1 });

function respond(body, status = 200) {
  return Promise.resolve(new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }));
}

beforeEach(() => {
  _resetRememberedForTest();
  setState({ rememberedSecrets: REMEMBERED_INITIAL });
});
afterEach(() => { globalThis.fetch = originalFetch; });

test('normalize keeps metadata only', () => {
  const r = normalizeRemembered({
    entries: [{ ...entry('vault-agents', 1), value: 'nope' }],
    uses: [{ at: 'a', kind: 'use', keys: [{ alias: 'vault-agents', kind: 'password' }, { alias: 'vault-agents', kind: 'username' }], sessionId: 's1', command: 'c', secret: 'x' }],
  });
  const json = JSON.stringify(r);
  expect(json).not.toContain('nope');
  expect(json).not.toContain('"secret"');
  expect(r.uses[0].aliases).toEqual(['vault-agents']);
});

test('one row per alias, expired entries gone', () => {
  const slice = normalizeRemembered({ entries: [entry('b', 2), entry('a', 1, 'username'), entry('a', 3), entry('old', -1)] });
  const rows = rememberedAliases(slice);
  expect(rows.map((r) => r.alias)).toEqual(['a', 'b']);
  expect(rows[0].expiresAt).toBe(slice.entries[2].expiresAt);
  expect(lastDeadline(rows)).toBe(slice.entries[2].expiresAt);
});

test('runs exclude the remembering, newest first', () => {
  const runs = rememberedRuns({ uses: [{ kind: 'remember', at: 'a' }, { kind: 'use', at: 'b' }, { kind: 'use', at: 'c' }] });
  expect(runs.map((u) => u.at)).toEqual(['c', 'b']);
});

test('a status read started before Forget cannot bring entries back', async () => {
  let release;
  globalThis.fetch = (url, opts) => {
    if (opts.method === 'GET') return new Promise((r) => { release = () => r(respond({ entries: [entry('a', 1)], uses: [] })); });
    return respond({ entries: [], uses: [] });
  };
  setState({ rememberedSecrets: normalizeRemembered({ entries: [entry('a', 1)] }) });
  const slow = loadRemembered();
  await forgetRemembered();
  expect(rememberedSlice(store.get()).entries).toEqual([]);
  release();
  await slow;
  expect(rememberedSlice(store.get()).entries).toEqual([]);
});

test('Forget one alias calls its DELETE and drops only it at once', async () => {
  const calls = [];
  globalThis.fetch = (url, opts) => { calls.push([opts.method, url]); return respond({ entries: [entry('b', 1)], uses: [] }); };
  setState({ rememberedSecrets: normalizeRemembered({ entries: [entry('a', 1), entry('b', 1)] }) });
  const p = forgetRemembered('a');
  expect(rememberedSlice(store.get()).entries.map((e) => e.alias)).toEqual(['b']);
  await p;
  expect(calls).toEqual([['DELETE', '/api/remembered-secrets/a']]);
});

test('a failed Forget reloads the real state and reports the error', async () => {
  globalThis.fetch = (url, opts) => opts.method === 'DELETE' ? respond({}, 500) : respond({ entries: [entry('a', 1)], uses: [] });
  setState({ rememberedSecrets: normalizeRemembered({ entries: [entry('a', 1)] }) });
  await expect(forgetRemembered()).rejects.toBeDefined();
  expect(rememberedSlice(store.get()).entries.length).toBe(1);
});

test('hold choices only on a request that offers them', () => {
  expect(holdChoices(normalizeSecretRequest({ id: 'a', fields: [] }))).toEqual([]);
  expect(holdChoices(normalizeSecretRequest({ id: 'a', holdMaxHours: 10, fields: [] }))).toEqual([0, 2, 4, 8, 10]);
  expect(holdChoices(normalizeSecretRequest({ id: 'a', holdMaxHours: 99, fields: [] }))).toEqual([0, 2, 4, 8, 10]);
  expect(holdChoices(normalizeSecretRequest({ id: 'a', holdMaxHours: '10', fields: [] }))).toEqual([]);
});

test('allow body carries holdHours only when chosen', () => {
  const fields = [{ index: 0 }];
  expect(buildAllowBody(fields, { 0: 'v' })).toEqual({ action: 'allow', values: [{ index: 0, value: 'v' }] });
  expect(buildAllowBody(fields, { 0: 'v' }, 0)).not.toHaveProperty('holdHours');
  expect(buildAllowBody(fields, { 0: 'v' }, 8).holdHours).toBe(8);
});
