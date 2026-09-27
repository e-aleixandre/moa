// attention-toast-detail.test.js — run with `bun test`
//
// A session away from the screen that asks a question or requests a
// permission raises a toast whose detail says which of the two it waits on.
import { test, expect, beforeEach, afterEach } from 'bun:test';
import { setState } from './store.js';
import { getToasts, removeToast } from './notifications.js';
import { handleWsAskUser, handleWsPermissionRequest } from './ws-handlers.js';
import { __resetAttentionArrivalsForTests } from './attention-arrivals.js';

const originals = { document: globalThis.document, navigator: globalThis.navigator };

afterEach(() => {
  for (const [key, value] of Object.entries(originals)) {
    if (value === undefined) delete globalThis[key];
    else Object.defineProperty(globalThis, key, { configurable: true, value });
  }
});

beforeEach(() => {
  Object.defineProperty(globalThis, 'document', { configurable: true, value: { hidden: false } });
  Object.defineProperty(globalThis, 'navigator', { configurable: true, value: {} });
  globalThis.fetch = () => Promise.resolve(new Response('', { status: 204 }));
  for (const t of getToasts()) removeToast(t.id);
  setState({
    sessions: { away: { id: 'away', title: 'design/toasts', state: 'running', messages: [], subagents: {} } },
    isMobile: true, activeSession: null, drawerOpen: false, paletteOpen: false,
  });
  __resetAttentionArrivalsForTests();
});

test('a question says it asks something, not that it needs permission', () => {
  handleWsAskUser('away', { id: 'a1', questions: [{ text: 'which branch?' }] });
  const [toast] = getToasts().filter(t => t.sessionId === 'away');
  expect(toast).toMatchObject({ type: 'attention', title: 'design/toasts', detail: 'Asks you a question' });
  expect(toast.detail).not.toContain('permission');
});

test('a permission request names the tool that needs it', () => {
  handleWsPermissionRequest('away', { id: 'p1', tool_name: 'bash', args: {} });
  const [toast] = getToasts().filter(t => t.sessionId === 'away');
  expect(toast).toMatchObject({ type: 'attention', detail: 'bash — needs permission' });
});
