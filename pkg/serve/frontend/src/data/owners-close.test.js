// owners-close.test.js — run with `bun test`
//
// Close owner is one server call; the client mirrors it. Only an explicit
// opening clears the flag, never the landing that follows a close.
import { test, expect, beforeEach } from 'bun:test';

const { setState, store } = await import('./store.js');
const { closeOwner } = await import('./owners.js');
const { openSession, autoSelectMobile, __resetBootForTests } = await import('./tile-actions.js');
const { resumeSession, markSessionClosedLocally } = await import('./session-actions.js');
const { landingOrder } = await import('./util/project-sessions.js');

const own = { id: 'own_a', name: 'A', session_id: 'oa', closed: false };
const calls = [];
let patchStatus = 200;
let serverClosed = true;

beforeEach(() => {
  calls.length = 0;
  patchStatus = 200;
  globalThis.fetch = (url, init = {}) => {
    calls.push(`${init.method || 'GET'} ${String(url).replace(/^https?:\/\/[^/]+/, '')}`);
    if ((init.method || 'GET') === 'PATCH') {
      return Promise.resolve(new Response(patchStatus === 200 ? '{}' : 'busy', { status: patchStatus }));
    }
    const list = String(url).endsWith('/api/owners') ? [{ ...own, closed: serverClosed }] : [];
    return Promise.resolve(new Response(JSON.stringify(list), { status: 200 }));
  };
  __resetBootForTests();
  setState({
    isMobile: true,
    activeSession: 'oa',
    sessions: {
      oa: { id: 'oa', kind: 'owner', state: 'idle', updated: 1 },
      child: { id: 'child', state: 'running', updated: 9, ownerId: 'own_a' },
    },
    owners: { list: [{ ...own }], loaded: true, error: null, retrying: false },
  });
});

test('Close is a single PATCH and the landing does not select the closed owner', async () => {
  expect(await closeOwner(own)).toBe(true);
  expect(calls.filter((c) => c.includes('/close'))).toEqual([]);
  expect(calls.filter((c) => c.startsWith('PATCH'))).toHaveLength(1);
  expect(store.get().owners.list[0].closed).toBe(true);
  expect(store.get().activeSession).not.toBe('oa');
  expect(landingOrder(store.get().sessions, store.get().owners.list)).not.toContain('oa');
});

test('a refused Close (409) changes nothing in the pane', async () => {
  patchStatus = 409;
  expect(await closeOwner(own)).toBe(false);
  expect(store.get().activeSession).toBe('oa');
  expect(store.get().owners.list[0].closed).toBe(false);
});

test('auto-selection leaves the flag; an explicit opening clears it', async () => {
  setState({ owners: { list: [{ ...own, closed: true }], loaded: true, error: null, retrying: false } });
  autoSelectMobile();
  expect(calls.filter((c) => c.startsWith('PATCH'))).toEqual([]);
  expect(store.get().owners.list[0].closed).toBe(true);

  expect(openSession('oa')).toBe(true);
  expect(store.get().owners.list[0].closed).toBe(false);
  expect(calls.filter((c) => c.startsWith('PATCH'))).toHaveLength(1);
});

function resumeFetch(release) {
  globalThis.fetch = async (url, init = {}) => {
    const method = init.method || 'GET';
    calls.push(`${method} ${String(url).replace(/^https?:\/\/[^/]+/, '')}`);
    if (method === 'POST' && String(url).endsWith('/resume')) {
      await release;
      return new Response(JSON.stringify({ id: 'oa', kind: 'owner', state: 'idle' }), { status: 200 });
    }
    if (String(url).includes('/api/sessions')) return new Response('[]', { status: 200 });
    return new Response('{}', { status: 200 });
  };
}

test('an automatic resume does not clear the flag; an explicit one does', async () => {
  setState({ activeSession: null, owners: { list: [{ ...own, closed: true }], loaded: true, error: null, retrying: false } });
  resumeFetch(Promise.resolve());
  await resumeSession('oa', { explicit: false });
  expect(store.get().owners.list[0].closed).toBe(true);
  expect(calls.some((c) => c.startsWith('PATCH'))).toBe(false);

  await resumeSession('oa');
  expect(store.get().owners.list[0].closed).toBe(false);
});

test('a resume answered after the owner was closed does not select it again', async () => {
  setState({ activeSession: null, owners: { list: [{ ...own, closed: true }], loaded: true, error: null, retrying: false } });
  let release;
  resumeFetch(new Promise((r) => { release = r; }));
  const pending = resumeSession('oa', { explicit: false });
  markSessionClosedLocally('oa');
  release();
  await pending;
  expect(store.get().activeSession).not.toBe('oa');
  expect(store.get().owners.list[0].closed).toBe(true);
});
