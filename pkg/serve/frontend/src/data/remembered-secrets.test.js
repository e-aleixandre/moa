// remembered-secrets.test.js — run with `bun test`
//
// The remembered-secrets slice: what it keeps from the wire, what counts as
// remembered, and that Forget wins over a slower status read.
import { test, expect, beforeEach, afterEach } from 'bun:test';
import { store, setState } from './store.js';
import {
  REMEMBERED_INITIAL, _resetRememberedForTest, forgetRemembered, isForgetting, lastDeadline, loadRemembered,
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

function deferred() { let resolve; const p = new Promise((r) => { resolve = r; }); return { p, resolve }; }

test('a read started before Forget cannot bring entries back', async () => {
  const get = deferred();
  globalThis.fetch = (url, opts) => opts.method === 'GET' ? get.p : respond({ entries: [], uses: [] });
  setState({ rememberedSecrets: normalizeRemembered({ entries: [entry('a', 1)] }) });
  const slow = loadRemembered();
  await forgetRemembered();
  expect(rememberedSlice(store.get()).entries).toEqual([]);
  get.resolve(respond({ entries: [entry('a', 1)], uses: [] }));
  await slow;
  expect(rememberedSlice(store.get()).entries).toEqual([]);
});

test('a read started during Forget cannot undo it', async () => {
  const del = deferred();
  const get = deferred();
  globalThis.fetch = (url, opts) => opts.method === 'GET' ? get.p : del.p;
  setState({ rememberedSecrets: normalizeRemembered({ entries: [entry('a', 1)] }) });
  const forgetting = forgetRemembered('a');
  const read = loadRemembered();
  // The DELETE answers first; the read (served before it) answers later.
  del.resolve(respond({ entries: [], uses: [] }));
  await forgetting;
  get.resolve(respond({ entries: [entry('a', 1)], uses: [] }));
  await read;
  expect(rememberedSlice(store.get()).entries).toEqual([]);
});

test('Forget keeps the entry, marked pending, until the server confirms', async () => {
  const del = deferred();
  const calls = [];
  globalThis.fetch = (url, opts) => { calls.push([opts.method, url]); return del.p; };
  setState({ rememberedSecrets: normalizeRemembered({ entries: [entry('a', 1), entry('b', 1)] }) });
  const p = forgetRemembered('a');
  expect(rememberedSlice(store.get()).entries.length).toBe(2);
  expect(isForgetting(rememberedSlice(store.get()), 'a')).toBe(true);
  expect(isForgetting(rememberedSlice(store.get()), 'b')).toBe(false);
  await Promise.resolve();
  del.resolve(respond({ entries: [entry('b', 1)], uses: [] }));
  await p;
  expect(calls).toEqual([['DELETE', '/api/remembered-secrets?alias=a']]);
  expect(rememberedSlice(store.get()).entries.map((e) => e.alias)).toEqual(['b']);
  expect(isForgetting(rememberedSlice(store.get()), 'a')).toBe(false);
});

test('dot aliases travel as a query value', async () => {
  const calls = [];
  globalThis.fetch = (url, opts) => { calls.push(url); return respond({ entries: [], uses: [] }); };
  await forgetRemembered('..');
  expect(calls).toEqual(['/api/remembered-secrets?alias=..']);
});

test('a failed Forget, even offline, leaves the entries and Forget available', async () => {
  globalThis.fetch = () => Promise.reject(new TypeError('offline'));
  setState({ rememberedSecrets: normalizeRemembered({ entries: [entry('a', 1)] }) });
  await expect(forgetRemembered()).rejects.toBeDefined();
  const slice = rememberedSlice(store.get());
  expect(slice.entries.length).toBe(1);
  expect(isForgetting(slice, 'a')).toBe(false);
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
