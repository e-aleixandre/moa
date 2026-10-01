import { describe, expect, test } from 'bun:test';
import { isPresent, presenceFrame, shownSessionIds, watchPresence } from './presence.js';

const tile = (id, sessionId) => ({ type: 'tile', id, sessionId });
const tree = { type: 'split', children: [tile('t1', 'a'), tile('t2', 'b')] };
const session = (hydrated = true) => ({ historyHydrated: hydrated });
const visibleDoc = () => ({ visibilityState: 'visible', addEventListener() {}, removeEventListener() {} });

function state(over = {}) {
  return {
    isMobile: false, view: null, activeSession: null, tileTree: tree, focusedTile: 't1',
    sessions: { a: session(), b: session() }, ...over,
  };
}

describe('shownSessionIds', () => {
  test('a single conversation shows only the focused tile, although the tree has more', () => {
    expect(shownSessionIds(state())).toEqual(['a']);
    expect(shownSessionIds(state({ focusedTile: 't2' }))).toEqual(['b']);
  });
  test('the grid shows every tile', () => {
    expect(shownSessionIds(state({ view: 'grid' }))).toEqual(['a', 'b']);
  });
  test('the tasks screen shows no conversation', () => {
    expect(shownSessionIds(state({ view: 'tasks' }))).toEqual([]);
  });
  test('mobile Tasks and Inbox cover the conversation; desktop Inbox does not', () => {
    const mobile = { isMobile: true, activeSession: 'a' };
    expect(shownSessionIds(state({ ...mobile, view: 'tasks' }))).toEqual([]);
    expect(shownSessionIds(state({ ...mobile, inboxOpen: true }))).toEqual([]);
    expect(shownSessionIds(state({ inboxOpen: true }))).toEqual(['a']);
  });
  test('a subagent or bash detail replaces the conversation, on desktop and mobile', () => {
    const sessions = (over) => ({ a: { ...session(), ...over }, b: session() });
    for (const over of [{ viewingSubagent: 'job' }, { viewingBashJob: 'job' }]) {
      expect(shownSessionIds(state({ sessions: sessions(over) }))).toEqual([]);
      expect(shownSessionIds(state({ isMobile: true, activeSession: 'a', sessions: sessions(over) }))).toEqual([]);
      expect(shownSessionIds(state({ view: 'grid', sessions: sessions(over) }))).toEqual(['b']);
    }
  });
  test('nothing is shown while the session list is still loading', () => {
    expect(shownSessionIds(state({ sessionsLoaded: false }))).toEqual([]);
    expect(shownSessionIds(state({ sessionsLoaded: false, isMobile: true, activeSession: 'a' }))).toEqual([]);
  });
  test('mobile shows the active session, whatever the tree says', () => {
    expect(shownSessionIds(state({ isMobile: true, activeSession: 'b' }))).toEqual(['b']);
    expect(shownSessionIds(state({ isMobile: true }))).toEqual([]);
  });
});

describe('isPresent', () => {
  test('needs the page visible, the session shown and its init landed', () => {
    const doc = visibleDoc();
    expect(isPresent(state(), 'a', doc)).toBe(true);
    expect(isPresent(state(), 'b', doc)).toBe(false); // connected, not shown
    expect(isPresent(state({ view: 'tasks' }), 'a', doc)).toBe(false);
    expect(isPresent(state({ sessions: { a: session(false) } }), 'a', doc)).toBe(false); // before init
    expect(isPresent(state(), 'a', { visibilityState: 'hidden' })).toBe(false);
  });
});

describe('watchPresence', () => {
  function harness(initial) {
    let current = initial;
    const listeners = new Set();
    const sent = [];
    const ws = (id) => ({ readyState: 1, send: (m) => sent.push([id, JSON.parse(m).visible]) });
    const sockets = { a: ws('a'), b: ws('b') };
    const stop = watchPresence({
      connections: () => Object.entries(sockets),
      getState: () => current,
      subscribe: (fn) => { listeners.add(fn); return () => listeners.delete(fn); },
      doc: visibleDoc(),
    });
    const set = (next) => { current = next; for (const fn of listeners) fn(); };
    return { sent, set, stop };
  }

  test('says nothing until the init lands, then only about the shown conversation', () => {
    const h = harness(state({ sessions: { a: session(false), b: session(false) } }));
    expect(h.sent).toEqual([]);
    h.set(state());
    expect(h.sent).toEqual([['a', true], ['b', false]]);
    h.stop();
  });

  test('reports a change once, not on every store update', () => {
    const h = harness(state());
    h.set(state());
    h.set(state());
    expect(h.sent.filter(([id]) => id === 'a')).toHaveLength(1);
    h.set(state({ view: 'tasks' }));
    expect(h.sent.filter(([id]) => id === 'a').map(([, v]) => v)).toEqual([true, false]);
    h.stop();
  });

  test('presenceFrame is the wire format the server reads', () => {
    expect(JSON.parse(presenceFrame(true))).toEqual({ type: 'presence', visible: true });
  });
});
