// artifacts-model.js — pure helpers for the Artifacts collection: the DTO
// returned by GET /api/sessions/{id}/artifacts, the shape of the ephemeral
// store slice, search filtering and which reader a reference opens in.
// DOM-free on purpose, so the controller (data/artifacts.js) and the drawer
// component share one definition and it stays unit-testable.

import { previewKind } from './util/file-card.js';

export const EMPTY_ARTIFACTS = Object.freeze([]);

// The whole feature is one ephemeral, global slice: which conversation owns
// the drawer, what it is showing, and the state of the in-flight list request.
// `owner` is set explicitly by the entry that was clicked — never inferred
// from the focused session, so changing focus does not switch the reader.
export const ARTIFACTS_CLOSED = Object.freeze({
  ownerSessionId: null,
  view: null,        // null (closed) | 'list' | 'reader'
  fileId: null,
  from: 'chat',      // where the reader was opened from: 'chat' | 'list'
  expanded: false,
  // seed — the descriptor a send_file card already has, so opening its reader
  // paints immediately while the authoritative GET is in flight.
  seed: null,
  status: 'idle',    // 'idle' | 'loading' | 'ready' | 'error'
  error: null,
  items: EMPTY_ARTIFACTS,
  // token — monotonic request id. A late response for an older token (a fast
  // A→B switch) is dropped instead of painting B's drawer with A's list.
  token: 0,
});

// artifactFileId extracts the file id from an artifact/send_file URL. Only our
// own shape is accepted, mirroring parseFileCardData's guard.
export function artifactFileId(url) {
  if (typeof url !== 'string') return null;
  const match = url.match(/^\/api\/sessions\/[^/]+\/files\/([^/?#]+)$/);
  return match ? match[1] : null;
}

// artifactSessionId returns the conversation an artifact URL belongs to, so a
// card can open the drawer scoped to its own conversation.
export function artifactSessionId(url) {
  if (typeof url !== 'string') return null;
  const match = url.match(/^\/api\/sessions\/([^/]+)\/files\/[^/?#]+$/);
  return match ? match[1] : null;
}

function text(value) {
  return typeof value === 'string' ? value : '';
}

// normalizeArtifacts turns the API payload into the list the UI renders.
// Entries without a usable id/url are dropped rather than rendered broken.
// Order comes from the server (updated_at desc); it is preserved as given.
export function normalizeArtifacts(payload) {
  const raw = Array.isArray(payload?.artifacts) ? payload.artifacts : [];
  const items = [];
  for (const entry of raw) {
    if (!entry || typeof entry !== 'object') continue;
    const url = text(entry.url);
    const id = text(entry.id) || artifactFileId(url) || '';
    if (!id || !artifactFileId(url)) continue;
    const name = text(entry.name) || id;
    items.push({
      id,
      name,
      // title is optional visual metadata; the file name is the fallback so a
      // row always has a legible heading.
      title: text(entry.title).trim() || name,
      description: text(entry.description).trim(),
      mime: text(entry.mime),
      size: typeof entry.size === 'number' ? entry.size : 0,
      url,
      createdAt: text(entry.created_at),
      updatedAt: text(entry.updated_at),
      // A missing source stays listed with its last known metadata.
      available: entry.available !== false,
    });
  }
  return items;
}

export function filterArtifacts(items, query) {
  const needle = (query || '').trim().toLocaleLowerCase();
  if (!needle) return items;
  return items.filter((a) => `${a.title} ${a.name} ${a.description}`.toLocaleLowerCase().includes(needle));
}

// artifactKind picks the reader. Size is deliberately NOT an input: the stored
// size is the last observed one and the file may have shrunk since, so the cap
// is enforced against the actual response (readCapped), not stale metadata.
export function artifactKind(artifact) {
  if (!artifact) return 'text';
  return previewKind(artifact.name, artifact.mime);
}

export function isHtmlArtifact(artifact) {
  return artifactKind(artifact) === 'html';
}

// currentArtifact resolves what the reader should show: the entry from the
// authoritative list when it has arrived, otherwise the card's own descriptor.
export function currentArtifact(slice) {
  if (!slice || slice.view !== 'reader') return null;
  const found = slice.items.find((a) => a.id === slice.fileId);
  if (found) return found;
  return slice.seed && slice.seed.id === slice.fileId ? slice.seed : null;
}

// seedFromFile turns a send_file card descriptor into a provisional artifact.
export function seedFromFile(file) {
  const id = artifactFileId(file?.url);
  if (!id) return null;
  const name = text(file?.name) || id;
  return {
    id,
    name,
    title: text(file?.title).trim() || name,
    description: text(file?.description).trim(),
    mime: text(file?.mime),
    size: typeof file?.size === 'number' ? file.size : 0,
    url: file.url,
    createdAt: '',
    updatedAt: '',
    available: true,
  };
}

// originLabel — the drawer's accessible name always says whose collection it
// is showing, even where the visible name is dropped for width.
export function originLabel(origin, subject) {
  return origin ? `${subject} — ${origin}` : subject;
}

// artifactRevision — what makes an OPEN reader stale. A resend keeps the same
// id and URL, so those alone cannot detect a republication; the observed
// metadata the list already refreshes (updated_at, plus the name and MIME that
// decide the reader) is the signal. Size is deliberately excluded: it is last
// observed metadata and the read cap is enforced against the real response.
// No filesystem watcher and no reload timer: the reader re-reads when a
// delivery, a metadata change or an explicit reopen says it should.
export function artifactRevision(artifact) {
  if (!artifact) return '';
  return [artifact.id, artifact.url, artifact.updatedAt, artifact.name, artifact.mime].join('\u0000');
}

// artifactFailure — what each unreadable state tells the user and what it lets
// them do. A 410 (the source is not at its location) is recoverable: the
// reference is still valid, so once the agent puts the file back a retry
// works. A 404 is not: this conversation no longer holds the reference, so the
// action is to ask for it again — nothing is promised about restoring an old
// ID. Pure, so the contract is pinned without rendering.
export function artifactFailure(kind) {
  switch (kind) {
    case 'unavailable':
      return {
        message: 'The original file is not at its location right now. Ask the agent to restore the file, then retry.',
        retryable: true, shareable: false,
      };
    case 'missing':
      return {
        message: 'This artifact is not in this conversation any more. Ask the agent to send it again.',
        retryable: false, shareable: false,
      };
    case 'too-large':
      return { message: 'Too large to open here.', retryable: false, shareable: true };
    case 'binary':
      return { message: 'Cannot display this file here.', retryable: false, shareable: true };
    default:
      return { message: 'Could not open this artifact.', retryable: true, shareable: true };
  }
}

// acceptsResponse decides whether a list response may be applied: it must be
// the newest request AND still belong to the conversation that owns the
// drawer. This is what protects a fast A→B switch from A's late answer.
export function acceptsResponse(slice, { sessionId, token }) {
  if (!slice || !slice.view) return false;
  return slice.ownerSessionId === sessionId && slice.token === token;
}
