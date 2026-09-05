// artifacts-model.test.js — run with `bun test`

import { test, expect } from 'bun:test';
import {
  ARTIFACTS_CLOSED, acceptsResponse, artifactFailure, artifactFileId, artifactKind, artifactSessionId,
  currentArtifact, filterArtifacts, isHtmlArtifact, normalizeArtifacts, seedFromFile,
} from './artifacts-model.js';

const payload = {
  artifacts: [
    {
      id: 'aaa', name: 'Report.md', title: 'Final report', description: 'What was asked for',
      mime: 'text/markdown; charset=utf-8', size: 1234, url: '/api/sessions/s1/files/aaa',
      created_at: '2026-09-01T10:00:00Z', updated_at: '2026-09-02T10:00:00Z', available: true,
    },
    {
      id: 'bbb', name: 'diagram.html', mime: 'text/html', size: 900,
      url: '/api/sessions/s1/files/bbb', created_at: '', updated_at: '', available: false,
    },
  ],
};

test('normalizeArtifacts keeps the server order and maps the DTO', () => {
  const items = normalizeArtifacts(payload);
  expect(items.map((a) => a.id)).toEqual(['aaa', 'bbb']);
  expect(items[0].title).toBe('Final report');
  expect(items[0].description).toBe('What was asked for');
  expect(items[0].updatedAt).toBe('2026-09-02T10:00:00Z');
  expect(items[1].available).toBe(false);
});

test('a missing title falls back to the file name', () => {
  const [, html] = normalizeArtifacts(payload);
  expect(html.title).toBe('diagram.html');
  expect(html.description).toBe('');
});

test('normalizeArtifacts drops entries without a trustworthy artifact URL', () => {
  const items = normalizeArtifacts({
    artifacts: [
      { id: 'x', name: 'x', url: 'https://evil.example/x' },
      { id: '', name: 'y', url: '/api/sessions/s1/files/' },
      null,
      { id: 'ok', name: 'ok', url: '/api/sessions/s1/files/ok' },
    ],
  });
  expect(items.map((a) => a.id)).toEqual(['ok']);
});

test('normalizeArtifacts tolerates an empty or malformed payload', () => {
  expect(normalizeArtifacts(null)).toEqual([]);
  expect(normalizeArtifacts({})).toEqual([]);
  expect(normalizeArtifacts({ artifacts: 'nope' })).toEqual([]);
});

test('artifactFileId and artifactSessionId only accept our own URL shape', () => {
  expect(artifactFileId('/api/sessions/s1/files/abc')).toBe('abc');
  expect(artifactSessionId('/api/sessions/s1/files/abc')).toBe('s1');
  expect(artifactFileId('/api/sessions/s1/files')).toBeNull();
  expect(artifactFileId('https://evil.example/api/sessions/s1/files/abc')).toBeNull();
  expect(artifactFileId(undefined)).toBeNull();
});

test('search matches title, file name and description; blank shows everything', () => {
  const items = normalizeArtifacts(payload);
  expect(filterArtifacts(items, 'diagram').map((a) => a.id)).toEqual(['bbb']);
  expect(filterArtifacts(items, 'FINAL').map((a) => a.id)).toEqual(['aaa']);
  expect(filterArtifacts(items, 'asked').map((a) => a.id)).toEqual(['aaa']);
  expect(filterArtifacts(items, '   ')).toBe(items);
  expect(filterArtifacts(items, 'nothing')).toEqual([]);
});

test('the reader is chosen by type, never by the stored size', () => {
  const [md, html] = normalizeArtifacts(payload);
  expect(artifactKind(md)).toBe('markdown');
  expect(artifactKind(html)).toBe('html');
  expect(isHtmlArtifact(html)).toBe(true);
  expect(artifactKind({ name: 'shot.png', mime: 'image/png', size: 99 * 1024 * 1024 })).toBe('image');
  expect(artifactKind({ name: 'archive.tar.gz', mime: 'application/gzip' })).toBe('text');
});

test('currentArtifact prefers the authoritative entry over the card seed', () => {
  const items = normalizeArtifacts(payload);
  const seed = seedFromFile({ name: 'stale.md', mime: 'text/markdown', size: 1, url: '/api/sessions/s1/files/aaa' });
  const slice = { ...ARTIFACTS_CLOSED, view: 'reader', fileId: 'aaa', items, seed };
  expect(currentArtifact(slice).name).toBe('Report.md');
  expect(currentArtifact({ ...slice, items: [] }).name).toBe('stale.md');
  expect(currentArtifact({ ...slice, view: 'list' })).toBeNull();
});

test('seedFromFile carries optional send_file metadata and falls back to the name', () => {
  const url = '/api/sessions/s1/files/aaa';
  expect(seedFromFile({ name: 'a.md', url, title: ' Titled ' }).title).toBe('Titled');
  expect(seedFromFile({ name: 'a.md', url }).title).toBe('a.md');
  expect(seedFromFile({ name: 'a.md', url: 'https://evil.example/x' })).toBeNull();
});

test('a response is accepted only for the current owner and the newest token', () => {
  const slice = { ...ARTIFACTS_CLOSED, view: 'list', ownerSessionId: 'A', token: 4 };
  expect(acceptsResponse(slice, { sessionId: 'A', token: 4 })).toBe(true);
  expect(acceptsResponse(slice, { sessionId: 'A', token: 3 })).toBe(false);
  expect(acceptsResponse(slice, { sessionId: 'B', token: 4 })).toBe(false);
  expect(acceptsResponse({ ...slice, view: null }, { sessionId: 'A', token: 4 })).toBe(false);
});

// ── recovery actions on an unreadable artifact ──────────────────────────────
// The contract is that a recoverable failure offers a way forward, and the copy
// says what to do next rather than describing internals.

test('a 410 is recoverable: it asks for the file back and offers Retry', () => {
  const failure = artifactFailure('unavailable');
  expect(failure.retryable).toBe(true);
  expect(failure.message).toBe('The original file is not at its location right now. Ask the agent to restore the file, then retry.');
  // Nothing to share: the bytes are not reachable.
  expect(failure.shareable).toBe(false);
});

test('a 404 asks for a fresh delivery and promises no legacy ID recovery', () => {
  const failure = artifactFailure('missing');
  expect(failure.retryable).toBe(false);
  expect(failure.message).toBe('This artifact is not in this conversation any more. Ask the agent to send it again.');
  expect(failure.message).not.toContain('restore');
});

test('a transport error stays retryable and keeps the download action', () => {
  expect(artifactFailure('error')).toEqual({
    message: 'Could not open this artifact.', retryable: true, shareable: true,
  });
});

test('too large and binary are not retried, but the file can still be taken away', () => {
  for (const kind of ['too-large', 'binary']) {
    const failure = artifactFailure(kind);
    expect(failure.retryable).toBe(false);
    expect(failure.shareable).toBe(true);
  }
});
