// subagent-view-model.js — PURE projection for the SubagentView (5J). Given the
// store's `session` object and the job_id being viewed, it derives everything
// the view needs (identity accent, sibling rail, terminal state + outcome
// banner) WITHOUT any preact/DOM. It reuses the same live-subagent rules as the
// stream projection (liveSubagents/liveAgent/FANOUT_ACCENTS) so the fork's
// accent and its siblings never diverge from the fanout block it zooms into.
//
// The heart of the "cero divergencia" contract: the sub-conversation body is
// projected by the very same projectStream() the parent stream uses — so a
// subagent transcript renders with the identical ledger/thinking/diff/prose
// pipeline (INC-37). This module only computes the FRAME (accent, siblings,
// task card text, terminal outcome); the body is projectStream(subSession).

import {
  FANOUT_ACCENTS,
  liveSubagents,
  liveAgent,
  seenJobIdsOf,
  projectStream,
  subagentAccentIndex,
} from './stream-model.js';
import { shortModel } from './util/format.js';
import { formatElapsed } from './util/activity.js';

const TERMINAL_STATUSES = new Set(['completed', 'failed', 'cancelled', 'error', 'done']);

// firstLine returns the first line of a (possibly multi-line) string.
function firstLine(str) {
  if (!str) return '';
  const nl = str.indexOf('\n');
  return nl < 0 ? str : str.slice(0, nl);
}

// codenameOf derives a short, breadcrumb-friendly label from the (long) task.
// There is no server-side codename today (SUBAGENT-VIEW-SPEC §9.3 [BACKEND]),
// so we trim the first line; the full task lives in the task card below.
function codenameOf(sub) {
  const short = firstLine(sub.task || '').trim();
  if (short) return short.length > 32 ? short.slice(0, 31) + '…' : short;
  return shortModel(sub.model) || sub.jobId || 'subagent';
}

// accentForIndex is the identity color for a fanout slot (never a state color).
function accentForIndex(i) {
  return FANOUT_ACCENTS[((i % FANOUT_ACCENTS.length) + FANOUT_ACCENTS.length) % FANOUT_ACCENTS.length];
}

// terminalOutcome maps a terminal status to the outcome-banner variant.
//   completed/done → 'completed' (green)
//   failed/error   → 'failed'    (red)
//   cancelled      → 'cancelled' (neutral / grey thread)
function terminalOutcome(status) {
  if (status === 'failed' || status === 'error') return 'failed';
  if (status === 'cancelled') return 'cancelled';
  return 'completed';
}

// lastError extracts the real failure text for a failed subagent: the last
// tool row that errored, else the subagent's own result/streaming text. Returns
// '' when nothing usable is present (the banner then shows a generic message
// rather than "undefined").
function lastError(sub) {
  const msgs = Array.isArray(sub.messages) ? sub.messages : [];
  for (let i = msgs.length - 1; i >= 0; i--) {
    const m = msgs[i];
    if (m && m._type === 'tool_start' && m.status === 'error' && m.result) {
      return String(m.result);
    }
  }
  return String(sub.result || sub.streamingText || '');
}

// resultChip returns a short result string for a completed subagent, or ''. No
// structured result_chip exists yet (SUBAGENT-VIEW-SPEC §9.4 [BACKEND]); we use
// the first line of the free-text result as a best-effort chip.
function resultChip(sub) {
  const line = firstLine(sub.result || '').trim();
  if (!line) return '';
  return line.length > 60 ? line.slice(0, 59) + '…' : line;
}

// subagentView is the single entry point. Returns null when the viewed job_id
// no longer exists in the session (the "rebote": the caller clears
// viewingSubagent and falls back to the parent). Otherwise a plain descriptor:
//
//   {
//     jobId, name, accent, model, task, async,
//     status, terminal:boolean, outcome:'completed'|'failed'|'cancelled'|null,
//     resultChip, error, usage,
//     siblings:[{ id, name, accent, action?, time?, active }],  // [] when alone
//     blocks:[...],           // projectStream() of the sub-conversation
//     action?, elapsed?,      // live now-line segments (omitted when absent)
//   }
export function subagentView(session, jobId) {
  if (!session || !jobId) return null;
  const subs = normalizeSubagents(session.subagents);
  const sub = subs[jobId];
  if (!sub) return null; // rebote — the subagent was pruned

  const seen = seenJobIdsOf(session.messages);
  const { subs: live } = liveSubagents(subs, seen);
  const liveIdx = live.findIndex((s) => String(s.jobId) === String(jobId));

  // Identity accent: STABLE for the subagent's whole life, derived from its
  // position in session.subagents' insertion order — never from the live
  // index. The live index shifts whenever a sibling finishes (it drops out of
  // the live set), which would otherwise repaint every remaining sibling's
  // color; a fanout/tray/view must all agree on one color per subagent for as
  // long as it exists.
  const stableIdx = subagentAccentIndex(subs, jobId);
  const accent = accentForIndex(stableIdx);

  // Sibling rail: the OTHER live subagents of the same fanout. Only a real
  // fanout (2+ live) shows a rail; a lone subagent shows none.
  let siblings = [];
  if (live.length >= 2) {
    siblings = live.map((s) => {
      const a = liveAgent(s, subagentAccentIndex(subs, s.jobId));
      const chip = {
        id: a.id,
        name: a.name,
        accent: a.accent,
        active: String(s.jobId) === String(jobId),
      };
      if (a.action) chip.action = a.action;
      if (a.time) chip.time = a.time;
      return chip;
    });
  }

  const status = sub.status || 'running';
  const terminal = TERMINAL_STATUSES.has(status);
  const outcome = terminal ? terminalOutcome(status) : null;

  const view = {
    jobId,
    name: codenameOf(sub),
    accent,
    model: shortModel(sub.model) || sub.model || '',
    task: sub.task || '',
    async: !!sub.async,
    status,
    terminal,
    outcome,
    siblings,
    blocks: projectStream({
      messages: sub.messages,
      streamingText: sub.streamingText,
      thinkingText: sub.thinkingText,
    }),
    usage: sub.usage || null,
  };

  if (terminal) {
    if (outcome === 'failed') view.error = lastError(sub);
    if (outcome === 'completed') view.resultChip = resultChip(sub);
  } else {
    // Live now-line segments. The action (last in-flight tool / "Working")
    // comes from liveAgent when this subagent is in the live set; elapsed is
    // derived
    // from the backend-anchored startedAtMs (started_at_ms) when present, so
    // it keeps counting across a reconnect instead of resetting. Omitted
    // (never "undefined"/"NaN") when the anchor isn't known.
    if (liveIdx >= 0) {
      const a = liveAgent(live[liveIdx], stableIdx);
      if (a.action) view.action = a.action;
    }
    if (sub.startedAtMs) {
      const elapsed = formatElapsed(Date.now() - sub.startedAtMs);
      if (elapsed) view.elapsed = elapsed;
    }
  }

  return view;
}

// normalizeSubagents accepts session.subagents as either a map keyed by
// job_id (the common shape) or an array of subagent records, returning a
// map keyed by jobId either way. Mirrors stream-model's liveSubagents, which
// already tolerates both shapes.
function normalizeSubagents(subagents) {
  if (!subagents) return {};
  if (!Array.isArray(subagents)) return subagents;
  const out = {};
  for (const s of subagents) {
    if (s && s.jobId) out[s.jobId] = s;
  }
  return out;
}

// canPromote decides whether the promote action is offered: only a
// synchronous (blocking) subagent that is CURRENTLY running can be promoted —
// the backend only accepts the promote call while status is exactly
// 'running' (a 'cancelling' subagent, though not terminal, can no longer be
// promoted).
export function canPromote(view) {
  return !!view && view.status === 'running' && !view.async;
}
