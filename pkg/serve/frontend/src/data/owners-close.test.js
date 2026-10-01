// owners-close.test.js — run with `bun test`
//
// Close owner is Close session on the owner's conversation: nothing else is
// stored, and "closed" is the unloaded state the owners list reports.
import { test, expect, beforeEach } from 'bun:test';

const { setState, store } = await import('./store.js');
const { closeOwner } = await import('./owners.js');
const { __resetBootForTests } = await import('./tile-actions.js');
const { resumeSession } = await import('./session-actions.js');
const { splitOwners, ownerRows } = await import('./owners-model.js');

const own = { id: 'own_a', name: 'A', session_id: 'oa' };
const calls = [];
let closeStatus = 200;
let serverState = 'idle';

beforeEach(() => {
  calls.length = 0;
  closeStatus = 200;
  serverState = 'idle';
  globalThis.fetch = (url, init = {}) => {
    calls.push(`${init.method || 'GET'} ${String(url).replace(/^https?:\/\/[^/]+/, '')}`);
    if (String(url).endsWith('/close')) {
      if (closeStatus === 200) serverState = 'saved';
      return Promise.resolve(new Response(closeStatus === 200 ? '{}' : 'busy', { status: closeStatus }));
    }
    const list = String(url).endsWith('/api/owners') ? [{ ...own, session_state: serverState }] : [];
    return Promise.resolve(new Response(JSON.stringify(list), { status: 200 }));
  };
  __resetBootForTests();
  setState({
    isMobile: true,
    activeSession: 'oa',
    sessions: { oa: { id: 'oa', kind: 'owner', state: 'idle', updated: 1 } },
    owners: { list: [{ ...own, session_state: 'idle' }], loaded: true, error: null, retrying: false },
  });
});

test('Close closes the session and the owner moves to the closed group', async () => {
  expect(await closeOwner(own)).toBe(true);
  expect(calls).toContain('POST /api/sessions/oa/close');
  expect(calls.some((c) => c.startsWith('PATCH'))).toBe(false);
  const { owners, sessions } = store.get();
  const { top, closed } = splitOwners(ownerRows(owners.list, sessions));
  expect(top).toEqual([]);
  expect(closed.map((o) => o.id)).toEqual(['own_a']);
  expect(store.get().activeSession).not.toBe('oa');
});

test('a refused Close (409) leaves the owner open', async () => {
  closeStatus = 409;
  expect(await closeOwner(own)).toBe(false);
  expect(store.get().activeSession).toBe('oa');
  const { owners, sessions } = store.get();
  expect(splitOwners(ownerRows(owners.list, sessions)).closed).toEqual([]);
});

// A Resume whose answer is still on its way when the user closes the session
// must not select it again: the later act of the user wins.
test('a Resume answered after Close does not select the closed session again', async () => {
  let release;
  const held = new Promise((r) => { release = r; });
  const base = globalThis.fetch;
  globalThis.fetch = async (url, init = {}) => {
    if (String(url).endsWith('/resume')) {
      await held;
      return new Response(JSON.stringify({ id: 'oa', kind: 'owner', state: 'idle' }), { status: 200 });
    }
    return base(url, init);
  };
  setState({ activeSession: null, sessions: { oa: { id: 'oa', kind: 'owner', state: 'saved', updated: 1 } } });
  const pending = resumeSession('oa');
  expect(await closeOwner(own)).toBe(true);
  release();
  await pending;
  expect(store.get().activeSession).toBeNull();
});
