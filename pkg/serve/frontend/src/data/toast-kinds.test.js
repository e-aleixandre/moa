// toast-kinds.test.js — run with `bun test`
//
// "Needs you" is only for a session that asks the user something, and only
// that — or a session failing away from the screen — alerts (vibration,
// system notification). A toast answering the user's own action is a Note or
// a Failed, with no alert. A failed run raises exactly one toast.
import { test, expect, beforeEach, afterEach } from 'bun:test';
import { setState } from './store.js';
import { getToasts, removeToast, triggerDone } from './notifications.js';
import { handleWsAskUser, handleWsStateChange } from './ws-handlers.js';
import { cancelBashJob, closeSession } from './session-actions.js';
import { __resetAttentionArrivalsForTests } from './attention-arrivals.js';

const originals = { document: globalThis.document, navigator: globalThis.navigator, window: globalThis.window };
let vibrations;
let systemNotifications;

afterEach(() => {
  for (const [key, value] of Object.entries(originals)) {
    if (value === undefined) delete globalThis[key];
    else Object.defineProperty(globalThis, key, { configurable: true, value });
  }
});

beforeEach(() => {
  vibrations = 0;
  systemNotifications = 0;
  // A hidden tab: every alert that can fire, does.
  Object.defineProperty(globalThis, 'document', { configurable: true, value: { hidden: true } });
  Object.defineProperty(globalThis, 'navigator', { configurable: true, value: { vibrate: () => { vibrations++; } } });
  class FakeNotification {
    static permission = 'granted';
    constructor() { systemNotifications++; }
  }
  Object.defineProperty(globalThis, 'window', { configurable: true, value: { Notification: FakeNotification } });
  globalThis.Notification = FakeNotification;
  globalThis.fetch = () => Promise.resolve(new Response('', { status: 204 }));
  for (const t of getToasts()) removeToast(t.id);
  setState({
    sessions: {
      s1: { id: 's1', title: 'design/toasts', state: 'running', messages: [], subagents: {} },
      untitled: { id: 'untitled', title: '', state: 'running', messages: [], subagents: {} },
    },
    isMobile: true, activeSession: null, drawerOpen: false, paletteOpen: false,
  });
  __resetAttentionArrivalsForTests();
});

function onScreen(id) {
  setState({ activeSession: id });
}

test('a question from a session is Needs you, with its alert', () => {
  handleWsAskUser('s1', { id: 'a1', questions: [{ text: 'which branch?' }] });
  expect(getToasts().map(t => t.type)).toEqual(['attention']);
  expect(vibrations).toBe(1);
  expect(systemNotifications).toBe(1);
});

test('a run failing away from the screen raises one Failed toast naming the session', () => {
  handleWsStateChange('s1', { state: 'error', error: 'provider 500' });
  const toasts = getToasts();
  expect(toasts).toHaveLength(1);
  expect(toasts[0]).toMatchObject({ type: 'error', title: 'design/toasts', detail: 'Run failed — provider 500' });
  expect(vibrations).toBe(1);
});

test('a run failing on screen raises one Failed toast and no alert', () => {
  onScreen('s1');
  handleWsStateChange('s1', { state: 'error', error: 'usage limit reached, resets in 2h' });
  const toasts = getToasts();
  expect(toasts).toHaveLength(1);
  expect(toasts[0]).toMatchObject({ type: 'error', title: 'Usage limit reached' });
  expect(vibrations).toBe(0);
  expect(systemNotifications).toBe(0);
});

test('a finished turn has no redundant second line and falls back to Untitled', () => {
  triggerDone({ id: 'untitled', title: '' }, false);
  const [toast] = getToasts();
  expect(toast).toMatchObject({ type: 'done', title: 'Untitled' });
  expect(toast.detail).toBeUndefined();
});

test('a job that could not be stopped is Failed, with no alert', async () => {
  onScreen('s1');
  globalThis.fetch = () => Promise.resolve(new Response('gone', { status: 404 }));
  await cancelBashJob('s1', 'j1').catch(() => {});
  const toasts = getToasts();
  expect(toasts).toHaveLength(1);
  expect(toasts[0]).toMatchObject({ type: 'error', title: 'Could not stop the job' });
  expect(vibrations).toBe(0);
  expect(systemNotifications).toBe(0);
});

test('closing a session that is still working is a Note, with no alert', async () => {
  onScreen('s1');
  globalThis.fetch = () => Promise.resolve(new Response('busy', { status: 409 }));
  await closeSession('s1').catch(() => {});
  const toasts = getToasts();
  expect(toasts).toHaveLength(1);
  expect(toasts[0]).toMatchObject({ type: 'info', title: 'Session is still working' });
  expect(vibrations).toBe(0);
  expect(systemNotifications).toBe(0);
});
