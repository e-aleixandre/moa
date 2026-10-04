// provider-error-roundtrip.test.js — the structured credential error reaches the
// client from every path the server sends it (roster, WS init, state_change),
// survives the merges between them, and decides the action without reading
// the error prose. R18 "late roster/init preserves structured class safely".
import { test, expect, beforeEach, afterEach } from 'bun:test';
import { store, setState, visibleSessionIds } from './store.js';
import { handleWsInit, handleWsStateChange } from './ws-handlers.js';
import { loadSessions } from './session-actions.js';
import { getToasts, removeToast } from './notifications.js';
import { resetProviderStatusForTest } from './providers.js';
import { errorActionFor, normalizeErrorDetail } from './provider-error.js';

const WIRE = {
  provider: 'anthropic', source: 'store', credential_generation: 'g1',
  class: 'key_rejected', action: 'replace_key',
};

let roster = [];
let calls = [];

// A session away from the screen toasts through triggerFailed, which asks the
// document whether the tab is hidden and may vibrate. Both are stubbed per test
// and restored after: bun shares globals across files.
const GLOBALS = ['document', 'navigator'];
const saved = {};

afterEach(() => {
  for (const name of GLOBALS) {
    if (saved[name].had) globalThis[name] = saved[name].value;
    else delete globalThis[name];
  }
});

beforeEach(() => {
  for (const name of GLOBALS) saved[name] = { had: name in globalThis, value: globalThis[name] };
  globalThis.document = { hidden: false, visibilityState: 'visible' };
  globalThis.navigator = {};
  roster = [];
  calls = [];
  globalThis.fetch = (path, opts) => {
    calls.push({ path: String(path), method: opts?.method });
    if (String(path).startsWith('/api/sessions')) return Promise.resolve(new Response(JSON.stringify(roster), { status: 200 }));
    return Promise.resolve(new Response('', { status: 204 }));
  };
  resetProviderStatusForTest();
  setState({ sessions: {}, tileTree: null, activeSession: null, isMobile: false });
  getToasts().forEach(({ id }) => removeToast(id));
});

const info = (over = {}) => ({ id: 's1', title: 'T', state: 'error', error: 'anthropic API key was rejected: replace the key', error_detail: WIRE, cwd: '/w', ...over });

test('the roster carries error_detail onto a hidden session', async () => {
  roster = [info()];
  await loadSessions();
  const s = store.get().sessions.s1;
  expect(s.errorDetail).toEqual({ provider: 'anthropic', source: 'store', generation: 'g1', class: 'key_rejected', action: 'replace_key' });
});

test('an unchanged roster tick keeps the same detail object (no churn)', async () => {
  roster = [info()];
  await loadSessions();
  const first = store.get().sessions.s1;
  await loadSessions();
  expect(store.get().sessions.s1).toBe(first);
});

test('the roster clears the detail when the session left the error', async () => {
  roster = [info()];
  await loadSessions();
  roster = [info({ state: 'idle', error: '', error_detail: undefined })];
  await loadSessions();
  expect(store.get().sessions.s1.errorDetail).toBeNull();
});

test('WS init restores error and detail that no replayed event would bring back', () => {
  setState({ sessions: { s1: { id: 's1', messages: [], subagents: {} } } });
  handleWsInit('s1', { state: 'error', error: 'boom', error_detail: WIRE, messages: [] });
  const s = store.get().sessions.s1;
  expect(s.error).toBe('boom');
  expect(s.errorDetail.class).toBe('key_rejected');
});

test('WS init without an error clears a stale one', () => {
  setState({ sessions: { s1: { id: 's1', messages: [], subagents: {}, state: 'error', error: 'old', errorDetail: normalizeErrorDetail(WIRE) } } });
  handleWsInit('s1', { state: 'idle', messages: [] });
  const s = store.get().sessions.s1;
  expect(s.error).toBeNull();
  expect(s.errorDetail).toBeNull();
});

test('state_change sets the detail, and a later non-error state clears it', () => {
  setState({ sessions: { s1: { id: 's1', messages: [], subagents: {}, state: 'running' } } });
  handleWsStateChange('s1', { state: 'error', error: 'x', error_detail: WIRE });
  expect(store.get().sessions.s1.errorDetail.class).toBe('key_rejected');
  handleWsStateChange('s1', { state: 'running' });
  expect(store.get().sessions.s1.errorDetail).toBeNull();
});

test('a stale roster cannot overwrite the live detail of a visible session', async () => {
  setState({
    sessions: { s1: { id: 's1', messages: [], subagents: {}, state: 'running' } },
    isMobile: true, activeSession: 's1',
  });
  expect(visibleSessionIds(store.get())).toContain('s1');
  handleWsStateChange('s1', { state: 'error', error: 'x', error_detail: WIRE });
  roster = [info({ state: 'running', error: '', error_detail: undefined })];
  await loadSessions();
  expect(store.get().sessions.s1.errorDetail?.class).toBe('key_rejected');
});

test('the detail drops fields it does not know', () => {
  const d = normalizeErrorDetail({ ...WIRE, account_id: 'acct', token: 'sk-x' });
  expect(Object.keys(d).sort()).toEqual(['action', 'class', 'generation', 'provider', 'source']);
  expect(JSON.stringify(d)).not.toContain('sk-x');
});

test('the action is decided by the detail, never by the prose', () => {
  // Prose that LOOKS like a credential failure, without a detail: no action.
  expect(errorActionFor(normalizeErrorDetail(null), true)).toBeNull();
  // Same detail with unrelated prose still yields the action.
  expect(errorActionFor(normalizeErrorDetail(WIRE), true).button).toBe('Replace API key');
});

test('one provider failure toasts once, however many sessions hit it', () => {
  setState({ sessions: {
    a: { id: 'a', messages: [], subagents: {}, state: 'running' },
    b: { id: 'b', messages: [], subagents: {}, state: 'running' },
  } });
  handleWsStateChange('a', { state: 'error', error: 'rejected', error_detail: WIRE });
  handleWsStateChange('b', { state: 'error', error: 'rejected', error_detail: WIRE });
  expect(getToasts().filter((t) => t.type === 'error')).toHaveLength(1);
  // A new credential generation is a new problem.
  handleWsStateChange('a', { state: 'running' });
  handleWsStateChange('a', { state: 'error', error: 'rejected', error_detail: { ...WIRE, credential_generation: 'g2' } });
  expect(getToasts().filter((t) => t.type === 'error')).toHaveLength(2);
});

test('errors without a detail keep toasting as before', () => {
  setState({ sessions: {
    a: { id: 'a', messages: [], subagents: {}, state: 'running' },
    b: { id: 'b', messages: [], subagents: {}, state: 'running' },
  } });
  handleWsStateChange('a', { state: 'error', error: 'boom' });
  handleWsStateChange('b', { state: 'error', error: 'boom' });
  expect(getToasts().filter((t) => t.type === 'error')).toHaveLength(2);
});

test('a credential failure re-reads the provider badge', () => {
  setState({ sessions: { a: { id: 'a', messages: [], subagents: {}, state: 'running' } } });
  handleWsStateChange('a', { state: 'error', error: 'x', error_detail: WIRE });
  expect(calls.some((c) => c.path === '/api/providers/status' && c.method === 'GET')).toBe(true);
});
