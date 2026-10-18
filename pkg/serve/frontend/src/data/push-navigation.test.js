import { afterEach, expect, test } from 'bun:test';
import { installNativeNavigation, installOpenSessionNavigation, openSessionInPlace } from './push-navigation.js';
import { setState, store } from './store.js';

class FakeServiceWorker {
  listeners = new Set();
  addEventListener(type, listener) { if (type === 'message') this.listeners.add(listener); }
  removeEventListener(type, listener) { if (type === 'message') this.listeners.delete(listener); }
  send(data, ports = []) { for (const listener of this.listeners) listener({ data, ports }); }
}

let restore;
afterEach(() => {
  if (restore) {
    setState(restore);
    restore = null;
  }
});

test('warm notification navigation waits for the initial session load', () => {
  restore = { sessions: store.get().sessions, sessionsLoaded: store.get().sessionsLoaded };
  setState({ sessions: {}, sessionsLoaded: false });
  const worker = new FakeServiceWorker();
  const selected = [];
  const stop = installOpenSessionNavigation({
    serviceWorker: worker,
    selectSession: id => selected.push(id),
    refreshSessions: () => { throw new Error('must not refresh after successful selection'); },
  });

  const acknowledgements = [];
  worker.send({ type: 'open-session', sessionId: 'session-1', requestId: 'tap-1' }, [{ postMessage: message => acknowledgements.push(message) }]);
  expect(selected).toEqual([]);
  expect(acknowledgements).toEqual([]);
  setState({ sessions: { 'session-1': { id: 'session-1' } }, sessionsLoaded: true });
  expect(selected).toEqual(['session-1']);
  expect(acknowledgements).toEqual([{ type: 'open-session-ack', requestId: 'tap-1' }]);

  stop();
});

test('unsafe notification session IDs are ignored and never acknowledged', () => {
  restore = { sessions: store.get().sessions, sessionsLoaded: store.get().sessionsLoaded };
  setState({ sessions: {}, sessionsLoaded: true });
  const worker = new FakeServiceWorker();
  const acknowledgements = [];
  const stop = installOpenSessionNavigation({ serviceWorker: worker, selectSession: () => { throw new Error('must not select'); } });

  worker.send({ type: 'open-session', sessionId: '../bad', requestId: 'tap-unsafe' }, [{ postMessage: message => acknowledgements.push(message) }]);
  expect(acknowledgements).toEqual([]);

  stop();
});

test('loaded stale session list refreshes and acknowledges only after retry succeeds', async () => {
  restore = { sessions: store.get().sessions, sessionsLoaded: store.get().sessionsLoaded };
  setState({ sessions: {}, sessionsLoaded: true });
  const worker = new FakeServiceWorker();
  const selected = [];
  const acknowledgements = [];
  let refreshes = 0;
  const stop = installOpenSessionNavigation({
    serviceWorker: worker,
    selectSession: id => {
      if (!store.get().sessions[id]) return false;
      selected.push(id);
      return true;
    },
    refreshSessions: async () => {
      refreshes++;
      setState({ sessions: { revived: { id: 'revived' } } });
    },
  });

  worker.send({ type: 'open-session', sessionId: 'revived', requestId: 'tap-retry' }, [{ postMessage: message => acknowledgements.push(message) }]);
  await Promise.resolve();
  await Promise.resolve();
  expect(refreshes).toBe(1);
  expect(selected).toEqual(['revived']);
  expect(acknowledgements).toEqual([{ type: 'open-session-ack', requestId: 'tap-retry' }]);

  stop();
});

test('unknown session after refresh withholds acknowledgement for SW fallback', async () => {
  restore = { sessions: store.get().sessions, sessionsLoaded: store.get().sessionsLoaded };
  setState({ sessions: {}, sessionsLoaded: true });
  const worker = new FakeServiceWorker();
  const acknowledgements = [];
  let refreshes = 0;
  const stop = installOpenSessionNavigation({
    serviceWorker: worker,
    selectSession: () => false,
    refreshSessions: async () => { refreshes++; },
  });

  worker.send({ type: 'open-session', sessionId: 'gone', requestId: 'tap-missing' }, [{ postMessage: message => acknowledgements.push(message) }]);
  await Promise.resolve();
  await Promise.resolve();
  expect(refreshes).toBe(1);
  expect(acknowledgements).toEqual([]);

  stop();
});

test('a pending-event tap opens the inbox and is acknowledged', () => {
  restore = { sessions: store.get().sessions, sessionsLoaded: store.get().sessionsLoaded, inboxOpen: store.get().inboxOpen };
  setState({ inboxOpen: false, sessionsLoaded: true });
  const worker = new FakeServiceWorker();
  const acknowledgements = [];
  const stop = installOpenSessionNavigation({ serviceWorker: worker });

  worker.send({ type: 'open-inbox', requestId: 'tap-inbox' }, [{ postMessage: message => acknowledgements.push(message) }]);
  expect(store.get().inboxOpen).toBe(true);
  expect(acknowledgements).toEqual([{ type: 'open-inbox-ack', requestId: 'tap-inbox' }]);

  stop();
});

test('in-place open waits for the first session list and does not refresh on success', async () => {
  restore = { sessions: store.get().sessions, sessionsLoaded: store.get().sessionsLoaded };
  setState({ sessions: {}, sessionsLoaded: false });
  const selected = [];
  const opened = openSessionInPlace('session-1', {
    selectSession: id => { selected.push(id); return true; },
    refreshSessions: () => { throw new Error('must not refresh after successful selection'); },
  });
  await Promise.resolve();
  expect(selected).toEqual([]);
  setState({ sessions: { 'session-1': { id: 'session-1' } }, sessionsLoaded: true });
  expect(await opened).toBe(true);
  expect(selected).toEqual(['session-1']);
});

test('in-place open refreshes a stale list once, then reports failure for a reload', async () => {
  restore = { sessions: store.get().sessions, sessionsLoaded: store.get().sessionsLoaded };
  setState({ sessions: {}, sessionsLoaded: true });
  let known = false;
  let refreshes = 0;
  const deps = {
    selectSession: () => known,
    refreshSessions: async () => { refreshes += 1; },
  };
  expect(await openSessionInPlace('missing', deps)).toBe(false);
  expect(refreshes).toBe(1);
  deps.refreshSessions = async () => { refreshes += 1; known = true; };
  expect(await openSessionInPlace('arrives', deps)).toBe(true);
  expect(refreshes).toBe(2);
});

test('in-place open rejects unsafe IDs and gives up when the list never loads', async () => {
  restore = { sessions: store.get().sessions, sessionsLoaded: store.get().sessionsLoaded };
  setState({ sessions: {}, sessionsLoaded: false });
  const never = () => { throw new Error('must not select'); };
  expect(await openSessionInPlace('../bad', { selectSession: never })).toBe(false);
  expect(await openSessionInPlace('session-1', { selectSession: never, timeoutMs: 5 })).toBe(false);
});

test('native navigation is exposed on the page and removed on teardown', async () => {
  restore = { sessions: store.get().sessions, sessionsLoaded: store.get().sessionsLoaded };
  setState({ sessions: {}, sessionsLoaded: true });
  const target = {};
  const stop = installNativeNavigation(target);
  expect(target.MoaNavigation.version).toBe(1);
  expect(await target.MoaNavigation.openSession('../bad')).toBe(false);
  stop();
  expect(target.MoaNavigation).toBeUndefined();
});

test('a call-request tap opens the request card link and acknowledges', async () => {
  const worker = new FakeServiceWorker();
  const links = [];
  const selected = [];
  const acks = [];
  const stop = installOpenSessionNavigation({
    serviceWorker: worker,
    openCallRequest: async id => { links.push(id); return true; },
    selectSession: id => selected.push(id),
  });
  worker.send({ type: 'open-call-request', callRequestId: 'cr-1', sessionId: 's1', requestId: 'tap-9' }, [{ postMessage: m => acks.push(m) }]);
  await new Promise(r => setTimeout(r, 0));
  expect(links).toEqual(['cr-1']);
  expect(selected).toEqual([]);
  expect(acks).toEqual([{ type: 'open-call-request-ack', requestId: 'tap-9' }]);
  stop();
});

test('a call-request tap on a request that ended opens the owner conversation instead', async () => {
  restore = { sessions: store.get().sessions, sessionsLoaded: store.get().sessionsLoaded };
  setState({ sessions: { s1: { id: 's1' } }, sessionsLoaded: true });
  const worker = new FakeServiceWorker();
  const selected = [];
  const acks = [];
  const stop = installOpenSessionNavigation({
    serviceWorker: worker,
    openCallRequest: async () => false,
    selectSession: id => { selected.push(id); return true; },
  });
  worker.send({ type: 'open-call-request', callRequestId: 'gone', sessionId: 's1', requestId: 'tap-10' }, [{ postMessage: m => acks.push(m) }]);
  await new Promise(r => setTimeout(r, 0));
  expect(selected).toEqual(['s1']);
  expect(acks).toEqual([{ type: 'open-call-request-ack', requestId: 'tap-10' }]);
  stop();
});
