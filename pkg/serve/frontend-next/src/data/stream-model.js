// stream-model.js — PURE projection from the data-engine message model to the
// ordered list of BLOCKS the Studio frontend renders. No preact, no JSX, no
// components, no store: it takes the plain `session` object (as produced by the
// ws-handlers / conversation-reducer) and returns a serializable array. This is
// the single place that decides how tool calls group into ledgers, how live
// subagents become a fanout, and how live async bash jobs become background
// strips. Every visual subphase downstream (5C) consumes this contract, so the
// grouping rules live here — isolated and exhaustively tested.
//
// ── OUTPUT CONTRACT (array of plain objects, each with a `kind`) ─────────────
//
//   { kind:'system', text }
//       A system line (clear/compact/branch/goal marker, or the truncation
//       notice "Older messages…"). Rendered as a thin separator. A system line
//       closes the current assistant turn: assistant content after it starts a
//       fresh document.
//
//   { kind:'waypoint', time, text, attachments? }
//       A user turn. `text` is the joined text of the user message. `time` is
//       the message ts when present (else undefined — we never invent one).
//       `attachments` is the list of non-text content items (images/files),
//       passed through untouched so a photo message renders as an attachment
//       instead of breaking.
//
//   { kind:'document', blocks:[...] }        // a FINISHED assistant turn
//   { kind:'streaming', blocks:[...] }       // the LIVE assistant turn
//       One assistant turn = everything the assistant produced between one user
//       message (or the start) and the next user message. `blocks` is the
//       assistant's prose and activity INTERLEAVED IN ORDER. A turn is
//       `streaming` (skeleton/cursor in 5C) when there is live output at the
//       tail: session.streamingText, session.thinkingText, a trailing
//       tool_start still in status generating/running, or live subagents/bash
//       jobs in session.subagents. Otherwise it is a `document`. Sub-block
//       types:
//
//         { type:'prose', text }
//             A run of assistant markdown text (raw; markdown rendered in 5C).
//
//         { type:'thinking', text }
//             A collapsible thinking block (from session.thinkingText).
//
//         { type:'ledger', rows:[...] }
//             A batch of CONSECUTIVE tool calls (no prose between them). Each
//             row matches the ActivityLedger/LedgerRow prop shape:
//               { tool, arg:{text,detail}|string, out, status:'ok'|'err'|'warn',
//                 body?, id, live?, startedAt? }
//             `id` is the tool_call_id (stable key). `status` maps
//             done→ok, error→err, rejected→warn, running/generating→ok.
//             `live:true` marks the tool currently in flight (status
//             running/generating); it is always the LAST row of the LAST
//             ledger of the streaming turn (see toLedgerRow). `startedAt` is
//             the ms epoch the tool call started (ws-handlers.js), used for
//             the live row's elapsed timer — the "Tail" direction (B) of
//             TOOLCALLS-ALT-SPEC-FABLE.md.
//
//         { type:'diff', filename, diffText, startLine }
//             A full-width diff for an edit that carries a REAL unified diff
//             (headers ---/+++/@@). Emitted as a SIBLING right after the ledger
//             that contains the edit row (so it shows full width, as in the
//             mockup). An edit-with-diff therefore closes its ledger; tools
//             after it open a new ledger. Edits WITHOUT a server unified diff
//             emit no diff block — just their ledger row — so DiffBlock never
//             gets an unparseable fallback and renders empty.
//
//         { type:'file', file:{name,size,mime,url} }
//             A full-width download card for a FINISHED send_file call whose
//             result ends in a valid JSON file descriptor (see
//             data/util/file-card.js#parseFileCardData). Emitted as a SIBLING
//             right after the ledger that contains the send_file row (same
//             pattern as `diff`), closing that ledger. A send_file call that
//             errored keeps its raw ledger text instead (no file block).
//
//         { type:'fanout', task?, agents:[...] }
//             2+ LIVE subagents running in parallel right now. Matches the
//             FanoutBlock prop shape; each agent:
//               { id, name, accent, state:'running', action?, time? }
//             (`id`=jobId key, `accent` cycles the palette, `result`/
//             `resultDesc` only apply to done agents, which come from messages,
//             not here — so live fanout agents are always 'running').
//
//         { type:'background', jobs:[...] }
//             LIVE async bash jobs (kind:'bash' entries in session.subagents).
//             Matches the BackgroundJob prop shape; each:
//               { jobId, jobLabel, cmd, progress?, elapsed?, lines:[...] }
//             `lines` is the output tail as an array of strings.
//
// ── GROUPING DECISIONS (documented, see report) ─────────────────────────────
//   • Consecutive tool calls → one ledger; the first prose after them closes it.
//   • One turn = user→next user; all assistant output in between = one document.
//   • TERMINATED subagents/bash are already pushed into session.messages (by
//     handleWsSubagentComplete / handleWsBashComplete) as tool_start rows in
//     their real chronological position, so they flow through the normal ledger
//     pipeline — no special-casing, no fanout invented for sequential work.
//   • Only LIVE subagents/bash (status running/cancelling) are read from
//     session.subagents; they have no message yet, so they attach to the live
//     turn. 1 live subagent → a ledger row (tool:'task'); 2+ → a fanout block;
//     live bash → a background block. Anything whose job_id already appears in
//     messages is skipped (dedup — completed entries also linger in the map).
//   • Edit diff = sibling block after the ledger (full width), only for real
//     unified diffs; never a fallback that renders empty.
//   • No reliable per-message ts → we DO NOT emit `tick` date separators.

import {
  truncateText,
  toolPath,
  toolPreview,
  splitPreviewTail,
  shortModel,
} from './util/format.js';
import { formatElapsed } from './util/activity.js';
import { parseFileCardData } from './util/file-card.js';

const MAX_MESSAGES_BEFORE_TRUNCATION_NOTE = 200;

// Fanout accent palette, cycled by agent index (see FanoutBlock.css). Exported
// so the SubagentView (5J) assigns the SAME accent a subagent had in the fanout
// (single source of truth for the sky/teal/mauve identity) instead of inventing
// its own palette.
export const FANOUT_ACCENTS = ['sky', 'teal', 'mauve', 'peach', 'blue', 'lavender'];

// projectStream is the single entry point. Given the store's `session` object
// it returns the ordered block array described in the contract above. It never
// mutates the input.
export function projectStream(session) {
  if (!session) return [];
  const allMessages = Array.isArray(session.messages) ? session.messages : [];

  // Cap the rendered history like the old SPA's MessageList: only the last
  // MAX_MESSAGES_BEFORE_TRUNCATION_NOTE turns are projected, so a very long
  // conversation stays responsive. `firstRendered` is the absolute offset of
  // the first kept message, used both to slice and as a stable id base.
  const firstRendered = Math.max(0, allMessages.length - MAX_MESSAGES_BEFORE_TRUNCATION_NOTE);
  const messages = firstRendered > 0 ? allMessages.slice(firstRendered) : allMessages;

  const blocks = [];

  // Truncation notice first, mirroring the old SPA: either the server told us
  // history was cut, or we elided older turns above.
  if (session.historyTruncated || firstRendered > 0) {
    blocks.push({ kind: 'system', id: 'sys-truncated', text: 'Older messages…' });
  }

  // Collect every tool_call_id already present in messages so we can dedup
  // live subagents/bash whose terminated card was already pushed here. The
  // completion handlers key those cards as `subagent-<job_id>` /
  // `bash-complete-<job_id>`, so we also index the bare job_id.
  const seenJobIds = seenJobIdsOf(messages);

  // currentDoc = the document object for the turn currently being built; reset
  // to null at every user message (and system line) so the next assistant
  // content starts a fresh document. currentLedger = the open ledger inside
  // currentDoc, or null when the last thing pushed was prose/diff.
  let currentDoc = null;
  let currentLedger = null;
  // currentDelegation = the open delegation block for this turn (SUBAGENTS-
  // REDESIGN-SPEC §1), or null. Reset at every turn boundary like currentDoc.
  let currentDelegation = null;

  // abs = absolute index of the message being processed (stable across polls
  // as long as older messages aren't dropped), used to derive block ids that
  // survive re-projection so preact doesn't recycle <details>/BackgroundJob
  // state into the wrong block.
  let abs = firstRendered;

  function ensureDoc() {
    if (!currentDoc) {
      currentDoc = { kind: 'document', id: `doc-${abs}`, blocks: [] };
      blocks.push(currentDoc);
      currentLedger = null;
    }
    return currentDoc;
  }

  function closeLedger() {
    currentLedger = null;
  }

  for (let i = 0; i < messages.length; i++, abs++) {
    const msg = messages[i];
    if (msg && msg._type === 'system') {
      // System line breaks the turn: emit at top level, start fresh doc after.
      blocks.push({ kind: 'system', id: `sys-${abs}`, text: msg.text || '' });
      currentDoc = null;
      currentLedger = null;
      currentDelegation = null;
      continue;
    }

    if (msg && msg._type === 'tool_start') {
      // subagent_wait is pure mechanism (launch + wait are two tool calls); it
      // only feeds the "parent is blocked waiting" state, never a row
      // (SUBAGENTS-REDESIGN-SPEC §4). Drop it from the projection entirely.
      if (msg.tool_name === 'subagent_wait') continue;

      const doc = ensureDoc();

      // A subagent card (live-completed or from history) is not a normal ledger
      // row: it folds into the turn's delegation block (SUBAGENTS-REDESIGN-SPEC
      // §1). Keyed `subagent-<jobId>`, tool_name 'subagent'.
      if (msg.tool_name === 'subagent') {
        if (!currentDelegation) {
          currentDelegation = { type: 'delegation', id: `delegation-${abs}`, agents: [], settled: true };
          doc.blocks.push(currentDelegation);
        }
        const jobId = String(msg.tool_call_id || '').replace(/^subagent-/, '');
        currentDelegation.agents.push(delegationDoneAgent(msg, subagentAccent(session.subagents, jobId, msg.accentIndex)));
        closeLedger();
        continue;
      }

      if (!currentLedger) {
        currentLedger = { type: 'ledger', id: `ledger-${abs}`, rows: [] };
        doc.blocks.push(currentLedger);
      }
      currentLedger.rows.push(toLedgerRow(msg));

      // An edit carrying a real unified diff emits a full-width sibling and
      // closes the ledger so subsequent tools open a new one below the diff.
      const diff = toDiffBlock(msg);
      if (diff) {
        diff.id = `diff-${abs}`;
        doc.blocks.push(diff);
        closeLedger();
      }

      // A finished send_file result renders as a download card, sibling to
      // the ledger row (like the diff above) instead of raw text.
      const file = toFileBlock(msg);
      if (file) {
        file.id = `file-${abs}`;
        doc.blocks.push(file);
        closeLedger();
      }
      continue;
    }

    if (msg && msg.role === 'assistant') {
      const text = joinText(msg.content);
      if (text) {
        const doc = ensureDoc();
        closeLedger();
        doc.blocks.push({ type: 'prose', id: `prose-${abs}`, text });
      }
      continue;
    }

    if (msg && msg.role === 'user') {
      // User turn boundary: close the previous assistant document.
      currentDoc = null;
      currentLedger = null;
      currentDelegation = null;
      const attachments = attachmentsOf(msg.content);
      const wp = {
        kind: 'waypoint',
        id: `wp-${msg.msg_id || msg._msg_id || abs}`,
        time: msg.ts,
        text: joinText(msg.content),
      };
      if (attachments.length > 0) wp.attachments = attachments;
      blocks.push(wp);
      continue;
    }
    // Unknown message shapes are ignored (robustness).
  }

  // ── Live work: streaming text/thinking, a trailing running tool, and the
  // in-flight subagents/bash still living in session.subagents (only those,
  // terminated ones are already in messages above). ─────────────────────────
  const { subs: liveSubs, bash: liveBash } = liveSubagents(session.subagents, seenJobIds);

  const lastMsg = messages.length > 0 ? messages[messages.length - 1] : null;
  const trailingRunningTool = !!(lastMsg && lastMsg._type === 'tool_start' &&
    (lastMsg.status === 'running' || lastMsg.status === 'generating'));
  const hasStreamingText = !!session.streamingText;
  const hasThinking = !!session.thinkingText;
  const hasLiveWork = liveSubs.length > 0 || liveBash.length > 0;
  const live = hasStreamingText || hasThinking || trailingRunningTool || hasLiveWork;

  if (live) {
    const doc = ensureDoc();
    closeLedger();
    if (hasThinking) doc.blocks.push({ type: 'thinking', id: `${doc.id}-thinking`, text: session.thinkingText });
    if (hasStreamingText) doc.blocks.push({ type: 'prose', id: `${doc.id}-stream`, text: session.streamingText });

    // Live subagents merge into this turn's delegation block (creating one if
    // the turn had none yet), joining any already-terminated agent rows. The
    // block is `settled:false` while at least one agent is still running so the
    // renderer keeps it live (hairline sweep, breathing dots) instead of
    // auto-collapsing (SUBAGENTS-REDESIGN-SPEC §1.3).
    if (liveSubs.length > 0) {
      if (!currentDelegation) {
        currentDelegation = { type: 'delegation', id: `${doc.id}-delegation`, agents: [] };
        doc.blocks.push(currentDelegation);
      }
      for (const s of liveSubs) {
        currentDelegation.agents.push(
          delegationRunningAgent(s, subagentAccentIndex(session.subagents, s.jobId)),
        );
      }
      currentDelegation.settled = false;
    }

    // Parent async bash (no owning subagent) stays a background block (spec §2).
    if (liveBash.length > 0) {
      doc.blocks.push({ type: 'background', id: `${doc.id}-bg`, jobs: liveBash.map(backgroundJob) });
    }

    doc.kind = 'streaming';
  }

  // Finalize every delegation block: attach its summary and mark settled ones
  // (all agents terminated) so the renderer auto-collapses them to the header
  // line (SUBAGENTS-REDESIGN-SPEC §1.3).
  finalizeDelegations(blocks);

  return blocks;
}

// ── helpers ────────────────────────────────────────────────────────────────

// subagentAccentIndex is the STABLE fanout-accent index for one subagent.
// Prefers the backend-assigned per-session creation ordinal (sub.accentIndex,
// propagated over WS in subagent_start / the init snapshot — see
// ws-handlers.js), which is deterministic and survives WS reconnects. Falls
// back to this subagent's position in session.subagents' insertion order (or
// array order) only when no backend ordinal is available (older payload,
// catalog/specimen mocks), matching the previous behavior for those cases.
// A sibling finishing and dropping out of the live set must never repaint the
// remaining siblings' colors, so every caller that needs "this subagent's
// color" (fanout, tray, SubagentView) goes through here instead of indexing
// into the live-only array.
export function subagentAccentIndex(subagents, jobId) {
  if (!subagents) return 0;
  if (Array.isArray(subagents)) {
    const sub = subagents.find((s) => s && String(s.jobId) === String(jobId));
    if (sub && Number.isInteger(sub.accentIndex)) return sub.accentIndex;
    const i = subagents.findIndex((s) => s && String(s.jobId) === String(jobId));
    return i >= 0 ? i : 0;
  }
  const sub = subagents[jobId];
  if (sub && Number.isInteger(sub.accentIndex)) return sub.accentIndex;
  const i = Object.keys(subagents).indexOf(jobId);
  return i >= 0 ? i : 0;
}

// jobIdAccentIndex deterministically derives a palette slot from a job id, so a
// terminated card whose live entry is gone (e.g. after reconnect) keeps a stable
// color instead of collapsing to slot 0.
function jobIdAccentIndex(jobId) {
  const s = String(jobId || '');
  let h = 0;
  for (let i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) | 0;
  return Math.abs(h) % FANOUT_ACCENTS.length;
}

// subagentAccent resolves a terminated card's accent token, preferring an
// explicit accentIndex saved on the card, then the live session entry, then a
// deterministic hash of the job id (never a blind slot 0).
function subagentAccent(subagents, jobId, cardAccentIndex) {
  let idx;
  if (Number.isInteger(cardAccentIndex)) idx = cardAccentIndex;
  else if (hasSubagentEntry(subagents, jobId)) idx = subagentAccentIndex(subagents, jobId);
  else idx = jobIdAccentIndex(jobId);
  return FANOUT_ACCENTS[idx % FANOUT_ACCENTS.length];
}

function hasSubagentEntry(subagents, jobId) {
  if (!subagents) return false;
  if (Array.isArray(subagents)) return subagents.some((s) => s && String(s.jobId) === String(jobId));
  return Object.prototype.hasOwnProperty.call(subagents, jobId);
}

// seenJobIdsOf collects every tool_call_id present in a message list, indexing
// the bare job_id for the terminated subagent/bash cards keyed as
// `subagent-<id>` / `bash-complete-<id>` (see projectStream's dedup). Exported
// so the AgentTray uses the exact same dedup set the stream does.
export function seenJobIdsOf(messages) {
  const seenJobIds = new Set();
  const list = Array.isArray(messages) ? messages : [];
  for (const msg of list) {
    if (msg && msg._type === 'tool_start' && msg.tool_call_id) {
      const id = String(msg.tool_call_id);
      seenJobIds.add(id);
      if (id.startsWith('subagent-')) seenJobIds.add(id.slice('subagent-'.length));
      if (id.startsWith('bash-complete-')) seenJobIds.add(id.slice('bash-complete-'.length));
    }
  }
  return seenJobIds;
}

// liveTrayAgents projects a session into the AgentTray's chip descriptors: the
// LIVE subagents (each carrying its fanout identity accent) followed by the LIVE
// async bash jobs (no identity accent — INC-22: spinner overlay1 + mono `bash`).
// It reuses the SAME liveSubagents/liveAgent rules the stream projection uses,
// so the tray never diverges from the fanout it mirrors. Each descriptor:
//   { id, kind:'subagent'|'bash', name, accent?, action?, time? }
export function liveTrayAgents(session) {
  if (!session) return [];
  const seen = seenJobIdsOf(session.messages);
  const { subs, bash } = liveSubagents(session.subagents, seen);
  const chips = subs.map((sub) => {
    const a = liveAgent(sub, subagentAccentIndex(session.subagents, sub.jobId));
    return { id: a.id, kind: 'subagent', name: a.name, accent: a.accent, action: a.action, time: a.time };
  });
  for (const job of bash) {
    const chip = { id: job.jobId, kind: 'bash', name: 'bash', action: bashAction(job) };
    const time = job.usage && formatElapsed(job.usage.elapsedMs);
    if (time) chip.time = time;
    chips.push(chip);
  }
  return chips;
}

// bashAction is the tray's short description of a live bash job: the command.
function bashAction(job) {
  return toolPath('bash', { command: job.task || '' }) || firstLine(job.task) || 'running';
}

function joinText(content) {
  if (!Array.isArray(content)) return '';
  return content
    .filter(c => c && c.type === 'text' && c.text)
    .map(c => c.text)
    .join('');
}

function attachmentsOf(content) {
  if (!Array.isArray(content)) return [];
  return content.filter(c => c && c.type && c.type !== 'text');
}

function firstLine(str) {
  if (!str) return '';
  const nl = str.indexOf('\n');
  return nl < 0 ? str : str.slice(0, nl);
}

// shortLabel trims a single-line label to a readable length for fanout names.
function shortLabel(str, max = 40) {
  if (!str) return '';
  return str.length > max ? str.slice(0, max - 1) + '…' : str;
}

// mapStatus normalizes a tool_start status to the LedgerRow t-out class.
function mapStatus(status) {
  if (status === 'error') return 'err';
  if (status === 'rejected') return 'warn';
  return 'ok'; // done, running, generating
}

// toLedgerRow builds one LedgerRow prop object from a tool_start message. This
// is also the path terminated subagent/bash cards take (they arrive as
// tool_start rows in messages), so a subagent/bash card produces a normal,
// legible ledger row: arg = first line of task/command, out = summary.
function toLedgerRow(msg) {
  const name = msg.tool_name || 'tool';
  const status = mapStatus(msg.status);
  const preview = toolPreview(name, msg.args, msg.result, msg.status, msg.start_line);
  // A diff preview is rendered as its own sibling block, so it is not the row body.
  const body = preview && preview.kind !== 'diff' ? truncateText(preview.text) : undefined;
  const row = {
    tool: toolToken(name),
    arg: { text: toolPath(name, msg.args) },
    out: deriveOut(msg),
    status,
    id: msg.tool_call_id,
  };
  if (body) row.body = body;
  // The tool currently in flight (status running/generating) is marked `live`
  // so ActivityLedger/MobileLedger render it as the "B·Tail" live line (verb +
  // object + caret + elapsed) instead of a normal terminated row.
  if (msg.status === 'running' || msg.status === 'generating') {
    row.live = true;
    row.startedAt = msg.startedAt || null;
  }
  return row;
}

// toolToken maps a raw tool name to the token LedgerRow uses for its icon/label
// (the keys of TOOL_ICONS). Unknown tools fall through as their lowercase name.
function toolToken(name) {
  return (name || 'tool').toLowerCase();
}

// deriveOut produces the short right-aligned summary shown on a ledger row.
function deriveOut(msg) {
  if (msg.status === 'error') return 'error';
  if (msg.status === 'rejected') return 'rejected';
  if (msg.status === 'running' || msg.status === 'generating') return '';
  const result = msg.result;
  if (!result) return '';
  const lines = String(result).split('\n');
  if (lines.length > 1) return `${lines.length} lines`;
  return truncateText(lines[0], 40);
}

// isUnifiedDiff decides whether a diff string is a REAL unified diff that
// DiffBlock.parseUnifiedDiff can render — i.e. it has a `@@` hunk header or the
// `--- ` file header. The formatDiff fallback (line-numbered ` - `/` + `) is
// NOT unified and would parse to an empty diff, so we reject it here.
function isUnifiedDiff(text) {
  if (!text) return false;
  if (text.startsWith('--- ')) return true;
  return /(^|\n)@@/.test(text);
}

// toDiffBlock returns a full-width diff sibling for an edit that carries a real
// server unified diff, or null. It reuses toolPreview to detect a diff preview,
// but only emits a block when the text is an actual unified diff (guarding
// against the non-parseable formatDiff fallback).
function toDiffBlock(msg) {
  const name = msg.tool_name || '';
  const preview = toolPreview(name, msg.args, msg.result, msg.status, msg.start_line);
  if (!preview || preview.kind !== 'diff') return null;
  if (!isUnifiedDiff(preview.text)) return null;
  const args = typeof msg.args === 'string' ? tryParse(msg.args) : (msg.args || {});
  return {
    type: 'diff',
    filename: (args && args.path) || '',
    diffText: preview.text,
    startLine: msg.start_line,
  };
}

function tryParse(s) {
  try { return JSON.parse(s); } catch { return null; }
}

// toFileBlock returns a full-width file-card sibling for a finished send_file
// tool call whose result ends in a valid JSON file descriptor (see
// data/util/file-card.js#parseFileCardData), or null. Mirrors toDiffBlock:
// only emitted for the DONE status (errors keep their raw ledger text so the
// failure reason stays visible).
function toFileBlock(msg) {
  const name = (msg.tool_name || '').toLowerCase();
  if (name !== 'send_file' || msg.status !== 'done') return null;
  const data = parseFileCardData(msg.result);
  if (!data) return null;
  return {
    type: 'file',
    file: { name: data.name, size: data.size, mime: data.mime, url: data.url },
  };
}

// liveSubagents divides session.subagents (a map keyed by job_id, or an array)
// into the LIVE real subagents and LIVE async bash jobs (kind:'bash'). Only
// entries with status running/cancelling are kept — terminated ones already
// live in session.messages — and anything whose job_id already appeared in
// messages is dropped for robustness (dedup). Exported so the AgentTray (5J)
// derives its live chips from the SAME rule the stream uses, rather than
// duplicating "which subagents are alive" logic.
export function liveSubagents(subagents, seenJobIds) {
  const subs = [];
  const bash = [];
  if (!subagents) return { subs, bash };
  const list = Array.isArray(subagents) ? subagents : Object.values(subagents);
  for (const s of list) {
    if (!s) continue;
    if (s.status !== 'running' && s.status !== 'cancelling') continue;
    if (s.jobId && seenJobIds && seenJobIds.has(String(s.jobId))) continue;
    if (s.kind === 'bash') bash.push(s);
    else subs.push(s);
  }
  return { subs, bash };
}

// agentAction describes what a running subagent is doing right now: the last
// in-flight tool, or a generic "Working" when only text is streaming.
function agentAction(sub) {
  const msgs = Array.isArray(sub.messages) ? sub.messages : [];
  for (let i = msgs.length - 1; i >= 0; i--) {
    const m = msgs[i];
    if (m && m._type === 'tool_start' && (m.status === 'running' || m.status === 'generating')) {
      return m.tool_name || 'Working';
    }
  }
  return 'Working';
}

// liveAgent builds one FanoutBlock agent descriptor for a live subagent.
// Shape (see FanoutBlock.jsx): { id, name, accent, state, action?, time? }.
// Exported so the AgentTray/SubagentView derive the SAME descriptor (identity
// accent, live action) instead of reimplementing it.
export function liveAgent(sub, i) {
  const agent = {
    id: sub.jobId,
    name: shortLabel(firstLine(sub.task)) || shortModel(sub.model) || sub.jobId || 'subagent',
    accent: FANOUT_ACCENTS[i % FANOUT_ACCENTS.length],
    state: 'running',
  };
  const action = agentAction(sub);
  if (action) agent.action = action;
  const time = sub.usage && formatElapsed(sub.usage.elapsedMs);
  if (time) agent.time = time;
  return agent;
}

// ── Delegation block (SUBAGENTS-REDESIGN-SPEC §1) ──────────────────────────
// A whole wave of subagents in one assistant turn is projected as a single
// { kind:'delegation' } block: live subs mutate in place and terminated ones
// fold to a ✓/✗ row with a short result chip — instead of the old pile of raw
// subagent ledger rows + system line + toast + fanout. Parent async bash stays
// a `background` block (spec §2); child bash without parent_job_id is an interim
// gap handled in phase 2.

// delegationRunningAgent builds a running agent row from a live subagent.
function delegationRunningAgent(sub, accentIdx) {
  const agent = {
    id: sub.jobId,
    name: shortLabel(firstLine(sub.task)) || shortModel(sub.model) || sub.jobId || 'subagent',
    accent: FANOUT_ACCENTS[accentIdx % FANOUT_ACCENTS.length],
    state: 'running',
    bashJobs: [],
  };
  const action = agentAction(sub);
  if (action) agent.action = action;
  // Prefer the backend's cumulative elapsed; fall back to wall-clock since the
  // job started (usage.elapsedMs isn't populated on live subagent entries).
  const elapsedMs = (sub.usage && sub.usage.elapsedMs) ||
    (sub.startedAtMs ? Date.now() - sub.startedAtMs : 0);
  const time = formatElapsed(elapsedMs);
  if (time) agent.time = time;
  return agent;
}

// delegationDoneAgent builds a terminated agent row from a subagent card
// (tool_name 'subagent', keyed `subagent-<jobId>`) already in messages. `accent`
// comes from session.subagents (the completed entry keeps its accentIndex).
function delegationDoneAgent(msg, accent) {
  const jobId = String(msg.tool_call_id || '').replace(/^subagent-/, '');
  const failed = msg.status === 'error' || msg.status === 'failed' || msg.status === 'cancelled';
  const task = msg.args && msg.args.task ? firstLine(msg.args.task) : '';
  const agent = {
    id: jobId,
    name: shortLabel(task) || jobId || 'subagent',
    accent: accent || FANOUT_ACCENTS[0],
    state: failed ? (msg.status === 'cancelled' ? 'cancelled' : 'failed') : 'done',
    bashJobs: [],
  };
  const chip = msg.result ? shortLabel(firstLine(msg.result), 48) : '';
  if (chip) agent.chip = chip;
  return agent;
}

// delegationSummary counts states for the block header (·N done ·N failed).
function delegationSummary(agents) {
  let done = 0, failed = 0;
  for (const a of agents) {
    if (a.state === 'done') done++;
    else if (a.state === 'failed' || a.state === 'cancelled') failed++;
  }
  return { total: agents.length, done, failed };
}

// finalizeDelegations walks the projected tree, attaches a summary to every
// delegation block, and settles a block (settled:true) only when no agent is
// still running — a settled block auto-collapses to its header line, a live one
// stays expanded (SUBAGENTS-REDESIGN-SPEC §1.3). Delegation blocks are nested
// inside document blocks.
function finalizeDelegations(blocks) {
  for (const b of blocks) {
    // A live turn's document has kind 'streaming' instead of 'document'; both
    // carry inner blocks, so match on the presence of a blocks array.
    if (b && Array.isArray(b.blocks)) {
      for (const inner of b.blocks) {
        if (inner && inner.type === 'delegation') finalizeDelegation(inner);
      }
    }
  }
}

function finalizeDelegation(d) {
  d.summary = delegationSummary(d.agents);
  const anyRunning = d.agents.some((a) => a.state === 'running');
  d.settled = !anyRunning;
}

// backgroundJob builds one BackgroundJob descriptor from a live bash-kind
// subagent. Shape (see BackgroundJob.jsx): { jobId, jobLabel, cmd, progress?,
// elapsed?, lines, live }. `lines` is the output tail as an array of strings.
// `live` is always true here (only running bash reaches this path), so the
// renderer auto-opens the tail and streams new lines in (ticker) without the
// user having to discover the peek toggle.
function backgroundJob(job) {
  const cmd = toolPath('bash', { command: job.task || '' }) || firstLine(job.task);
  const bg = {
    jobId: job.jobId,
    jobLabel: 'BG · JOB',
    cmd,
    lines: bashLines(job),
    live: true,
  };
  const elapsed = job.usage && formatElapsed(job.usage.elapsedMs);
  if (elapsed) bg.elapsed = elapsed;
  return bg;
}

// bashLines returns the last N lines of a bash job's output as a string array.
function bashLines(job) {
  const out = bashOutput(job);
  if (!out) return [];
  const { visible } = splitPreviewTail(out);
  return visible ? visible.split('\n') : [];
}

function bashOutput(job) {
  const msgs = Array.isArray(job.messages) ? job.messages : [];
  const m = msgs.find(x => x && x._type === 'tool_start');
  if (!m) return job.streamingText || '';
  return m.result || m.streamingResult || '';
}
