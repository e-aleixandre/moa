// owners.js — the project owners slice and its controller.
//
// An owner is one standing agent per codebase: it keeps the project's book,
// starts the sessions that work on it and reads what they report back
// (docs/owners.md). This module owns three things and nothing else:
//
//   1. the roster of owners (GET /api/owners) and whether it can be believed
//   2. the book of the owner whose dossier is open
//   3. the actions: create an owner, open its conversation, save PROJECT.md
//
// The CHILDREN of an owner are deliberately not fetched. Every session already
// carries `ownerId` in the store (SessionInfo.owner_id), so the dossier selects
// them out of the roster it already has and groups them with the same
// projection the session list uses. Two lists of the same sessions, taken at
// different instants, would disagree about which one is waiting.
//
// Same shape as data/events.js: thin helpers over setState, with health
// travelling WITH the list so a surface can never claim "no owners yet" after a
// load that never succeeded.

import { api } from './api.js';
import { store, setState, OWNERS_INITIAL } from './store.js';
import { addToast } from './notifications.js';
import { openSession } from './tile-actions.js';
import { loadSessions } from './session-actions.js';

export { OWNERS_INITIAL };

export function ownersSlice(state) {
  return state?.owners || OWNERS_INITIAL;
}

function patch(next) {
  setState((s) => ({ owners: { ...ownersSlice(s), ...next } }));
}

// ownersHealth is what the list is allowed to CLAIM about itself, the same
// three facts the inbox learned to carry: whether a load has ever succeeded,
// the last failure, and whether a retry is in flight.
export function ownersHealth(state) {
  const slice = ownersSlice(state);
  if (slice.error) return { status: 'error', error: slice.error, retrying: slice.retrying };
  if (!slice.loaded) return { status: 'loading' };
  return { status: 'ready' };
}

export async function loadOwners() {
  try {
    const list = await api('GET', '/api/owners');
    patch({ list: Array.isArray(list) ? list : [], loaded: true, error: null, retrying: false });
  } catch (e) {
    // The previous list keeps saying what is still true; only the health moves.
    patch({ error: String(e.message || e), retrying: false });
  }
}

export async function retryOwners() {
  patch({ retrying: true });
  await loadOwners();
}

// createOwner creates the entity, its book and its conversation, then opens
// that conversation. The roster is reloaded first: the new owner's session is
// an ordinary session as far as the store is concerned, and openSession only
// works on one it knows.
export async function createOwner({ root, name, model, thinking, avatar }) {
  const info = await api('POST', '/api/owners', { root, name, model, thinking, avatar });
  await loadOwners();
  await loadSessions();
  if (info?.session_id) openOwnerConversation(info);
  return info;
}

// updateOwner reloads both projections because an owner rename can also
// retitle its standing conversation, which is carried by the session list.
export async function updateOwner(id, { name, avatar }) {
  const info = await api('PATCH', `/api/owners/${id}`, { name, avatar });
  await loadOwners();
  await loadSessions();
  return info;
}

// openOwnerConversation opens the owner's own session in the pane. The backend
// creates that session with the owner (pkg/serve/owners.go), so there is no
// "create on first use" step: an owner without a session_id is an owner whose
// creation failed, and it says so rather than pretending to open something.
export function openOwnerConversation(own) {
  const id = own?.session_id;
  if (!id) {
    addToast({
      title: 'This owner has no conversation',
      detail: 'Delete it and create it again — its book stays on disk.',
      type: 'error',
    });
    return false;
  }
  if (!openSession(id)) {
    // The owner's conversation is hidden from GET /api/sessions, so the store
    // may not hold it yet. Ask for it explicitly, then open.
    loadOwnerSessions().then(() => openSession(id));
  }
  return true;
}

// loadOwnerSessions pulls the roster WITH the owner conversations, which the
// ordinary poll excludes. Only used when an owner's own session is about to be
// shown: the sidebar's lists must keep getting the roster without them.
export async function loadOwnerSessions() {
  try {
    const list = await api('GET', '/api/sessions?include=owners');
    const { normalizeSessionInfo } = await import('./session-actions.js');
    const state = store.get();
    const sessions = { ...state.sessions };
    for (const info of list) {
      if (sessions[info.id]) continue;
      sessions[info.id] = normalizeSessionInfo(info, undefined, new Set()).session;
    }
    setState({ sessions });
  } catch (e) {
    console.error('loadOwnerSessions failed:', e);
  }
}

/* ── The book ──────────────────────────────────────────────────────────── */

export async function loadOwnerBook(ownerId) {
  if (!ownerId) return;
  patch({ bookOwnerId: ownerId, bookStatus: 'loading', bookError: null, openPath: null, openBody: '' });
  try {
    const data = await api('GET', `/api/owners/${ownerId}/book`);
    // A newer owner may have been opened while this was in flight.
    if (ownersSlice(store.get()).bookOwnerId !== ownerId) return;
    patch({ bookStatus: 'ready', bookFiles: Array.isArray(data?.files) ? data.files : [] });
  } catch (e) {
    if (ownersSlice(store.get()).bookOwnerId !== ownerId) return;
    patch({ bookStatus: 'error', bookError: String(e.message || e) });
  }
}

export async function openBookFile(ownerId, path) {
  if (!path) {
    patch({ openPath: null, openBody: '', openStatus: 'idle', openEditable: false });
    return;
  }
  patch({ openPath: path, openBody: '', openStatus: 'loading', openEditable: false });
  try {
    const data = await api('GET', `/api/owners/${ownerId}/book/${encodeURI(path)}`);
    if (ownersSlice(store.get()).openPath !== path) return;
    patch({ openStatus: 'ready', openBody: data?.content || '', openEditable: !!data?.editable });
  } catch (e) {
    if (ownersSlice(store.get()).openPath !== path) return;
    patch({ openStatus: 'error', openBody: '', openEditable: false });
    addToast({ title: 'Could not read the file', detail: String(e.message || e), type: 'error' });
  }
}

// saveBookFile writes PROJECT.md. The server enforces which file is writable;
// a failure surfaces as a toast and the draft is left alone, so nothing typed
// is lost to a failed request.
export async function saveBookFile(ownerId, path, content) {
  await api('PUT', `/api/owners/${ownerId}/book/${encodeURI(path)}`, { path, content });
  patch({ openBody: content });
  // The size in the listing is now wrong; the listing is cheap.
  loadOwnerBook(ownerId).then(() => openBookFile(ownerId, path));
}
