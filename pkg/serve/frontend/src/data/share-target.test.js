import { expect, test } from 'bun:test';
import {
  appendSharedText, discardShare, isShareID, readShare,
  shareComposerText, shareIdFromLocation, SHARE_CACHE, SHARE_PREFIX,
} from './share-target.js';

// A Cache Storage double: enough of the API that readShare/discardShare
// exercise their real code (open → match → json/blob → keys → delete) rather
// than a rewritten version of it.
function fakeCaches(entries = {}) {
  const store = new Map(Object.entries(entries));
  const cache = {
    async match(key) {
      const value = store.get(String(key));
      return value === undefined ? undefined : response(value);
    },
    async keys() {
      return [...store.keys()].map((key) => ({ url: `https://moa.test${key}` }));
    },
    async delete(request) {
      const key = new URL(request.url).pathname;
      return store.delete(key);
    },
  };
  return { caches: { async open() { return cache; } }, store };
}

function response(value) {
  return {
    async json() { return JSON.parse(value.body); },
    async blob() { return { type: value.type || '', size: value.body.length }; },
  };
}

function meta(files) {
  return { body: JSON.stringify({ id: 'abc-def', at: Date.now(), title: '', text: '', url: '', files }) };
}

test('a share id is only accepted in the shape the worker mints', () => {
  expect(isShareID('m9x2k-ab12cd34')).toBe(true);
  expect(isShareID('../../etc/passwd')).toBe(false);
  expect(isShareID('abc/def')).toBe(false);
  expect(isShareID('')).toBe(false);
  expect(isShareID(undefined)).toBe(false);
});

test('a malformed ?share= is not treated as a share', () => {
  expect(shareIdFromLocation('?share=m9x2k-ab12cd34')).toBe('m9x2k-ab12cd34');
  expect(shareIdFromLocation('?share=..%2Fmeta')).toBe('');
  expect(shareIdFromLocation('?session=s1')).toBe('');
});

test('a shared link is not printed three times in the composer', () => {
  // Android hands the same URL as text AND url, with the page title on top.
  const text = shareComposerText({
    title: 'Web Share Target API',
    text: 'https://example.com/api',
    url: 'https://example.com/api',
  });
  expect(text).toBe('https://example.com/api\nWeb Share Target API');
});

test('shared text, url and title each survive when they differ', () => {
  expect(shareComposerText({ title: 'A post', text: 'look at this', url: 'https://x.test/p' }))
    .toBe('look at this\nhttps://x.test/p\nA post');
  expect(shareComposerText({})).toBe('');
});

test('a share never overwrites what is already in the composer', () => {
  expect(appendSharedText('review this', 'https://x.test')).toBe('review this\nhttps://x.test');
  expect(appendSharedText('review this\n', 'https://x.test')).toBe('review this\nhttps://x.test');
  expect(appendSharedText('', 'https://x.test')).toBe('https://x.test');
  expect(appendSharedText('review this', '')).toBe('review this');
});

test('a share with no metadata entry reads as nothing: the write never finished', async () => {
  const { caches } = fakeCaches({
    [`${SHARE_PREFIX}abc-def/file/0`]: { body: 'PDF', type: 'application/pdf' },
  });
  expect(await readShare('abc-def', { caches })).toBeNull();
});

test('the retained files come back as File objects with their own name and type', async () => {
  const { caches } = fakeCaches({
    [`${SHARE_PREFIX}abc-def/meta`]: meta([
      { key: `${SHARE_PREFIX}abc-def/file/0`, name: 'contract.odp', mime: 'application/vnd.oasis.opendocument.presentation', size: 3 },
    ]),
    [`${SHARE_PREFIX}abc-def/file/0`]: { body: 'ODP', type: 'application/vnd.oasis.opendocument.presentation' },
  });
  const share = await readShare('abc-def', { caches });
  expect(share.files).toHaveLength(1);
  expect(share.files[0].name).toBe('contract.odp');
  // No format allow-list anywhere in this path: whatever the phone reported is
  // what the composer receives.
  expect(share.files[0].type).toBe('application/vnd.oasis.opendocument.presentation');
});

test('a file the metadata lists but the cache lost is skipped, not faked', async () => {
  const { caches } = fakeCaches({
    [`${SHARE_PREFIX}abc-def/meta`]: meta([
      { key: `${SHARE_PREFIX}abc-def/file/0`, name: 'here.txt', mime: 'text/plain', size: 2 },
      { key: `${SHARE_PREFIX}abc-def/file/1`, name: 'gone.txt', mime: 'text/plain', size: 2 },
    ]),
    [`${SHARE_PREFIX}abc-def/file/0`]: { body: 'hi', type: 'text/plain' },
  });
  const share = await readShare('abc-def', { caches });
  expect(share.files.map((f) => f.name)).toEqual(['here.txt']);
});

test('discarding a share removes its files and its metadata, and nobody else’s', async () => {
  const { caches, store } = fakeCaches({
    [`${SHARE_PREFIX}abc-def/meta`]: meta([]),
    [`${SHARE_PREFIX}abc-def/file/0`]: { body: 'x' },
    [`${SHARE_PREFIX}zzz-999/meta`]: meta([]),
  });
  await discardShare('abc-def', { caches });
  expect([...store.keys()]).toEqual([`${SHARE_PREFIX}zzz-999/meta`]);
});

test('the cache name is the one the service worker writes to', () => {
  // sw.js holds these two by hand (it is copied verbatim into the bundle and
  // imports nothing). If one side is renamed, this is what notices.
  const worker = Bun.file(new URL('../sw.js', import.meta.url).pathname);
  return worker.text().then((source) => {
    expect(source).toContain(`const SHARE_CACHE = '${SHARE_CACHE}'`);
    expect(source).toContain(`const SHARE_PREFIX = '${SHARE_PREFIX}'`);
  });
});

// Sharing cannot depend on having enabled notifications. Until this test
// existed, the only code that ever registered /sw.js was enablePush, so on a
// profile that had never granted notifications there was no worker to
// intercept the share POST -- measured against a running server: the POST
// reached Go as a 404 and navigator.serviceWorker.ready never resolved.
test("installing share navigation registers the worker", async () => {
  const { installShareNavigation } = await import("./share.js");
  let registered = 0;
  const listeners = [];
  const fakeWorker = {
    addEventListener(type, fn) { listeners.push([type, fn]); },
    removeEventListener() {},
  };
  const stop = installShareNavigation({
    serviceWorker: fakeWorker,
    register: async () => { registered += 1; },
  });
  await new Promise((r) => setTimeout(r, 0));
  expect(registered).toBe(1);
  // And it still does its original job: listening for the warm handoff.
  expect(listeners.some(([type]) => type === "message")).toBe(true);
  stop();
});
