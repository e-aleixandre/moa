// remembered-secrets.js — the secrets the owner chose to remember, on the
// client: one store slice with what GET /api/remembered-secrets says, the
// Forget actions, and the refresh that keeps it current.
//
// Only metadata lives here (which aliases, until when, the uses). A value
// never does: it travels once, in the secret card's POST body, like any
// answer.
//
// Freshness: the server is the authority and nothing is pushed. The slice is
// reloaded on start, when the app becomes visible, every minute while it is
// visible and after the owner's own actions (answering a card, Forget). Each
// deadline is known, so an entry hides itself at its expiresAt without
// asking. // a read that overlapped a Forget is dropped (see Ordering below).

import { api } from './api.js';
import { store, setState } from './store.js';

export const REMEMBERED_INITIAL = Object.freeze({ entries: [], uses: [], loaded: false, pending: [] });

const REFRESH_MS = 60_000;
const BASE = '/api/remembered-secrets';

export function rememberedSlice(state) {
  return state?.rememberedSecrets || REMEMBERED_INITIAL;
}

const str = (v) => (typeof v === 'string' ? v : '');

// normalizeRemembered copies what the strip and the sheet read. Anything else
// on the wire is dropped.
export function normalizeRemembered(data) {
  const entries = Array.isArray(data?.entries) ? data.entries : [];
  const uses = Array.isArray(data?.uses) ? data.uses : [];
  return {
    loaded: true,
    usesDropped: Number.isInteger(data?.usesDropped) ? data.usesDropped : 0,
    entries: entries.map((e) => ({
      alias: str(e?.alias),
      kind: str(e?.kind),
      rememberedAt: str(e?.rememberedAt),
      expiresAt: str(e?.expiresAt),
      uses: Number.isInteger(e?.uses) ? e.uses : 0,
    })).filter((e) => e.alias && e.expiresAt),
    uses: uses.map((u) => ({
      at: str(u?.at),
      kind: u?.kind === 'remember' ? 'remember' : 'use',
      aliases: Array.isArray(u?.keys) ? [...new Set(u.keys.map((k) => str(k?.alias)).filter(Boolean))] : [],
      sessionId: str(u?.sessionId),
      sessionTitle: str(u?.sessionTitle),
      requester: str(u?.requester),
      command: str(u?.command),
    })),
  };
}

// liveEntries — the entries still before their deadline at `now`.
export function liveEntries(slice, now = Date.now()) {
  return (slice?.entries || []).filter((e) => {
    const until = Date.parse(e.expiresAt);
    return Number.isFinite(until) && now < until;
  });
}

// rememberedAliases — one row per alias (the owner forgets by alias), with its
// latest deadline and its uses.
export function rememberedAliases(slice, now = Date.now()) {
  const byAlias = new Map();
  for (const e of liveEntries(slice, now)) {
    const row = byAlias.get(e.alias) || { alias: e.alias, expiresAt: e.expiresAt, uses: 0 };
    if (Date.parse(e.expiresAt) > Date.parse(row.expiresAt)) row.expiresAt = e.expiresAt;
    row.uses = Math.max(row.uses, e.uses);
    byAlias.set(e.alias, row);
  }
  return [...byAlias.values()].sort((a, b) => a.alias.localeCompare(b.alias));
}

// lastDeadline — when nothing will be remembered any more.
export function lastDeadline(rows) {
  let last = '';
  for (const r of rows) if (!last || Date.parse(r.expiresAt) > Date.parse(last)) last = r.expiresAt;
  return last;
}

// rememberedRuns — the runs answered from memory, newest first.
export function rememberedRuns(slice) {
  return (slice?.uses || []).filter((u) => u.kind === 'use').slice().reverse();
}

// Ordering. A read only counts if no Forget was in flight while it ran:
// `mutations` changes when one starts and when it ends, and a read applies
// its answer only if the counter is the same as when it started and nothing
// is pending. Forgets run one after another; each applies the status its
// DELETE answers with.
let reads = 0;
let readApplied = 0;
let mutations = 0;
let inFlight = 0;
let queue = Promise.resolve();

function setPending(pending) {
  setState({ rememberedSecrets: { ...rememberedSlice(store.get()), pending } });
}

export function loadRemembered() {
  const mySeq = ++reads;
  const myMutations = mutations;
  return api('GET', BASE, undefined, { cache: 'no-store' })
    .then((data) => {
      if (inFlight > 0 || mutations !== myMutations || mySeq < readApplied) return;
      readApplied = mySeq;
      setState({ rememberedSecrets: { ...normalizeRemembered(data), pending: rememberedSlice(store.get()).pending } });
    })
    .catch(() => {});
}

// forgetRemembered forgets one alias, or everything without one. Nothing
// leaves the screen until the server confirms: meanwhile the entries show as
// being forgotten (`pending`), and a failure leaves them as they were, so
// Forget is still there to try again. Rejects on failure.
export function forgetRemembered(alias = '') {
  const pending = rememberedSlice(store.get()).pending || [];
  setPending([...pending, alias || '*']);
  mutations++;
  inFlight++;
  const run = queue.then(async () => {
    try {
      const data = await api('DELETE', alias ? `${BASE}?alias=${encodeURIComponent(alias)}` : BASE);
      setState({ rememberedSecrets: { ...normalizeRemembered(data), pending: rememberedSlice(store.get()).pending } });
    } finally {
      mutations++;
      inFlight--;
      const left = [...(rememberedSlice(store.get()).pending || [])];
      left.splice(left.indexOf(alias || '*'), 1);
      setPending(left);
    }
  });
  queue = run.catch(() => {});
  return run;
}

// isForgetting — whether a Forget covering `alias` is waiting for the server.
export function isForgetting(slice, alias) {
  const p = slice?.pending || [];
  return p.includes('*') || (alias ? p.includes(alias) : false);
}

// closeSheetIfEmpty drops the intent to show the sheet once nothing is
// remembered (Forget, expiry, another device), so it cannot reopen by itself
// when something is remembered again later.
export function closeSheetIfEmpty(state = store.get()) {
  if (state?.rememberedSheetOpen && liveEntries(rememberedSlice(state)).length === 0) {
    setState({ rememberedSheetOpen: false });
  }
}

let started = false;
let timer = null;
let expiryTimer = null;

function scheduleExpiry() {
  clearTimeout(expiryTimer);
  const now = Date.now();
  const next = liveEntries(rememberedSlice(store.get()), now)
    .map((e) => Date.parse(e.expiresAt))
    .reduce((a, b) => Math.min(a, b), Infinity);
  if (!Number.isFinite(next)) return;
  // Re-render at the next deadline so that entry leaves the screen.
  expiryTimer = setTimeout(() => setState({ rememberedSecrets: { ...rememberedSlice(store.get()) } }), Math.min(next - now + 50, 2 ** 31 - 1));
}

// startRememberedSecrets begins keeping the slice fresh. Idempotent.
export function startRememberedSecrets(win = typeof window !== 'undefined' ? window : null) {
  if (started || !win) return;
  started = true;
  const doc = win.document;
  const visible = () => !doc || doc.visibilityState !== 'hidden';
  loadRemembered();
  timer = setInterval(() => { if (visible()) loadRemembered(); }, REFRESH_MS);
  doc?.addEventListener?.('visibilitychange', () => { if (visible()) loadRemembered(); });
  let last = rememberedSlice(store.get());
  store.subscribe(() => {
    const next = rememberedSlice(store.get());
    if (next !== last) {
      last = next;
      scheduleExpiry();
      closeSheetIfEmpty();
    }
  });
}

// Test hook.
export function _resetRememberedForTest() {
  reads = 0;
  readApplied = 0;
  mutations = 0;
  inFlight = 0;
  queue = Promise.resolve();
  started = false;
  clearInterval(timer);
  clearTimeout(expiryTimer);
}
