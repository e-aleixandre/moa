// Routing → presence: drives the REAL store actions the screens use to cover
// or replace a conversation, and checks what the watcher tells the server. A
// predicate test built from hand-made state would not notice the day an action
// starts writing a different field. Only the socket and the document are
// fakes, and both are injected, so nothing global is touched.
import { afterEach, beforeEach, describe, expect, test } from 'bun:test';
import { setState, store } from './store.js';
import { watchPresence } from './presence.js';
import { openInbox, closeInbox } from './events.js';
import { openTasksView, closeTasksView } from './tasks-view.js';
import { openBashJob } from './session-actions.js';
import { focusTile, setActiveSession } from './tile-actions.js';

const tree = { type: 'split', children: [{ type: 'tile', id: 1, sessionId: 'a' }, { type: 'tile', id: 2, sessionId: 'b' }] };
const session = (id) => ({ id, state: 'running', messages: [], subagents: id === 'a' ? { job: { id: 'job' } } : {}, bashJobs: {}, historyHydrated: true });

let stop;
let sockets;
const doc = { visibilityState: 'visible', addEventListener() {}, removeEventListener() {} };

function openSocket(id, hydrated = true) {
  const ws = { readyState: 1, sent: [], send(frame) { this.sent.push(JSON.parse(frame).visible); } };
  sockets[id] = ws;
  if (hydrated) setState({ sessions: { ...store.get().sessions, [id]: { ...store.get().sessions[id], historyHydrated: true } } });
  return ws;
}
const last = (ws) => ws.sent.at(-1);

function boot(over = {}) {
  setState({
    sessionsLoaded: true, isMobile: false, view: null, inboxOpen: false, activeSession: 'a',
    focusedTile: 1, tileTree: tree, sessions: { a: session('a'), b: session('b') }, ...over,
  });
  sockets = {};
  stop = watchPresence({
    connections: () => Object.entries(sockets),
    getState: store.get,
    subscribe: store.subscribe,
    doc,
  });
}

beforeEach(() => { doc.visibilityState = 'visible'; });
afterEach(() => { stop?.(); closeInbox(); setState({ view: null }); });

describe('what covers or replaces a conversation stops counting as presence', () => {
  test('desktop: focusing the other tile, the grid, and the tasks screen', () => {
    boot();
    const a = openSocket('a');
    const b = openSocket('b');
    expect([last(a), last(b)]).toEqual([true, false]);
    focusTile(2, { focusInput: false });
    expect([last(a), last(b)]).toEqual([false, true]);
    openTasksView();
    expect([last(a), last(b)]).toEqual([false, false]);
    closeTasksView();
  });

  test('mobile: the tasks page and the inbox cover the active conversation', () => {
    boot({ isMobile: true });
    const a = openSocket('a');
    expect(last(a)).toBe(true);
    openTasksView();
    expect(last(a)).toBe(false);
    closeTasksView();
    expect(last(a)).toBe(true);
    openInbox();
    expect(last(a)).toBe(false);
    closeInbox();
    expect(last(a)).toBe(true);
  });

  test('a bash detail replaces the parent stream on desktop and on mobile', () => {
    for (const isMobile of [false, true]) {
      stop?.();
      boot({ isMobile });
      const a = openSocket('a');
      expect(last(a)).toBe(true);
      openBashJob('a', 'job');
      expect(last(a)).toBe(false);
    }
  });

  test('switching the mobile session moves presence to the new one', () => {
    boot({ isMobile: true });
    const a = openSocket('a');
    const b = openSocket('b');
    expect([last(a), last(b)]).toEqual([true, false]);
    setActiveSession('b');
    expect([last(a), last(b)]).toEqual([false, true]);
  });

  test('nothing is reported while the session list loads, nor before the init', () => {
    boot({ sessionsLoaded: false });
    const a = openSocket('a');
    expect(last(a)).toBe(false);
    setState({ sessionsLoaded: true });
    expect(last(a)).toBe(true);
    stop();
    boot();
    const cold = openSocket('a', false);
    setState({ sessions: { ...store.get().sessions, a: { ...store.get().sessions.a, historyHydrated: false } } });
    expect(cold.sent.every((v) => v === false)).toBe(true);
  });
});
