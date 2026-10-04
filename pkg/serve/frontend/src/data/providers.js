// providers.js — the provider credential STATUS the whole app reads (badge,
// session error actions) and the controller that opens Settings → Providers.
//
// Its own tiny pub/sub rather than a slice of store.js: the store persists to
// localStorage, and nothing about credentials — not even the sanitized status —
// needs to outlive the page. Only GET /api/providers/status lands here: state,
// kind, source and counts, never an account, a key hint or a token. Keys and
// pasted sign-in values never leave the component that collects them.

import { useEffect, useState } from 'preact/hooks';
import { api } from './api.js';
import { providerToastKey } from './provider-error.js';

const EMPTY = Object.freeze({
  loaded: false,
  // A failed read keeps the last known reading and says it is stale, rather
  // than announcing zero problems it could not check.
  stale: false,
  canAdmin: null, // null until the first read: unknown is not "device"
  attentionCount: 0,
  providers: [],
});

let status = EMPTY;
const statusListeners = new Set();
let inflight = null;

export function getProviderStatus() { return status; }

export function subscribeProviderStatus(fn) {
  statusListeners.add(fn);
  fn(status);
  return () => statusListeners.delete(fn);
}

function emitStatus(next) {
  status = next;
  statusListeners.forEach((fn) => fn(status));
}

// applyProviderStatus — adopt one /api/providers/status (or owner list) body.
// Exported for the page, which reads the owner list and must not leave the
// badge behind it.
export function applyProviderStatus(body) {
  if (!body || typeof body !== 'object' || !Array.isArray(body.providers)) return;
  emitStatus({
    loaded: true,
    stale: false,
    canAdmin: !!body.can_admin,
    attentionCount: Number.isFinite(body.attention_count) ? body.attention_count : 0,
    providers: body.providers,
  });
}

// loadProviderStatus — one read, coalesced: the roster tick, the foreground
// and a burst of failing sessions may all ask at once.
export function loadProviderStatus({ request = api } = {}) {
  if (inflight) return inflight;
  inflight = request('GET', '/api/providers/status', null, { cache: 'no-store' })
    .then((body) => { applyProviderStatus(body); })
    .catch(() => {
      if (status.loaded) emitStatus({ ...status, stale: true });
    })
    .finally(() => { inflight = null; });
  return inflight;
}

export function useProviderStatus() {
  const [value, setValue] = useState(status);
  useEffect(() => subscribeProviderStatus(setValue), []);
  return value;
}

// attentionLabel — the badge's words. "Needs attention", not "needs sign-in":
// a failed save or a broken credential file count too.
export function attentionLabel(count) {
  if (!count) return '';
  return count === 1 ? '1 provider needs attention' : `${count} providers need attention`;
}

// claimProviderToast — true the first time a credential failure is seen, false
// after. A revoked key fails every session that uses it; the first one says
// so, and the inline action and the badge carry the rest. Errors that are not
// credential failures (no detail) are always toasted as before.
const toastedProviderFailures = new Set();

export function claimProviderToast(detail) {
  const key = providerToastKey(detail);
  if (!key) return true;
  if (toastedProviderFailures.has(key)) return false;
  toastedProviderFailures.add(key);
  return true;
}

// ── Open controller ─────────────────────────────────────────────────────────
// Like pulse-pairing-panel.js: the hosts (DesktopShell, MobileSessionChrome)
// own the Settings sheet; anyone may ask them to open it on Providers with one
// provider in focus. Carries IDs only, never anything a sign-in produced.

let openRequest = null;
let openSeq = 0;
const openListeners = new Set();

export function subscribeProviderSettings(fn) {
  openListeners.add(fn);
  if (openRequest) fn(openRequest);
  return () => openListeners.delete(fn);
}

export function openProviderSettings(provider = '', returnSessionId = '') {
  openRequest = {
    seq: ++openSeq,
    provider: typeof provider === 'string' ? provider : '',
    returnSessionId: typeof returnSessionId === 'string' ? returnSessionId : '',
  };
  openListeners.forEach((fn) => fn(openRequest));
}

// consumeProviderSettings — a host that has acted on the request clears it so
// a later mount does not reopen Settings by itself.
export function consumeProviderSettings(seq) {
  if (openRequest && openRequest.seq === seq) openRequest = null;
}

// Test seam: back to a first load.
export function resetProviderStatusForTest() {
  status = EMPTY;
  inflight = null;
  openRequest = null;
  toastedProviderFailures.clear();
  statusListeners.clear();
  openListeners.clear();
}
