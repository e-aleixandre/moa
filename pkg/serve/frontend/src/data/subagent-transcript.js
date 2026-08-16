// subagent-transcript.js — backfills a live subagent's earlier transcript.
//
// A subagent born while its parent conversation is already open only ever
// receives DELTAS over the WebSocket: its store entry starts empty and
// accumulates from there. The delegated task is not a header — it is a
// user-role message with custom.source === 'subagent_parent' inside the
// CHILD's transcript, written before any event is streamed — so opening that
// subagent showed a view with no encargo and no earlier turns. Only a socket
// remount (which replays a full snapshot) used to repair it.
//
// The server already exposes the whole child transcript at
// GET /api/sessions/{id}/subagents/{jobID}; this module fetches it on demand
// and splices it UNDER whatever the socket has delivered so far.

import { api } from './api.js';
import { normalizeConversationProjection } from './ws-handlers.js';
import { store, updateSession } from './store.js';

const PAGE_SIZE = 100;
// The encargo is the OLDEST message, so a newest-first walk has to reach the
// head of the transcript to find it. Cap the walk so a runaway child with
// thousands of rows can't turn opening its view into a page storm; past the
// cap the view still gains the most recent 500 rows, far more than a reader
// scrolls back through.
const MAX_PAGES = 5;

// hasParentTask reports whether the entry already holds the child's very first
// message. It is the row that proves the transcript is complete: the backend
// writes it before the child runs, so anything holding it also holds
// everything in between.
export function hasParentTask(messages) {
  return (messages || []).some((m) => m?.custom?.source === 'subagent_parent');
}

// needsTranscriptHydration decides whether opening this subagent must hit the
// REST transcript. What can be missing is the history predating the moment
// this client started listening — not "any message" — so an entry restored
// from an init snapshot or from a persisted transcript is already complete.
export function needsTranscriptHydration(entry) {
  if (!entry) return false;
  if (entry.transcriptHydrating || entry.transcriptHydrated) return false;
  return !hasParentTask(entry.messages);
}

// Tool arguments are summarized by the server within a 512-byte budget, and
// text within 12KB. Comparing a shorter prefix keeps a truncated fetched row
// recognizable as the same row its untruncated live twin represents.
const SIGNATURE_CHARS = 160;

function rowText(row) {
  if (Array.isArray(row?.content)) {
    return row.content.filter((c) => c?.type === 'text').map((c) => c.text || '').join('');
  }
  return row?.text || '';
}

function stableArgs(args) {
  if (!args || typeof args !== 'object') return '';
  return Object.keys(args).sort()
    .map((key) => `${key}=${typeof args[key] === 'string' ? args[key] : JSON.stringify(args[key])}`)
    .join('&');
}

// rowSignature is the identity used to tell "the same row" apart across the
// two sources. A message id is authoritative whenever both sides have one —
// the backend mints it, so the live event and the REST item agree on it. Tool
// rows have no shared id (REST keys them by their position under the assistant
// call, the socket by the provider's tool_call_id), so they fall back to what
// both do carry: the tool and its arguments.
export function rowSignature(row) {
  if (row?._msg_id) return `id|${row._msg_id}`;
  if (row?._type === 'tool_start') {
    return `tool|${row.tool_name || ''}|${stableArgs(row.args).slice(0, SIGNATURE_CHARS)}`;
  }
  return `msg|${row?.role || ''}|${rowText(row).slice(0, SIGNATURE_CHARS)}`;
}

// mergeSubagentTranscript splices a fetched history under the rows the socket
// already delivered.
//
// The live rows are a suffix of the same transcript, so the fetched rows that
// duplicate them are its LAST occurrences: walking the fetched list backwards
// and consuming one live signature per match drops exactly those. Live rows
// are never rewritten nor dropped, so deltas that landed while the request was
// in flight survive untouched, and every message ends up rendered once —
// always from the live copy, which is the fresher of the two.
export function mergeSubagentTranscript(fetched, live) {
  const current = Array.isArray(live) ? live : [];
  const unmatched = new Map();
  for (const row of current) {
    const signature = rowSignature(row);
    unmatched.set(signature, (unmatched.get(signature) || 0) + 1);
  }
  const rows = normalizeConversationProjection(fetched || []);
  const prefix = [];
  for (let i = rows.length - 1; i >= 0; i--) {
    const signature = rowSignature(rows[i]);
    const pending = unmatched.get(signature) || 0;
    if (pending > 0) {
      unmatched.set(signature, pending - 1);
      continue;
    }
    prefix.unshift(rows[i]);
  }
  return [...prefix, ...current];
}

// fetchTranscriptItems pages backwards (the endpoint answers newest first)
// until the encargo shows up, the server reports no more history, or the page
// cap is hit. Returns raw REST items in chronological order.
async function fetchTranscriptItems(id, jobId) {
  const items = [];
  let cursor = '';
  for (let page = 0; page < MAX_PAGES; page++) {
    const query = `limit=${PAGE_SIZE}${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`;
    const response = await api('GET', `/api/sessions/${id}/subagents/${jobId}?${query}`);
    if (!response) break;
    const batch = response.messages || [];
    items.unshift(...(response.order === 'newest_first' ? [...batch].reverse() : batch));
    if (!response.has_more || !response.next_cursor) break;
    if (items.some((item) => item?.source === 'subagent_parent')) break;
    cursor = response.next_cursor;
  }
  return items;
}

// hydrateSubagentTranscript backfills one subagent's earlier history.
// `isActive` lets the caller refuse the result when the view was closed while
// the request was in flight. Failures — including the 404 of a pruned
// subagent — degrade to whatever the socket delivered, and clear the in-flight
// flag so reopening the view may try again.
export async function hydrateSubagentTranscript(id, jobId, isActive = () => true) {
  const session = store.get().sessions[id];
  const entry = session?.subagents?.[jobId];
  if (!needsTranscriptHydration(entry)) return false;
  updateSession(id, {
    subagents: { ...session.subagents, [jobId]: { ...entry, transcriptHydrating: true } },
  });

  let items = null;
  try {
    items = await fetchTranscriptItems(id, jobId);
  } catch { /* degrade to the live-only transcript below */ }

  // Read the store AFTER the round trip: WS deltas may have extended this
  // subagent while the request was in flight, and merging into the pre-await
  // snapshot would silently drop them.
  const settled = store.get().sessions[id];
  const now = settled?.subagents?.[jobId];
  if (!now) return false;
  const applied = !!items && isActive();
  updateSession(id, {
    subagents: {
      ...settled.subagents,
      [jobId]: {
        ...now,
        transcriptHydrating: false,
        ...(applied
          ? { messages: mergeSubagentTranscript(items, now.messages || []), transcriptHydrated: true }
          : {}),
      },
    },
  });
  return applied;
}
