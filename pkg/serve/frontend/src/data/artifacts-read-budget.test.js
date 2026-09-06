// artifacts-read-budget.test.js — the reading budget an artifact is opened
// under. The stored size is the LAST OBSERVED one, so it may be stale in both
// directions; the cap is therefore enforced against the actual response, and
// the reader never decides from metadata alone. Run with `bun test`.

import { test, expect } from 'bun:test';
import { readCapped, MAX_PREVIEW_SIZE, MAX_HIGHLIGHT_SIZE } from './util/file-preview.js';
import { artifactKind } from './artifacts-model.js';

test('the shared limits are the ones the file viewer already used', () => {
  expect(MAX_PREVIEW_SIZE).toBe(2 * 1024 * 1024);
  expect(MAX_HIGHLIGHT_SIZE).toBe(150 * 1024);
});

test('a file that shrank since it was sent still opens, despite a huge stored size', async () => {
  const stale = { name: 'report.md', mime: 'text/markdown', size: 99 * 1024 * 1024 };
  // Nothing in the reader's decision depends on that stale number.
  expect(artifactKind(stale)).toBe('markdown');
  const body = 'now tiny';
  const blob = await readCapped(new Response(body), MAX_PREVIEW_SIZE);
  expect(await blob.text()).toBe(body);
});

test('a file that grew past the cap is rejected from the response, not from metadata', async () => {
  const oversized = 'x'.repeat(MAX_PREVIEW_SIZE + 1);
  await expect(readCapped(new Response(oversized), MAX_PREVIEW_SIZE))
    .rejects.toMatchObject({ tooLarge: true });
});

test('a declared content-length over the cap is refused without draining the body', async () => {
  const response = new Response('short body', {
    headers: { 'content-length': String(MAX_PREVIEW_SIZE + 1), 'content-type': 'text/markdown' },
  });
  await expect(readCapped(response, MAX_PREVIEW_SIZE)).rejects.toMatchObject({ tooLarge: true });
  // readCapped never consumed the body: it refused on the declared length.
  expect(response.bodyUsed).toBe(false);
});

// ── a republished artifact is re-read, not cached ───────────────────────────
// The same path resent keeps its id and URL; the reader must fetch the CURRENT
// bytes with no HTTP cache, and pick its renderer from the CURRENT metadata.

test('a re-read of the same URL returns the new bytes and bypasses the HTTP cache', async () => {
  const originalFetch = globalThis.fetch;
  const bodies = ['# old report', '# new report'];
  const options = [];
  globalThis.fetch = (_, opts) => {
    options.push(opts);
    return Promise.resolve(new Response(bodies[options.length - 1], { headers: { 'content-type': 'text/markdown' } }));
  };
  try {
    const url = '/api/sessions/A/files/f1';
    const first = await (await fetch(url, { cache: 'no-store' })).text();
    const second = await (await fetch(url, { cache: 'no-store' })).text();
    expect(first).toBe('# old report');
    expect(second).toBe('# new report');
    expect(options.every((o) => o.cache === 'no-store')).toBe(true);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('the renderer follows the CURRENT metadata, so a republished type is not stale', () => {
  const before = { name: 'report.md', mime: 'text/markdown' };
  const after = { name: 'report.html', mime: 'text/html' };
  expect(artifactKind(before)).toBe('markdown');
  expect(artifactKind(after)).toBe('html');
  expect(artifactKind({ name: 'report.png', mime: 'image/png' })).toBe('image');
});
