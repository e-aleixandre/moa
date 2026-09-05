// artifacts-entry.test.js — the entries that own the shared drawer: each is
// bound to ITS conversation, clicking another pane's entry switches the same
// drawer, and a focus change never moves ownership. Run with `bun test`.

import { test, expect, beforeEach, afterEach } from 'bun:test';
import { store, setState } from '../../data/store.js';
import { ARTIFACTS_CLOSED, seedFromFile } from '../../data/artifacts-model.js';
import { artifactsSlice, openArtifactFromCard, openArtifactsList } from '../../data/artifacts.js';
import { artifactsEntryState } from './ArtifactsEntry.jsx';

const originalFetch = globalThis.fetch;
let calls;

beforeEach(() => {
  calls = [];
  globalThis.fetch = (path) => {
    calls.push(path);
    return Promise.resolve(new Response(JSON.stringify({ artifacts: [] }), { status: 200 }));
  };
  setState({ artifacts: ARTIFACTS_CLOSED, sessions: {}, isMobile: false, activeSession: null });
});

afterEach(() => {
  globalThis.fetch = originalFetch;
  // The store is a singleton for the whole bun run: leaving isMobile true here
  // would hand the phone layout to unrelated test files.
  setState({ isMobile: false, activeSession: null });
});

test('an entry without a conversation is not shown', () => {
  expect(artifactsEntryState(store.get(), null)).toEqual({ visible: false, active: false });
});

test('only the owning conversation entry reads as on', () => {
  openArtifactsList('A');
  expect(artifactsEntryState(store.get(), 'A').active).toBe(true);
  expect(artifactsEntryState(store.get(), 'B').active).toBe(false);
  expect(calls).toEqual(['/api/sessions/A/artifacts']);
});

test("opening another pane's entry switches the same drawer", () => {
  openArtifactsList('A');
  openArtifactsList('B');
  const slice = artifactsSlice(store.get());
  expect(slice.ownerSessionId).toBe('B');
  expect(slice.view).toBe('list');
  expect(artifactsEntryState(store.get(), 'A').active).toBe(false);
  expect(artifactsEntryState(store.get(), 'B').active).toBe(true);
  expect(calls).toEqual(['/api/sessions/A/artifacts', '/api/sessions/B/artifacts']);
});

test('a focus change leaves the drawer and its entry states untouched', () => {
  openArtifactsList('A');
  setState({ isMobile: true, activeSession: 'B' });
  expect(artifactsSlice(store.get()).ownerSessionId).toBe('A');
  expect(artifactsEntryState(store.get(), 'A').active).toBe(true);
  expect(artifactsEntryState(store.get(), 'B').active).toBe(false);
});

test('a send_file card opens the reader on its own artifact, in its conversation', () => {
  openArtifactFromCard('A', { name: 'report.md', mime: 'text/markdown', size: 12, url: '/api/sessions/A/files/f1' });
  const slice = artifactsSlice(store.get());
  expect(slice.ownerSessionId).toBe('A');
  expect(slice.view).toBe('reader');
  expect(slice.fileId).toBe('f1');
  expect(artifactsEntryState(store.get(), 'A').active).toBe(true);
});

test('an image card is an artifact too', () => {
  openArtifactFromCard('A', { name: 'shot.png', mime: 'image/png', size: 4096, url: '/api/sessions/A/files/f2' });
  expect(artifactsSlice(store.get()).fileId).toBe('f2');
});

test('a descriptor that is not an artifact URL is not an artifact card', () => {
  expect(seedFromFile({ name: 'x.txt', url: '/api/other/x' })).toBeNull();
  openArtifactFromCard('A', { name: 'x.txt', url: '/api/other/x' });
  expect(artifactsSlice(store.get()).view).toBeNull();
});
