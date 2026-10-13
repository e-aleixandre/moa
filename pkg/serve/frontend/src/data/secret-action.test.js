// secret-action.test.js — run with `bun test`
//
// The pure rules of the secret-for-an-action card and what the session store
// does with its three wire messages (secret_request, secret_resolved,
// init.pending_secret_requests).
import { test, expect, beforeEach, afterEach } from 'bun:test';
import { store, setState } from './store.js';
import { getToasts, removeToast } from './notifications.js';
import { handleWsInit, handleWsSecretRequest, handleWsSecretResolved } from './ws-handlers.js';
import { resolveSecretAction } from './session-actions.js';
import { __resetAttentionArrivalsForTests } from './attention-arrivals.js';
import {
  DENY_BODY, buildAllowBody, browserMapping, fieldAutocomplete, normalizeSecretRequest,
  requesterLabel, secretCopy, secretErrorText, valuesComplete,
} from './secret-action.js';
import { sessionDisplayDotState } from './util/format.js';

const COMMAND = {
  id: 'sec_1', sessionId: 's1', requester: 'main', kind: 'command',
  command: 'ssh -tt me@host security unlock-keychain', cwd: '/w', sshHost: 'host',
  actionFingerprint: 'abc',
  fields: [{ index: 0, label: 'Keychain password', kind: 'password', alias: 'agents-keychain' }],
  requestedAt: '2026-10-13T10:00:00Z',
};
const BROWSER = {
  id: 'sec_2', requester: 'subagent · job7', kind: 'browser', origin: 'https://idmsa.apple.com', submit: true,
  actionFingerprint: 'def',
  fields: [
    { index: 0, label: 'Username', kind: 'username', element: 'e21', realLabel: 'Email or Phone Number', realType: 'text', autocomplete: 'username' },
    { index: 1, label: 'Password', kind: 'password', element: 'e27', realLabel: 'Password', realType: 'password' },
  ],
};

test('a request keeps only the metadata the card reads', () => {
  const r = normalizeSecretRequest({ ...COMMAND, value: 'leak', fields: [{ ...COMMAND.fields[0], value: 'leak' }] });
  expect(JSON.stringify(r)).not.toContain('leak');
  expect(r).toMatchObject({ id: 'sec_1', kind: 'command', sshHost: 'host', cwd: '/w' });
  expect(r.fields[0]).toEqual({
    index: 0, label: 'Keychain password', kind: 'password', alias: 'agents-keychain',
    element: '', realLabel: '', realType: '',
  });
  expect(normalizeSecretRequest({ kind: 'command' })).toBeNull();
});

test('copy follows the kind', () => {
  expect(secretCopy({ kind: 'command' })).toEqual({
    title: 'Password for this command', note: 'Sent only to this command, not to the chat', primary: 'Run',
  });
  expect(secretCopy({ kind: 'browser' })).toEqual({
    title: 'Sign in on this page', note: 'Sent only to this page, not to the chat', primary: 'Fill in',
  });
});

test('the requester is named by the subagent title, never by its job id', () => {
  expect(requesterLabel('main', {})).toBe('');
  expect(requesterLabel('subagent · job7', { job7: { title: 'sonnet' } })).toBe('subagent · sonnet');
  expect(requesterLabel('subagent · job7', {})).toBe('subagent');
  expect(requesterLabel('subagent · job7', {})).not.toContain('job7');
});

test('browser mapping names the real field and its type', () => {
  const r = normalizeSecretRequest(BROWSER);
  expect(browserMapping(r)).toEqual([
    { label: 'Username', text: 'field “Email or Phone Number” (text)' },
    { label: 'Password', text: 'field “Password” (password)' },
  ]);
});

test('field kinds map to autocomplete tokens', () => {
  expect(fieldAutocomplete('password')).toBe('current-password');
  expect(fieldAutocomplete('username')).toBe('username');
  expect(fieldAutocomplete('text')).toBe('off');
});

test('allow needs every field; the body is the contract shape', () => {
  const r = normalizeSecretRequest(BROWSER);
  expect(valuesComplete(r.fields, { 0: 'me' })).toBe(false);
  expect(valuesComplete(r.fields, { 0: 'me', 1: 'pw' })).toBe(true);
  expect(valuesComplete(r.fields, { 1: 'pw' }, new Set([0]))).toBe(true);
  expect(() => buildAllowBody(r.fields, { 0: 'me' })).toThrow();
  expect(buildAllowBody(r.fields, { 0: 'me', 1: 'pw' })).toEqual({
    action: 'allow', values: [{ index: 0, value: 'me' }, { index: 1, value: 'pw' }],
  });
  expect(DENY_BODY).toEqual({ action: 'deny' });
});

test('an error line never reflects the server body', () => {
  const e = Object.assign(new Error('400: {"value":"hunter2"}'), { status: 400 });
  expect(secretErrorText(e)).not.toContain('hunter2');
  expect(secretErrorText(Object.assign(new Error('x'), { status: 404 }))).toBe('This request is no longer pending.');
  expect(secretErrorText(new Error('network'))).toBe('Could not send. Try again.');
});

// ── store ────────────────────────────────────────────────────────────────────

const originals = { document: globalThis.document, navigator: globalThis.navigator, fetch: globalThis.fetch };

afterEach(() => {
  for (const [key, value] of Object.entries(originals)) {
    if (value === undefined) delete globalThis[key];
    else Object.defineProperty(globalThis, key, { configurable: true, writable: true, value });
  }
});

let calls;
beforeEach(() => {
  Object.defineProperty(globalThis, 'document', { configurable: true, writable: true, value: { hidden: false } });
  Object.defineProperty(globalThis, 'navigator', { configurable: true, writable: true, value: {} });
  calls = [];
  globalThis.fetch = (path, opts) => {
    calls.push({ path: String(path), method: opts?.method, body: opts?.body, headers: opts?.headers });
    return Promise.resolve(new Response('', { status: 204 }));
  };
  for (const t of getToasts()) removeToast(t.id);
  setState({
    sessions: { s1: { id: 's1', title: 'Mac keychain', state: 'running', messages: [], subagents: {} } },
    isMobile: true, activeSession: null, drawerOpen: false, paletteOpen: false,
  });
  __resetAttentionArrivalsForTests();
});

test('secret_request queues the card, flips the session to permission and toasts a generic line', () => {
  handleWsSecretRequest('s1', COMMAND, 5);
  const s = store.get().sessions.s1;
  expect(s.state).toBe('permission');
  expect(s.pendingSecrets.map((r) => r.id)).toEqual(['sec_1']);
  expect(sessionDisplayDotState(s)).toBe('permission');
  const [toast] = getToasts().filter((t) => t.sessionId === 's1');
  expect(toast.detail).toBe('Needs a password');
  expect(JSON.stringify(toast)).not.toContain('unlock-keychain');
});

test('several requests queue oldest first, once each', () => {
  handleWsSecretRequest('s1', COMMAND, 5);
  handleWsSecretRequest('s1', BROWSER, 6);
  handleWsSecretRequest('s1', COMMAND, 7);
  expect(store.get().sessions.s1.pendingSecrets.map((r) => r.id)).toEqual(['sec_1', 'sec_2']);
});

test('secret_resolved removes exactly that request', () => {
  handleWsSecretRequest('s1', COMMAND, 5);
  handleWsSecretRequest('s1', BROWSER, 6);
  handleWsSecretResolved('s1', { id: 'sec_1' });
  expect(store.get().sessions.s1.pendingSecrets.map((r) => r.id)).toEqual(['sec_2']);
  handleWsSecretResolved('s1', { id: 'nope' });
  expect(store.get().sessions.s1.pendingSecrets.map((r) => r.id)).toEqual(['sec_2']);
  handleWsSecretResolved('s1', { id: 'sec_2' });
  expect(store.get().sessions.s1.pendingSecrets).toEqual([]);
});

test('init restores the pending requests and replaces stale ones', () => {
  handleWsSecretRequest('s1', COMMAND, 5);
  handleWsInit('s1', { state: 'permission', messages: [], pending_secret_requests: [BROWSER, COMMAND] });
  expect(store.get().sessions.s1.pendingSecrets.map((r) => r.id)).toEqual(['sec_2', 'sec_1']);
  handleWsInit('s1', { state: 'idle', messages: [] });
  expect(store.get().sessions.s1.pendingSecrets).toEqual([]);
});

test('answering POSTs the contract body and drops the card', async () => {
  handleWsSecretRequest('s1', COMMAND, 5);
  await resolveSecretAction('s1', 'sec_1', { action: 'allow', values: [{ index: 0, value: 'pw' }] });
  expect(calls).toHaveLength(1);
  expect(calls[0].path).toBe('/api/sessions/s1/secret-requests/sec_1');
  expect(calls[0].method).toBe('POST');
  expect(JSON.parse(calls[0].body)).toEqual({ action: 'allow', values: [{ index: 0, value: 'pw' }] });
  expect(calls[0].headers['X-Moa-Request']).toBe('1');
  expect(store.get().sessions.s1.pendingSecrets).toEqual([]);
});

test('a refused answer keeps the card pending', async () => {
  handleWsSecretRequest('s1', COMMAND, 5);
  globalThis.fetch = () => Promise.resolve(new Response('missing', { status: 400 }));
  await expect(resolveSecretAction('s1', 'sec_1', { action: 'allow', values: [] })).rejects.toMatchObject({ status: 400 });
  expect(store.get().sessions.s1.pendingSecrets).toHaveLength(1);
});
