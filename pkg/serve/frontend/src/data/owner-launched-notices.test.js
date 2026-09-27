// owner-launched-notices.test.js — run with `bun test`
//
// A session an owner launched (origin "owner") reports to that owner; it must
// not raise a toast for the user. A session the user opened in the owner's
// codebase, and the owner's own conversation, still do.
import { test, expect, beforeEach, afterEach } from 'bun:test';
import { store, setState } from './store.js';
import { getToasts, removeToast } from './notifications.js';
import {
  handleWsAskUser, handleWsPermissionRequest, handleWsStateChange,
  handleWsSubagentComplete, handleWsBashComplete, handleWsCommandDequeued,
} from './ws-handlers.js';
import { __resetAttentionArrivalsForTests } from './attention-arrivals.js';

const SESSIONS = {
  launched: { id: 'launched', origin: 'owner', kind: '', ownerId: 'own_1' },
  userInOwner: { id: 'userInOwner', origin: '', kind: '', ownerId: 'own_1' },
  owner: { id: 'owner', origin: 'owner', kind: 'owner', ownerId: '' },
};

const originals = { document: globalThis.document, navigator: globalThis.navigator };

afterEach(() => {
  for (const [key, value] of Object.entries(originals)) {
    if (value === undefined) delete globalThis[key];
    else Object.defineProperty(globalThis, key, { configurable: true, value });
  }
});

beforeEach(() => {
  // The tab is in the foreground: only the toast is raised, not the system
  // notification that a hidden tab would add.
  Object.defineProperty(globalThis, 'document', { configurable: true, value: { hidden: false } });
  Object.defineProperty(globalThis, 'navigator', { configurable: true, value: {} });
  globalThis.fetch = () => Promise.resolve(new Response('', { status: 204 }));
  for (const t of getToasts()) removeToast(t.id);
  const sessions = {};
  for (const [id, s] of Object.entries(SESSIONS)) {
    sessions[id] = { ...s, title: id, state: 'running', messages: [], subagents: {} };
  }
  // Nothing on screen: every session is away, so every event would notify.
  setState({ sessions, isMobile: true, activeSession: null, drawerOpen: false, paletteOpen: false });
  __resetAttentionArrivalsForTests();
});

// Every session event that raises a toast for a session that is not on screen.
function raiseEveryNotice(id) {
  handleWsAskUser(id, { id: 'a1', questions: [{ text: 'which branch?' }] });
  handleWsPermissionRequest(id, { id: 'p1', tool_name: 'bash', args: {} });
  handleWsStateChange(id, { state: 'error', error: 'boom' });
  setState({ sessions: { ...store.get().sessions, [id]: { ...store.get().sessions[id], state: 'running' } } });
  handleWsStateChange(id, { state: 'idle' });
  handleWsSubagentComplete(id, { job_id: 'j1', status: 'completed', task: 'look around' });
  handleWsBashComplete(id, { job_id: 'b1', status: 'failed', command: 'make' });
  handleWsCommandDequeued(id, { id: 'c1', executed: false, raw: '/model x', err: 'unknown' });
  return getToasts().filter(t => t.sessionId === id).length;
}

test('a session the owner launched raises no toast', () => {
  expect(raiseEveryNotice('launched')).toBe(0);
});

test('a user session in an owner codebase still raises its toasts', () => {
  expect(raiseEveryNotice('userInOwner')).toBe(8);
});

test('the owner asking in its own conversation still raises its toasts', () => {
  expect(raiseEveryNotice('owner')).toBe(8);
});
