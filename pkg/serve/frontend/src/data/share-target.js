// share-target.js — reading what the service worker retained for a share.
//
// The share target (manifest.webmanifest `share_target`) is a POST the OS makes
// to /share-target; sw.js answers it, puts the shared files and text in the
// Cache Storage and redirects here. This module is the READER of that handoff,
// and the only place that knows its shape. The two constants below are the
// contract with sw.js: the worker is copied verbatim into the bundle and shares
// no module with the app, so they exist twice on purpose — change one, change
// the other.

export const SHARE_CACHE = 'moa-share-v1';
export const SHARE_PREFIX = '/__moa-share__/';

// A share id is minted by the worker (`<base36 time>-<random>`). It travels
// through a URL and a postMessage, so it is validated before it is used to
// build a cache key — the same reason isPushSessionID exists.
export function isShareID(value) {
  return typeof value === 'string' && /^[a-z0-9]{1,16}-[a-z0-9]{1,16}$/.test(value);
}

// shareIdFromLocation reads the ?share= a cold start carries (the worker's 303
// redirect). Returns '' when there is none or it is malformed.
export function shareIdFromLocation(search = typeof location === 'undefined' ? '' : location.search) {
  try {
    const value = new URLSearchParams(search).get('share') || '';
    return isShareID(value) ? value : '';
  } catch (_) {
    return '';
  }
}

// shareComposerText turns the text fields of a share into the one text the
// composer receives. Android hands a shared link as `text`, `url`, or both, and
// a title that is usually the page's — so the URL is only appended when the
// text does not already contain it, and a title that repeats either is dropped.
// The owner is going to type an instruction next to this; it must not arrive
// with the same link printed three times.
export function shareComposerText({ title = '', text = '', url = '' } = {}) {
  const lines = [];
  const push = (value) => {
    const trimmed = String(value || '').trim();
    if (!trimmed) return;
    if (lines.some((line) => line.includes(trimmed))) return;
    lines.push(trimmed);
  };
  push(text);
  push(url);
  push(title);
  return lines.join('\n');
}

// appendSharedText merges the shared text into whatever the composer already
// holds. A share never overwrites a draft: the owner may have typed the
// instruction first and shared the link second. Returns the existing value
// unchanged when there is nothing to add.
export function appendSharedText(existing = '', shared = '') {
  if (!shared) return existing;
  if (!existing) return shared;
  return /\n$/.test(existing) ? existing + shared : `${existing}\n${shared}`;
}

function cacheStorage(caches = globalThis.caches) {
  return caches && typeof caches.open === 'function' ? caches : null;
}

// readShare returns the retained share as { id, title, text, url, files: File[] }
// or null when nothing (complete) is stored under that id. The worker writes the
// metadata entry LAST, so its absence means the share never finished landing and
// there is nothing to show the owner.
export async function readShare(shareId, { caches = globalThis.caches } = {}) {
  if (!isShareID(shareId)) return null;
  const storage = cacheStorage(caches);
  if (!storage) return null;
  try {
    const cache = await storage.open(SHARE_CACHE);
    const metaResponse = await cache.match(`${SHARE_PREFIX}${shareId}/meta`);
    if (!metaResponse) return null;
    const meta = await metaResponse.json();
    const files = [];
    for (const entry of Array.isArray(meta.files) ? meta.files : []) {
      const response = await cache.match(entry.key);
      // A file listed by the metadata but missing from the cache is skipped
      // rather than faked: the rest of the share is still deliverable, and a
      // zero-byte attachment would look like a file the agent can read.
      if (!response) continue;
      const blob = await response.blob();
      const mime = entry.mime || blob.type || 'application/octet-stream';
      files.push(new File([blob], entry.name || 'shared-file', { type: mime }));
    }
    return {
      id: shareId,
      title: typeof meta.title === 'string' ? meta.title : '',
      text: typeof meta.text === 'string' ? meta.text : '',
      url: typeof meta.url === 'string' ? meta.url : '',
      files,
    };
  } catch (_) {
    return null;
  }
}

// discardShare removes every entry of a share. Called once it has landed in a
// composer and when the owner dismisses the picker: a shared video must not
// stay in the cache because a share sheet was opened by accident.
export async function discardShare(shareId, { caches = globalThis.caches } = {}) {
  if (!isShareID(shareId)) return;
  const storage = cacheStorage(caches);
  if (!storage) return;
  try {
    const cache = await storage.open(SHARE_CACHE);
    const base = `${SHARE_PREFIX}${shareId}`;
    for (const request of await cache.keys()) {
      let pathname = '';
      try { pathname = new URL(request.url).pathname; } catch (_) { continue; }
      if (pathname === `${base}/meta` || pathname.startsWith(`${base}/file/`)) {
        await cache.delete(request);
      }
    }
  } catch (_) { /* the worker sweeps stale shares on its next activate */ }
}
