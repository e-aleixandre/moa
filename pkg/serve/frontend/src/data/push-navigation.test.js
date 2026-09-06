import { afterEach, expect, test } from 'bun:test';
import { installOpenSessionNavigation } from './push-navigation.js';
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
