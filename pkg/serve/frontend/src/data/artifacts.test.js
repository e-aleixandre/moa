// artifacts.test.js — the drawer controller: ownership, races, refresh.
// Run with `bun test`.

import { test, expect, beforeEach, afterEach } from 'bun:test';
import { store, setState } from './store.js';
import { ARTIFACTS_CLOSED } from './artifacts-model.js';
import {
  artifactsSlice, closeArtifacts, closeArtifactsForMissingOwner, closeArtifactsForSession, loadArtifacts,
  openArtifactFromCard, openArtifactFromList, openArtifactsList,
  refreshArtifactsAfterDelivery, refreshArtifactsAfterReconnect, retryArtifacts,
} from './artifacts.js';

const originalFetch = globalThis.fetch;

function artifactsFor(sessionId, ids) {
  return {
    artifacts: ids.map((id) => ({
      id, name: `${id}.md`, mime: 'text/markdown', size: 10,
      url: `/api/sessions/${sessionId}/files/${id}`, created_at: '', updated_at: '', available: true,
    })),
  };
}

function serveImmediately(bySession) {
  const calls = [];
  globalThis.fetch = (path) => {
    calls.push(path);
    const sessionId = String(path).split('/')[3];
    return Promise.resolve(new Response(JSON.stringify(artifactsFor(sessionId, bySession[sessionId] || [])), { status: 200 }));
  };
  return calls;
}

// A deferred server: each request is resolved by hand, so a late answer can be
// delivered after the drawer already switched conversation.
function serveDeferred() {
  const pending = [];
  globalThis.fetch = (path) => new Promise((resolve) => { pending.push({ path, resolve }); });
  return pending;
}

beforeEach(() => {
  setState({ artifacts: ARTIFACTS_CLOSED, sessions: {}, isMobile: false });
});

afterEach(() => {
  globalThis.fetch = originalFetch;
  // The store is a singleton for the whole bun run: leaving isMobile true here
  // would hand the phone layout to unrelated test files.
  setState({ isMobile: false, activeSession: null });
});

test('opening an entry scopes the drawer to that conversation and fetches the list', async () => {
  const calls = serveImmediately({ A: ['a1', 'a2'] });
  openArtifactsList('A');
  expect(artifactsSlice(store.get()).ownerSessionId).toBe('A');
  expect(artifactsSlice(store.get()).status).toBe('loading');
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
  const slice = artifactsSlice(store.get());
  expect(slice.status).toBe('ready');
  expect(slice.items.map((a) => a.id)).toEqual(['a1', 'a2']);
  expect(calls).toEqual(['/api/sessions/A/artifacts']);
});

test('changing the focused session does not touch the drawer owner', async () => {
  serveImmediately({ A: ['a1'] });
  openArtifactsList('A');
  setState({ activeSession: 'B', isMobile: true });
  expect(artifactsSlice(store.get()).ownerSessionId).toBe('A');
});

test('a late response from A never paints B, and B still gets its own list', async () => {
  const pending = serveDeferred();
  openArtifactsList('A');
  openArtifactsList('B');
  expect(artifactsSlice(store.get()).ownerSessionId).toBe('B');
  // Switching owner clears the previous rows instead of showing them for B.
  expect(artifactsSlice(store.get()).items).toEqual([]);

  const [first, second] = pending;
  first.resolve(new Response(JSON.stringify(artifactsFor('A', ['a1'])), { status: 200 }));
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
  expect(artifactsSlice(store.get()).items).toEqual([]);

  second.resolve(new Response(JSON.stringify(artifactsFor('B', ['b1'])), { status: 200 }));
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
  const slice = artifactsSlice(store.get());
  expect(slice.ownerSessionId).toBe('B');
  expect(slice.items.map((a) => a.id)).toEqual(['b1']);
});

test('a failed list is reported and retry re-requests the same conversation', async () => {
  globalThis.fetch = () => Promise.resolve(new Response('nope', { status: 500 }));
  openArtifactsList('A');
  for (let i = 0; i < 5; i++) await Promise.resolve();
  expect(artifactsSlice(store.get()).status).toBe('error');

  const calls = serveImmediately({ A: ['a1'] });
  retryArtifacts();
  for (let i = 0; i < 5; i++) await Promise.resolve();
  expect(artifactsSlice(store.get()).status).toBe('ready');
  expect(artifactsSlice(store.get()).items.map((a) => a.id)).toEqual(['a1']);
  expect(calls).toEqual(['/api/sessions/A/artifacts']);
});

test('a card opens the reader on its own artifact and still loads the collection', async () => {
  const calls = serveImmediately({ A: ['a1'] });
  openArtifactFromCard('A', { name: 'a1.md', mime: 'text/markdown', size: 10, url: '/api/sessions/A/files/a1' });
  const slice = artifactsSlice(store.get());
  expect(slice.view).toBe('reader');
  expect(slice.fileId).toBe('a1');
  expect(slice.from).toBe('chat');
  expect(slice.seed.name).toBe('a1.md');
  expect(calls).toEqual(['/api/sessions/A/artifacts']);
});

test('a card whose URL is not an artifact URL does not open the drawer', () => {
  serveImmediately({});
  openArtifactFromCard('A', { name: 'x', url: 'https://evil.example/x' });
  expect(artifactsSlice(store.get()).view).toBeNull();
});

test('opening from the list records the origin so Back returns there', async () => {
  serveImmediately({ A: ['a1'] });
  openArtifactsList('A');
  for (let i = 0; i < 5; i++) await Promise.resolve();
  openArtifactFromList('a1');
  const slice = artifactsSlice(store.get());
  expect(slice.view).toBe('reader');
  expect(slice.from).toBe('list');
});

test('a delivery refreshes only the open drawer of the owning conversation', async () => {
  let calls = serveImmediately({ A: ['a1'] });
  openArtifactsList('A');
  for (let i = 0; i < 5; i++) await Promise.resolve();
  calls.length = 0;

  refreshArtifactsAfterDelivery('B', { url: '/api/sessions/B/files/b1' });
  expect(calls).toEqual([]);

  refreshArtifactsAfterDelivery('A', { url: '/api/sessions/A/files/a2' });
  expect(calls).toEqual(['/api/sessions/A/artifacts']);
});

test('a delivery with the drawer closed fetches nothing', () => {
  const calls = serveImmediately({ A: [] });
  refreshArtifactsAfterDelivery('A', { url: '/api/sessions/A/files/a1' });
  expect(calls).toEqual([]);
});

test('a reconnect reloads the open collection from the authoritative endpoint', async () => {
  const calls = serveImmediately({ A: ['a1'] });
  openArtifactsList('A');
  for (let i = 0; i < 5; i++) await Promise.resolve();
  calls.length = 0;

  refreshArtifactsAfterReconnect('B');
  expect(calls).toEqual([]);
  refreshArtifactsAfterReconnect('A');
  expect(calls).toEqual(['/api/sessions/A/artifacts']);
});

test('closing resets the slice but keeps the request token monotonic', async () => {
  const pending = serveDeferred();
  openArtifactsList('A');
  const tokenWhileOpen = artifactsSlice(store.get()).token;
  closeArtifacts();
  const closed = artifactsSlice(store.get());
  expect(closed.view).toBeNull();
  expect(closed.ownerSessionId).toBeNull();
  expect(closed.token).toBe(tokenWhileOpen);

  pending[0].resolve(new Response(JSON.stringify(artifactsFor('A', ['a1'])), { status: 200 }));
  for (let i = 0; i < 5; i++) await Promise.resolve();
  expect(artifactsSlice(store.get()).items).toEqual([]);
});

test('a conversation that disappears closes the drawer it owned', async () => {
  serveImmediately({ A: ['a1'] });
  openArtifactsList('A');
  closeArtifactsForSession('B');
  expect(artifactsSlice(store.get()).view).toBe('list');
  closeArtifactsForSession('A');
  expect(artifactsSlice(store.get()).view).toBeNull();
});

test('loadArtifacts ignores a call without a conversation', async () => {
  const calls = serveImmediately({});
  await loadArtifacts(null);
  expect(calls).toEqual([]);
});

// ── deletion observed through the authoritative roster ──────────────────────
// A delete performed in ANOTHER client/device reaches this one as an absence
// from the roster poll, not as a local action.

test('a roster without the owner closes the drawer it owned', async () => {
  serveImmediately({ A: ['a1'] });
  openArtifactsList('A');
  for (let i = 0; i < 5; i++) await Promise.resolve();

  closeArtifactsForMissingOwner(new Set(['B', 'C']));
  expect(artifactsSlice(store.get()).view).toBeNull();
  expect(artifactsSlice(store.get()).ownerSessionId).toBeNull();
});

test('a roster that still lists the owner leaves the drawer alone', async () => {
  serveImmediately({ A: ['a1'] });
  openArtifactsList('A');
  for (let i = 0; i < 5; i++) await Promise.resolve();

  // saved/closed conversations stay in the roster: only deletion removes them,
  // and an unloaded conversation still answers the artifacts API.
  closeArtifactsForMissingOwner(new Set(['A', 'B']));
  const slice = artifactsSlice(store.get());
  expect(slice.view).toBe('list');
  expect(slice.ownerSessionId).toBe('A');
  expect(slice.items.map((a) => a.id)).toEqual(['a1']);
});

test('a roster poll with the drawer closed does nothing', () => {
  const calls = serveImmediately({});
  closeArtifactsForMissingOwner(new Set([]));
  expect(artifactsSlice(store.get()).view).toBeNull();
  expect(calls).toEqual([]);
});
