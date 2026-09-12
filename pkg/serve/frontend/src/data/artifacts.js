// artifacts.js — the Artifacts drawer controller. One ephemeral, global slice
// in the app store (state.artifacts): a single right-hand drawer whose owner is
// whichever conversation's entry was clicked. There is no parallel bus and no
// per-pane viewer state; the list itself is never stored inside session
// metadata, because it is derived server state, refetched on open.

import { store, setState } from './store.js';
import { api } from './api.js';
import { closeSessionPanel } from './session-panel.js';
import {
  ARTIFACTS_CLOSED, EMPTY_ARTIFACTS, acceptsResponse, artifactFileId,
  normalizeArtifacts, seedFromFile,
} from './artifacts-model.js';

// artifactsOrigin — the conversation the drawer belongs to, as a stable
// snapshot: a plain string, so subscribing to it does not re-render on every
// streamed token. Derived from the OWNER, never from the focused session.
export function artifactsOrigin(state) {
  const slice = state?.artifacts || ARTIFACTS_CLOSED;
  const id = slice.ownerSessionId;
  if (!id) return '';
  const title = (state?.sessions?.[id]?.title || '').trim();
  return title || 'Untitled';
}

export function artifactsSlice(state) {
  return state?.artifacts || ARTIFACTS_CLOSED;
}

function patch(next) {
  setState((s) => ({ artifacts: { ...artifactsSlice(s), ...next } }));
}

// The element that opened the drawer, so closing restores focus where the user
// left it (the head entry, the pane button, or the stream card).
let lastTrigger = null;

function rememberTrigger() {
  if (typeof document !== 'undefined') lastTrigger = document.activeElement;
}

export function restoreArtifactsFocus() {
  if (typeof document === 'undefined') return;
  if (lastTrigger && lastTrigger.isConnected) {
    lastTrigger.focus?.({ preventScroll: true });
    return;
  }
  // The mobile entry is a menu item that unmounts with its menu, so fall back
  // to the permanent control that owns it (the session actions button) rather
  // than dropping focus to <body>.
  const fallback = document.querySelector('.mconv-action-rail-button')
    || document.querySelector('[data-artifacts-trigger="true"]');
  fallback?.focus?.({ preventScroll: true });
}

// loadArtifacts fetches the authoritative collection. Every open/switch calls
// it: the API is the source of truth, so a reconnect or a missed event can
// never leave a stale list on screen. The response is applied only when it is
// still the newest request for the conversation that owns the drawer.
export async function loadArtifacts(sessionId, { token } = {}) {
  if (!sessionId) return;
  const requestToken = token ?? artifactsSlice(store.get()).token;
  try {
    const payload = await api('GET', `/api/sessions/${encodeURIComponent(sessionId)}/artifacts`, undefined, { cache: 'no-store' });
    const items = normalizeArtifacts(payload);
    if (!acceptsResponse(artifactsSlice(store.get()), { sessionId, token: requestToken })) return;
    patch({ status: 'ready', error: null, items });
  } catch (error) {
    if (!acceptsResponse(artifactsSlice(store.get()), { sessionId, token: requestToken })) return;
    patch({ status: 'error', error: String(error?.message || error) });
  }
}

// ONE right-hand surface at a time. The dossier and the reader are both the
// right-hand side of the screen, and the reader (600px, 1100 expanded) is wider
// than the dossier (340), so an open reader covers the dossier completely.
//
// While the dossier was a drawer that cost nothing, being covered was
// harmless. As the shell's third zone it is not: the covered dossier still
// TAKES ITS COLUMN, so the centre pays 340px for a zone nobody can see — the
// transcript is squeezed to make room for something invisible. Measured at
// 1600: centre 272..1260, dossier 1260..1600, reader 1000..1600.
//
// So every door into the reader closes the dossier first. This is the rule the
// panel's own Artifacts page already kept ("the reader never lives in 340px");
// it lived on that one door because covering used to be free. It belongs here,
// on the funnel every door goes through — head entry, pane button, send_file
// card and the panel's own list — rather than repeated on each of them.
function beginRequest(sessionId, next) {
  closeSessionPanel();
  return claimRequest(sessionId, next);
}

// claimRequest is beginRequest without the panel-closing: taking ownership and
// a fresh token is bookkeeping, while closing the dossier is the drawer's
// entry behaviour (two right-hand surfaces must never stack). The dossier's
// own artifacts page needs the first and must not do the second -- it IS the
// panel.
function claimRequest(sessionId, next) {
  const previous = artifactsSlice(store.get());
  const token = previous.token + 1;
  const sameOwner = previous.ownerSessionId === sessionId;
  patch({
    ownerSessionId: sessionId,
    token,
    status: 'loading',
    error: null,
    // Switching conversation must not show the previous owner's rows even for
    // one frame; reopening the same one keeps them so the list doesn't blink.
    items: sameOwner ? previous.items : EMPTY_ARTIFACTS,
    ...next,
  });
  return token;
}

// listArtifactsInPanel — the dossier's own page needs the collection WITHOUT
// opening the drawer over it. It was calling loadArtifacts() directly, which
// looks right and silently never worked: acceptsResponse requires slice.view
// to be set, and the panel never sets it, so every 200 was dropped on arrival
// and the page said "no files in this conversation yet" while the API was
// returning three. Measured, not deduced: GET .../artifacts returned 200 with
// artifacts[] while the panel showed its empty state.
//
// So the panel claims ownership and a token like any other reader, with
// view:'panel'. That view is a claim, not a door: ArtifactsDrawer opens on
// 'list' and 'reader' only, so the response is accepted and nothing slides
// over the page. (I first wrote that no drawer rendered 'panel' without
// checking; the drawer opened on ANY truthy view and covered the dossier with
// "Not in this conversation". Seen on screen, not in the diff.) The
// alternative, loosening acceptsResponse to allow a null view, would have let
// a late response from a closed drawer land on whatever is on screen now; that
// guard is what protects a fast A→B switch.
export function listArtifactsInPanel(sessionId) {
  if (!sessionId) return;
  const token = claimRequest(sessionId, { view: 'panel', fileId: null, from: 'panel', expanded: false, seed: null });
  loadArtifacts(sessionId, { token });
}

// openArtifactsList — the discreet per-pane/head entry. Explicitly scoped: the
// caller passes the conversation the entry belongs to.
export function openArtifactsList(sessionId) {
  if (!sessionId) return;
  rememberTrigger();
  const token = beginRequest(sessionId, { view: 'list', fileId: null, from: 'chat', expanded: false, seed: null });
  loadArtifacts(sessionId, { token });
}

// openArtifactFromCard — every send_file card opens the reader on its own
// artifact, in the conversation that produced it.
export function openArtifactFromCard(sessionId, file) {
  const seed = seedFromFile(file);
  if (!sessionId || !seed) return;
  rememberTrigger();
  const token = beginRequest(sessionId, { view: 'reader', fileId: seed.id, from: 'chat', expanded: false, seed });
  loadArtifacts(sessionId, { token });
}

export function openArtifactFromList(fileId) {
  const slice = artifactsSlice(store.get());
  if (!fileId || !slice.ownerSessionId) return;
  patch({ view: 'reader', fileId, from: 'list', expanded: false });
}

export function backToArtifactsList() {
  patch({ view: 'list', fileId: null, from: 'chat', expanded: false, seed: null });
}

export function closeArtifacts() {
  const slice = artifactsSlice(store.get());
  if (!slice.view) return;
  setState({ artifacts: { ...ARTIFACTS_CLOSED, token: slice.token } });
}

export function setArtifactExpanded(expanded) {
  patch({ expanded: !!expanded });
}

export function retryArtifacts() {
  const slice = artifactsSlice(store.get());
  if (!slice.ownerSessionId) return;
  const token = slice.token + 1;
  patch({ token, status: 'loading', error: null });
  loadArtifacts(slice.ownerSessionId, { token });
}

// refreshArtifactsAfterDelivery — a successful send_file tool_end already
// carries the descriptor and arrives after the backend upsert, so no new bus
// event is needed. Only the conversation that owns the OPEN drawer refreshes;
// a delivery in another session must not repaint what is on screen.
export function refreshArtifactsAfterDelivery(sessionId, result) {
  const slice = artifactsSlice(store.get());
  if (!slice.view || slice.ownerSessionId !== sessionId) return;
  if (result !== undefined && !artifactFileId(result?.url)) return;
  const token = slice.token + 1;
  patch({ token });
  loadArtifacts(sessionId, { token });
}

// refreshArtifactsAfterReconnect — a socket that went away may have missed a
// delivery entirely. On init (fresh connection/snapshot) the open collection is
// reloaded from the authoritative endpoint.
export function refreshArtifactsAfterReconnect(sessionId) {
  const slice = artifactsSlice(store.get());
  if (!slice.view || slice.ownerSessionId !== sessionId) return;
  const token = slice.token + 1;
  patch({ token, status: slice.status === 'ready' ? 'ready' : 'loading' });
  loadArtifacts(sessionId, { token });
}

// closeArtifactsForSession — a conversation that disappears (closed/deleted)
// must not leave its collection open in the shared drawer.
export function closeArtifactsForSession(sessionId) {
  if (artifactsSlice(store.get()).ownerSessionId === sessionId) closeArtifacts();
}

// closeArtifactsForMissingOwner — the same rule driven by the authoritative
// roster, so a deletion performed in ANOTHER client/device is honoured here
// too. The roster still lists saved and closed conversations, so absence from
// it means deleted; a merely unloaded conversation keeps its drawer, because
// the artifacts API answers for it.
export function closeArtifactsForMissingOwner(rosterIds) {
  const owner = artifactsSlice(store.get()).ownerSessionId;
  if (owner && !rosterIds.has(owner)) closeArtifacts();
}
