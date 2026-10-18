// secret-signal.test.js — run with `bun test`
//
// A session blocked on a password says so where the owner looks (its row, its
// owner's line, the live tray) and raises one global toast with Enter, unless
// the user is already in that session.
import { test, expect, beforeEach, afterEach } from 'bun:test';
import { store, setState, updateSession } from './store.js';
import { getToasts, removeToast, addToast } from './notifications.js';
import { watchSecretSignal } from './secret-signal.js';
import { handleWsSecretRequest, handleWsSecretResolved } from './ws-handlers.js';
import { readFileSync } from 'node:fs';
import { loadSessions, deleteSession } from './session-actions.js';
import { openSession } from './tile-actions.js';
import { ownerChildRow, ownerLine, ownerRows } from './owners-model.js';
import { liveTrayAgents } from './stream-model.js';
import { sessionRowReason } from '../layout/Sidebar/sessions.js';

const REQ = {
  id: 'sec_1', requester: 'main', kind: 'command', command: 'ssh me@canary-host', cwd: '/w',
  actionFingerprint: 'abc', fields: [{ index: 0, label: 'Password', kind: 'password' }],
};
const child = (over = {}) => ({
  id: 'c1', title: 'Fix webhook retries', state: 'permission', ownerId: 'own1', origin: 'owner',
  messages: [], subagents: {}, ...over,
});

const saved = {};
let stopWatching = () => {};
beforeEach(() => {
  for (const key of ['document', 'navigator', 'fetch']) saved[key] = globalThis[key];
  Object.defineProperty(globalThis, 'document', { configurable: true, writable: true, value: { hidden: false, visibilityState: 'visible', addEventListener() {}, removeEventListener() {} } });
  Object.defineProperty(globalThis, 'navigator', { configurable: true, writable: true, value: {} });
  for (const t of getToasts()) removeToast(t.id);
  stopWatching = watchSecretSignal();
  setState({ sessions: { c1: child({ state: 'running' }) }, isMobile: true, activeSession: null, drawerOpen: false, view: null, inboxOpen: false, sessionsLoaded: true });
});
afterEach(() => {
  stopWatching();
  for (const [key, value] of Object.entries(saved)) {
    if (value === undefined) delete globalThis[key];
    else Object.defineProperty(globalThis, key, { configurable: true, writable: true, value });
  }
});

const secretToasts = () => getToasts().filter((t) => t.sessionId === 'c1');
const rosterEntry = (ids, extra = {}) => ({
  id: 'c1', title: 'Fix webhook retries', state: ids.length ? 'permission' : 'running', cwd: '/w',
  origin: 'owner', owner_id: 'own1',
  // The count is what an older server sent; the ids are what this one sends.
  pending_secrets: ids.length || undefined, pending_secret_ids: ids.length ? ids : undefined, ...extra,
});
const serve = (list, onDelete) => {
  globalThis.fetch = async (path, opts) => {
    if (opts?.method === 'DELETE') { onDelete?.(); return new Response(null, { status: 204 }); }
    return new Response(JSON.stringify(list), { status: 200 });
  };
};
// look puts the conversation really on screen: selected, hydrated, page visible.
const look = (over = {}) => setState({
  activeSession: 'c1', sessionsLoaded: true,
  sessions: { ...store.get().sessions, c1: { ...store.get().sessions.c1, historyHydrated: true } }, ...over,
});
const poll = async (ids) => { serve([rosterEntry(ids)]); await loadSessions(); };

test('a row blocked only on a password says so, with the lock', () => {
  expect(sessionRowReason(child({ secretIds: ['sec_1'] }))).toEqual({ text: 'Needs password', tone: 'yellow', lock: true });
  expect(sessionRowReason(child({ secretIds: ['sec_1'], pendingSecrets: [REQ] }))?.text).toBe('Needs password');
});

test('a session that also asks a question keeps the plain sentence', () => {
  expect(sessionRowReason(child({ secretIds: ['sec_1'], pendingAsk: { id: 'a' } }))).toEqual({ text: 'Needs your answer', tone: 'yellow' });
  expect(sessionRowReason(child({ pendingPerm: { id: 'p' } }))?.text).toBe('Needs your answer');
  expect(sessionRowReason(child())?.lock).toBeUndefined();
});

test('the owner line names the password only when every waiting child waits on one', () => {
  const owner = (children) => ({ id: 'own1', name: 'Winerim', session_state: '', children });
  const locked = ownerChildRow(child({ secretIds: ['sec_1'] }));
  const asking = ownerChildRow(child({ id: 'c2', pendingAsk: { id: 'a' } }));
  expect(ownerLine(owner([locked])).tail).toBe('1 needs a password');
  expect(ownerLine(owner([locked, asking])).tail).toBe('2 waiting on you');
  expect(ownerLine(owner([asking])).tail).toBe('1 waiting on you');
});

test('ownerRows carries the lock from the store to the owner line', () => {
  const rows = ownerRows([{ id: 'own1', name: 'Winerim' }], { c1: child({ secretIds: ['sec_1'] }) });
  expect(ownerLine(rows[0]).tail).toBe('1 needs a password');
});

test('the owner tray marks the blocked child', () => {
  const chips = liveTrayAgents({ id: 'os', messages: [], subagents: {} }, { c1: child({ secretIds: ['sec_1'] }) }, [{ id: 'own1', session_id: 'os' }]);
  expect(chips).toHaveLength(1);
  expect(chips[0]).toMatchObject({ action: 'Needs password', lock: true });
});

test('a request in a session you are not in raises one sticky toast with Enter, once per request', () => {
  handleWsSecretRequest('c1', REQ, 3);
  handleWsSecretRequest('c1', REQ, 4);
  const toasts = secretToasts();
  expect(toasts).toHaveLength(1);
  expect(toasts[0]).toMatchObject({ type: 'attention', sticky: true, detail: 'Stopped until you enter the password', action: { label: 'Enter' } });
  expect(JSON.stringify(toasts[0])).not.toContain('canary-host');
});

test('no toast while you are in that session', () => {
  look();
  handleWsSecretRequest('c1', REQ, 3);
  expect(secretToasts()).toHaveLength(0);
});

test('resolving the last request takes the toast and the mark down', () => {
  handleWsSecretRequest('c1', REQ, 3);
  handleWsSecretResolved('c1', { id: 'sec_1' });
  expect(secretToasts()).toHaveLength(0);
  expect(store.get().sessions.c1.secretIds).toEqual([]);
  expect(sessionRowReason(store.get().sessions.c1)?.lock).toBeUndefined();
});

test('a poll with open requests raises the toast, even when the app opens already blocked', async () => {
  setState({ sessions: {} });
  await poll(['sec_1']);
  expect(secretToasts()).toHaveLength(1);
});

test('F1: a request cancelled elsewhere never comes back as a toast, however often the poll repeats', async () => {
  handleWsSecretRequest('c1', REQ, 3); // the socket cached its metadata
  expect(secretToasts()).toHaveLength(1);
  await poll([]);
  await poll([]);
  await poll([]);
  expect(secretToasts()).toHaveLength(0);
  expect(store.get().sessions.c1.secretIds).toEqual([]);
});

test('F2: deleting the session takes its toast down', async () => {
  await poll(['sec_1']);
  expect(secretToasts()).toHaveLength(1);
  serve([], () => {});
  await deleteSession('c1');
  expect(secretToasts()).toHaveLength(0);
});

test('F2: a session that vanishes from the roster takes its toast down', async () => {
  await poll(['sec_1']);
  expect(secretToasts()).toHaveLength(1);
  serve([]);
  await loadSessions();
  expect(secretToasts()).toHaveLength(0);
});

test('F3: a dismissed request stays dismissed, but a new one in its place announces itself', async () => {
  await poll(['sec_a']);
  secretToasts()[0].onDismiss();
  removeToast(secretToasts()[0].id);
  await poll(['sec_a']);
  expect(secretToasts()).toHaveLength(0);
  // A is cancelled and B opens between two polls: still one open request.
  await poll(['sec_b']);
  expect(secretToasts()).toHaveLength(1);
});

test('F3: a second request beside an announced one replaces the toast instead of stacking', async () => {
  await poll(['sec_a']);
  await poll(['sec_a', 'sec_b']);
  expect(secretToasts()).toHaveLength(1);
});

test('selected is not shown: behind Tasks the request announces itself, and walking to the owner never loses it', () => {
  look({ view: 'tasks' });
  handleWsSecretRequest('c1', REQ, 3);
  expect(secretToasts()).toHaveLength(1);
  // The navigation passes through a frame where Tasks has closed and the
  // hidden conversation is still the selected one, before the owner is.
  setState({ view: null });
  setState({ activeSession: 'own-session' });
  expect(secretToasts()).toHaveLength(1);
});

test('selected is not shown: with the phone drawer over it the request announces itself', () => {
  look({ drawerOpen: true });
  handleWsSecretRequest('c1', REQ, 3);
  expect(secretToasts()).toHaveLength(1);
});

test('selected is not shown: in a background page the request announces itself', () => {
  const page = globalThis.document;
  Object.defineProperty(globalThis, 'document', { configurable: true, writable: true, value: { ...page, hidden: true, visibilityState: 'hidden' } });
  const hadWindow = 'window' in globalThis;
  if (!hadWindow) globalThis.window = {};
  try {
    look();
    handleWsSecretRequest('c1', REQ, 3);
    expect(secretToasts()).toHaveLength(1);
  } finally {
    if (!hadWindow) delete globalThis.window;
    Object.defineProperty(globalThis, 'document', { configurable: true, writable: true, value: page });
  }
});

test('a card you saw and left unanswered tells you again: the session is still blocked', () => {
  look();
  handleWsSecretRequest('c1', REQ, 3);
  expect(secretToasts()).toHaveLength(0);
  setState({ activeSession: 'other' });
  expect(secretToasts()).toHaveLength(1);
  setState({ activeSession: 'c1' });
  expect(secretToasts()).toHaveLength(0);
});

test('closing the toast with X hides that request only', async () => {
  await poll(['sec_a']);
  secretToasts()[0].onDismiss();
  removeToast(secretToasts()[0].id);
  await poll(['sec_a']);
  expect(secretToasts()).toHaveLength(0);
  await poll(['sec_b']);
  expect(secretToasts()).toHaveLength(1);
});

test('F4: Enter opens the session and closes the drawer that would cover its card', () => {
  setState({ drawerOpen: true, drawerStep: 'list' });
  handleWsSecretRequest('c1', REQ, 3);
  secretToasts()[0].action.onClick();
  expect(store.get().activeSession).toBe('c1');
  expect(store.get().drawerOpen).toBe(false);
});

test('F6: entering the session by any route withdraws the toast at once, with no poll', () => {
  handleWsSecretRequest('c1', REQ, 3);
  expect(secretToasts()).toHaveLength(1);
  setState({ sessionsLoaded: true });
  openSession('c1');
  // The toast goes when the conversation is really rendered: its history landed.
  updateSession('c1', { historyHydrated: true });
  expect(secretToasts()).toHaveLength(0);
});

test('F5: a standing toast docks below the phone chrome instead of covering it', () => {
  const css = readFileSync(new URL('../components/Toast/ToastContainer.css', import.meta.url), 'utf8');
  const jsx = readFileSync(new URL('../components/Toast/ToastContainer.jsx', import.meta.url), 'utf8');
  expect(jsx).toContain('has-sticky');
  expect(css).toMatch(/@media \(max-width: 768px\)[\s\S]*\.toast-stack\.has-sticky\s*\{[^}]*top:\s*calc\(var\(--shell-top\) \+ 64px\)/);
});

test('the toast keeps one identity per request: leaving and coming back is the same toast, not a second one', async () => {
  await poll(['sec_a']);
  const first = secretToasts()[0].id;
  look();
  expect(secretToasts()).toHaveLength(0);
  setState({ activeSession: 'other' });
  expect(secretToasts()).toHaveLength(1);
  expect(secretToasts()[0].id).toBe(first);
  // Raising it again while it stands replaces it.
  const { addToast } = await import('./notifications.js');
  addToast({ ...secretToasts()[0] });
  expect(secretToasts()).toHaveLength(1);
});

test('tapping the toast body does what Enter does: closes the drawer, opens the card', async () => {
  setState({ drawerOpen: true, activeSession: 'other' });
  await poll(['sec_a']);
  secretToasts()[0].onOpen();
  expect(store.get().drawerOpen).toBe(false);
  expect(store.get().activeSession).toBe('c1');
});

test('a request already in the first roster makes no vibration, even after a tap and before the list is marked loaded; a later one does', () => {
  stopWatching();
  let buzzes = 0;
  globalThis.navigator = { vibrate: () => { buzzes += 1; }, userActivation: { hasBeenActive: true } };
  globalThis.window = {};
  try {
    setState({ sessions: {}, sessionsLoaded: false });
    stopWatching = watchSecretSignal();
    // The roster lands (sessions with their open requests), and only after it the list is marked loaded.
    setState({ sessions: { c1: child({ secretIds: ['sec_old'] }) } });
    setState({ sessionsLoaded: true });
    expect(secretToasts()).toHaveLength(1);
    expect(buzzes).toBe(0);
    updateSession('c1', { secretIds: ['sec_old', 'sec_new'] });
    expect(buzzes).toBe(1);
  } finally { delete globalThis.window; }
});

test('a toast raised without an id never replaces another toast', () => {
  const a = addToast({ title: 'a', sticky: true });
  const b = addToast({ title: 'b', sticky: true });
  expect(a).not.toBe(b);
  expect(getToasts().filter((x) => x.sticky && (x.id === a || x.id === b))).toHaveLength(2);
  const real = Math.random; Math.random = () => 0.5;
  try {
    const ids = [addToast({ title: 'c', sticky: true }), addToast({ title: 'd', sticky: true })];
    expect(getToasts().filter((x) => ids.includes(x.id))).toHaveLength(2);
  } finally { Math.random = real; }
});

test('a request seen with its card visible never buzzes later, not even after a reconnect', () => {
  stopWatching();
  let buzzes = 0;
  globalThis.navigator = { vibrate: () => { buzzes += 1; }, userActivation: { hasBeenActive: true } };
  globalThis.window = {};
  try {
    stopWatching = watchSecretSignal();
    look();
    handleWsSecretRequest('c1', REQ, 3);
    expect(secretToasts()).toHaveLength(0);
    expect(buzzes).toBe(0);
    // The socket drops and reconnects: history is rehydrated, the request is the same.
    updateSession('c1', { historyHydrated: false });
    expect(buzzes).toBe(0);
    updateSession('c1', { historyHydrated: true });
    expect(buzzes).toBe(0);
    setState({ activeSession: 'other' });
    expect(secretToasts()).toHaveLength(1);
    expect(buzzes).toBe(0);
  } finally { delete globalThis.window; }
});
