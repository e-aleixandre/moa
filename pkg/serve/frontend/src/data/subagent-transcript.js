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
  if (!entry || entry.transcriptHydrating) return false;
  // A reconnect snapshot is authoritative about which jobs are live. When the
  // viewed entry is absent, fetch its persisted summary even if its messages
  // were already hydrated: the missed frame may be its terminal lifecycle.
  if (entry.lifecycleUnverified) return true;
  if (entry.transcriptHydrated) return false;
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

// mergeSubagentTranscript reconciles the fetched history with the rows the
// socket delivered.
//
// The fetched snapshot is the authoritative CHRONOLOGY: the server writes the
// child's history in order, so its sequence is the one to render. Live rows
// are not necessarily a suffix of it — a hydration started before the task was
// announced can have its REST response run ahead of the queued live events, so
// the socket may so far hold only a row the snapshot already contains in the
// middle. Prepending everything unmatched then moved the encargo BELOW the
// answer it caused.
//
// So: walk the fetched rows in order, emitting the live copy wherever the two
// sources describe the same row (live is the fresher of the two, and never
// truncated), then append the live rows the snapshot did not contain — the
// genuine tail that landed while the request was in flight. Every message is
// rendered exactly once, in the server's order.
//
// Matching is occurrence-aware, and pairs from the END: a repeated tool
// signature has no id to tell its occurrences apart, and the live rows are the
// most recent ones, so the last N live occurrences correspond to the last N
// fetched occurrences.
export function mergeSubagentTranscript(fetched, live, toolDetailBase = '') {
  const current = Array.isArray(live) ? live : [];
  const rows = normalizeConversationProjection(fetched || [], toolDetailBase);

  // Legacy signatures are only comparable between the same shared message
  // anchors. Matching across a parent task would move a new execution into
  // an older turn just because both invoked the same command.
  const liveIDs = new Set(current.map(row => row?._msg_id).filter(Boolean));
  const sharedIDs = new Set(rows.map(row => row?._msg_id).filter(id => id && liveIDs.has(id)));
  const signatures = (source) => {
    const nextAnchors = [];
    let next = null;
    for (let i = source.length - 1; i >= 0; i--) {
      nextAnchors[i] = next;
      if (sharedIDs.has(source[i]?._msg_id)) next = source[i]._msg_id;
    }
    let previous = null;
    return source.map((row, i) => {
      if (sharedIDs.has(row?._msg_id)) previous = row._msg_id;
      const signature = rowSignature(row);
      return row?._msg_id ? signature : JSON.stringify([signature, previous, nextAnchors[i]]);
    });
  };
  const fetchedSignatures = signatures(rows);
  const liveSignatures = signatures(current);

  // signature → positions, in order, on each side.
  const fetchedBySignature = new Map();
  rows.forEach((row, index) => {
    const signature = fetchedSignatures[index];
    if (!fetchedBySignature.has(signature)) fetchedBySignature.set(signature, []);
    fetchedBySignature.get(signature).push(index);
  });
  const liveBySignature = new Map();
  current.forEach((row, index) => {
    const signature = liveSignatures[index];
    if (!liveBySignature.has(signature)) liveBySignature.set(signature, []);
    liveBySignature.get(signature).push(index);
  });

  const liveForFetched = new Map(); // fetched index → live index
  const pairedLive = new Set();
  for (const [signature, liveIndexes] of liveBySignature) {
    const fetchedIndexes = fetchedBySignature.get(signature) || [];
    const pairs = Math.min(liveIndexes.length, fetchedIndexes.length);
    for (let n = 1; n <= pairs; n++) {
      const liveIndex = liveIndexes[liveIndexes.length - n];
      liveForFetched.set(fetchedIndexes[fetchedIndexes.length - n], liveIndex);
      pairedLive.add(liveIndex);
    }
  }

  const merged = rows.map((row, index) => (
    liveForFetched.has(index) ? current[liveForFetched.get(index)] : row
  ));
  for (let index = 0; index < current.length; index++) {
    if (!pairedLive.has(index)) merged.push(current[index]);
  }
  return merged;
}

// fetchTranscriptItems pages backwards (the endpoint answers newest first)
// until the encargo shows up, the server reports no more history, or the page
// cap is hit. Returns raw REST items in chronological order together with the
// lifecycle fields carried by the first page.
async function fetchTranscriptItems(id, jobId) {
  const items = [];
  let summary = null;
  let cursor = '';
  for (let page = 0; page < MAX_PAGES; page++) {
    const query = `limit=${PAGE_SIZE}${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`;
    const response = await api('GET', `/api/sessions/${id}/subagents/${jobId}?${query}`);
    if (!response) break;
    if (!summary) {
      summary = {
        status: response.status,
        finishedAtMs: response.finished_at ? Date.parse(response.finished_at) : null,
      };
    }
    const batch = response.messages || [];
    items.unshift(...(response.order === 'newest_first' ? [...batch].reverse() : batch));
    if (!response.has_more || !response.next_cursor) break;
    if (items.some((item) => item?.source === 'subagent_parent')) break;
    cursor = response.next_cursor;
  }
  return summary ? { items, ...summary } : null;
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

  let snapshot = null;
  try {
    snapshot = await fetchTranscriptItems(id, jobId);
  } catch { /* degrade to the live-only transcript below */ }

  // Read the store AFTER the round trip: WS deltas may have extended this
  // subagent while the request was in flight, and merging into the pre-await
  // snapshot would silently drop them.
  const settled = store.get().sessions[id];
  const now = settled?.subagents?.[jobId];
  if (!now) return false;
  const applied = !!snapshot && isActive();
  const terminal = applied && (
    snapshot.status === 'completed' || snapshot.status === 'failed' || snapshot.status === 'cancelled'
  );
  const merged = applied
    ? mergeSubagentTranscript(
      snapshot.items,
      now.messages || [],
      `/api/sessions/${id}/subagents/${jobId}`,
    )
    : null;
  // A live snapshot without the parent task may precede its announcement.
  // Only terminal snapshots are complete without it, including legacy history
  // written before provenance tags were introduced.
  const hydrated = applied && (terminal || hasParentTask(merged));
  updateSession(id, {
    subagents: {
      ...settled.subagents,
      [jobId]: {
        ...now,
        transcriptHydrating: false,
        ...(applied
          ? {
            messages: merged,
            status: snapshot.status || now.status,
            finishedAtMs: Number.isFinite(snapshot.finishedAtMs) ? snapshot.finishedAtMs : now.finishedAtMs,
            streamingText: terminal ? null : now.streamingText,
            thinkingText: terminal ? null : now.thinkingText,
            lifecycleUnverified: false,
            transcriptHydrated: hydrated,
          }
          : {}),
      },
    },
  });
  return applied;
}
