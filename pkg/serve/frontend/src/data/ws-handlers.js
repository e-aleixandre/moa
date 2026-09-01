// ws-handlers.js — WebSocket event handlers and streaming delta batching

import { triggerAttention, triggerDone, addToast } from './notifications.js';
import { api, acknowledgeVisibleAttentionThrough } from './api.js';
import { store, setState, updateSession, visibleSessionIds } from './store.js';
import { canAppendHistoryDelta, finishHistoryHydration } from './history-hydration.js';
import { newBuffers, applyNestedEvent } from './conversation-reducer.js';
import { truncateText } from './util/format.js';
import { attentionArrival } from './attention-arrivals.js';
import { resetOlderHistory, seedOlderHistory } from './history-paging.js';

// --- Message normalization ---

export function normalizeHistory(raw, liveSubagents = []) {
  const result = [];
  const resultMap = {};
  const legacySubagentJobIds = legacySubagentJobIdsOf(raw, liveSubagents);
  for (const msg of raw) {
    if (msg.role === 'tool_result') {
      resultMap[msg.tool_call_id] = msg;
    }
  }
  for (let index = 0; index < raw.length; index++) {
    const msg = raw[index];
    if (msg.role === 'assistant') {
      const textParts = [];
      for (const c of (msg.content || [])) {
        if (c.type === 'text' && c.text) {
          textParts.push(c.text);
        } else if (c.type === 'tool_call') {
          if (textParts.length > 0) {
            result.push({ role: 'assistant', _msg_id: msg.msg_id, timestamp: msg.timestamp, requested_model: msg.requested_model, model: msg.model, content: [{ type: 'text', text: textParts.join('') }] });
            textParts.length = 0;
          }
          const tr = resultMap[c.tool_call_id];
          let resultText = null;
          let status = 'running';
          if (tr) {
            resultText = (tr.content || []).filter(x => x.type === 'text').map(x => x.text).join('');
            if (tr.custom?.rejected === true) {
              status = 'rejected';
            } else if (tr.is_error) {
              status = 'error';
            } else {
              status = 'done';
            }
          }
          result.push({
            _type: 'tool_start',
            _msg_id: msg.msg_id,
            tool_call_id: c.tool_call_id,
            tool_name: c.tool_name,
            args: c.arguments || {},
            status,
            result: resultText,
            note: extractToolNote(resultText, status === 'rejected'),
            // The subagent tool records the job it spawned on its result: the
            // tool call ID is the provider's, so this is the only link from a
            // restored card to a subagent transcript on disk.
            subagentJobId: tr?.custom?.subagent_job_id || undefined,
            timestamp: tr?.timestamp || msg.timestamp,
          });
        }
      }
      if (textParts.length > 0) {
        result.push({ role: 'assistant', _msg_id: msg.msg_id, timestamp: msg.timestamp, requested_model: msg.requested_model, model: msg.model, content: [{ type: 'text', text: textParts.join('') }] });
      }
    } else if (msg.role === 'shell' || (msg.role === 'user' && msg.custom?.shell)) {
      const text = (msg.content || []).filter(x => x.type === 'text').map(x => x.text).join('');
      const { command, output } = parseShellBody(text);
      result.push({
        _type: 'tool_start',
        _msg_id: msg.msg_id,
        tool_call_id: 'shell_' + (msg.msg_id || index),
        tool_name: 'bash',
        args: { command },
        status: 'done',
        result: output,
      });
    } else if (msg.role === 'goal') {
      // Persistent goal-lifecycle marker (start / iteration verdict / end).
      // Rendered as a system line, matching the live goal event styling.
      const text = (msg.content || []).filter(x => x.type === 'text').map(x => x.text).join('');
      result.push({ _type: 'system', _msg_id: msg.msg_id, text });
    } else if (msg.role === 'session_event' && msg.custom?.type === 'compaction_marker') {
      // Compaction entries are durable tree events, rather than conversational
      // messages. Preserve their complete payload as a first-class normalized
      // row so the stream projection can render the same card after init and
      // when a command event includes the freshly persisted entry.
      result.push({
        _type: 'compaction_marker',
        _msg_id: msg.msg_id,
        timestamp: msg.timestamp,
        summary: typeof msg.custom.summary === 'string' ? msg.custom.summary : '',
        tokensBefore: Number.isFinite(msg.custom.tokens_before) ? msg.custom.tokens_before : 0,
        readFiles: Array.isArray(msg.custom.read_files) ? msg.custom.read_files.filter(f => typeof f === 'string') : [],
        modifiedFiles: Array.isArray(msg.custom.modified_files) ? msg.custom.modified_files.filter(f => typeof f === 'string') : [],
      });
    } else if (msg.role === 'user') {
      if (msg.custom?.source === 'compaction_notice') {
        // moa talking to itself. It rides as a user message because providers
        // accept no other role mid-conversation, but rendering it as one puts
        // a <system-reminder> block in the transcript under the user's name.
        result.push({
          _type: 'system',
          _msg_id: msg.msg_id,
          timestamp: msg.timestamp,
          text: '⚠ Context filling up — asked the agent to save unsaved work',
        });
      } else if (msg.custom?.source === 'secret_batch') {
        result.push({
          _type: 'secret_batch',
          _msg_id: msg.msg_id,
          timestamp: msg.timestamp,
          aliases: Array.isArray(msg.custom.secret_aliases) ? msg.custom.secret_aliases : [],
        });
      } else if (msg.custom?.source === 'subagent') {
        // When a real job ID is available, key the restored card
        // `subagent-<jobId>` so projectStream folds it into the turn's
        // delegation block by that ID. Unmatched legacy cards retain a
        // synthetic key. accentIndex, if saved, keeps the row's color stable
        // across reloads; the projection falls back to a jobId hash otherwise.
        const jobId = msg.custom.subagent_job_id ||
          legacySubagentJobIds.get(subagentTaskIdentity(msg.custom.subagent_task));
        result.push({
          _type: 'tool_start',
            _msg_id: msg.msg_id,
          tool_call_id: jobId ? 'subagent-' + jobId : 'subagent_' + (msg.msg_id || index),
          tool_name: 'subagent',
          args: { task: msg.custom.subagent_task || '' },
          status: subagentRestoreStatus(msg.custom.subagent_status),
          accentIndex: Number.isInteger(msg.custom.subagent_accent_index)
            ? msg.custom.subagent_accent_index
            : undefined,
          result: msg.custom.subagent_status === 'completed' ? (msg.custom.subagent_result || '') : '',
          error: msg.custom.subagent_status === 'failed' ? (msg.custom.subagent_result || '') : '',
          timestamp: msg.timestamp,
        });
      } else if (msg.custom?.source === 'bash_job') {
        const bashText = (msg.content || []).filter(x => x.type === 'text').map(x => x.text).join('');
        result.push({
          _type: 'tool_start',
            _msg_id: msg.msg_id,
          tool_call_id: 'bash_complete_' + (msg.msg_id || index),
          tool_name: 'bash',
          args: { command: msg.custom.bash_command || '' },
          status: (msg.custom.bash_status || '') === 'failed' ? 'error' : 'done',
          result: bashText,
        });
      } else {
        // Backwards compatibility: detect prefix-based notifications
        // from sessions saved before custom metadata was introduced.
        const userText = (msg.content || []).filter(x => x.type === 'text').map(x => x.text).join('');
        const subagent = parseSubagentNotification(userText);
        if (subagent) {
          const jobId = subagent.jobId || legacySubagentJobIds.get(subagentTaskIdentity(subagent.task));
          result.push({
            _type: 'tool_start',
            _msg_id: msg.msg_id,
            tool_call_id: jobId ? 'subagent-' + jobId : 'subagent_' + (msg.msg_id || index),
            subagentJobId: jobId || undefined,
            tool_name: 'subagent',
            args: { task: subagent.task },
            status: subagentRestoreStatus(subagent.status),
            result: subagent.result,
          });
        } else {
          const bash = parseBashNotification(userText);
          if (bash) {
            result.push({
              _type: 'tool_start',
            _msg_id: msg.msg_id,
              tool_call_id: 'bash_complete_' + (msg.msg_id || index),
              tool_name: 'bash',
              args: { command: bash.command },
              status: bash.status === 'failed' ? 'error' : 'done',
              result: userText,
            });
          } else {
            // Preserve the server's msg_id as _msg_id so a later Steered event
            // (seq > snapshot cut) can dedup this same user message by identity
            // instead of appending it a second time.
            result.push(msg.msg_id ? { ...msg, _msg_id: msg.msg_id } : msg);
          }
        }
      }
    }
  }
  return result;
}

// A delta init is a suffix of the durable tree, not a replay stream. Append it
// in place so the cached prefix keeps both its array and row identities; this
// lets stream projection memoization retain the expensive already-rendered
// history. Tool results are special: their matching assistant call may be in
// that prefix, while normalizeHistory normally sees both at once.
export function appendNormalizedHistoryDelta(prefix, raw, liveSubagents = []) {
  const toolResults = new Map();
  for (const msg of raw) {
    if (msg?.role !== 'tool_result' || !msg.tool_call_id) continue;
    const result = (msg.content || []).filter(c => c.type === 'text').map(c => c.text).join('');
    const update = {
      result,
      status: msg.custom?.rejected === true ? 'rejected' : msg.is_error ? 'error' : 'done',
      timestamp: msg.timestamp,
    };
    // Carry the spawned job the same way normalizeHistory does when it sees the
    // call and its result together. A delta splits them: the launch row is
    // already in the cached prefix, so dropping this link leaves that row
    // unmatchable and the turn draws a second, unopenable card for one child.
    const subagentJobId = msg.custom?.subagent_job_id;
    if (subagentJobId) update.subagentJobId = subagentJobId;
    toolResults.set(msg.tool_call_id, update);
  }
  for (let i = 0; i < prefix.length; i++) {
    const row = prefix[i];
    const update = row?._type === 'tool_start' && toolResults.get(row.tool_call_id);
    if (!update) continue;
    prefix[i] = {
      ...row, ...update,
      note: extractToolNote(update.result, update.status === 'rejected'),
      timestamp: update.timestamp || row.timestamp,
    };
  }

  for (const row of normalizeHistory(raw, liveSubagents)) {
    const duplicate = row._type === 'tool_start'
      ? prefix.some(existing => existing?._type === 'tool_start' && existing.tool_call_id === row.tool_call_id)
      : row._msg_id && prefix.some(existing => existing?._type !== 'tool_start' && existing._msg_id === row._msg_id);
    if (!duplicate) prefix.push(row);
  }
  return prefix;
}

// Match an old terminal card to a snapshot job only when the task identifies
// exactly one card and one live job. This lets a legacy card use the same
// canonical key as a current card without suppressing distinct live jobs.
function legacySubagentJobIdsOf(raw, liveSubagents) {
  const historyTaskCounts = new Map();
  for (const msg of (raw || [])) {
    const task = legacySubagentTaskOf(msg);
    if (task) historyTaskCounts.set(task, (historyTaskCounts.get(task) || 0) + 1);
  }

  const liveJobsByTask = new Map();
  for (const subagent of (liveSubagents || [])) {
    if (!subagent || !subagent.job_id ||
        (subagent.status && subagent.status !== 'running' && subagent.status !== 'cancelling')) continue;
    const task = subagentTaskIdentity(subagent.task);
    if (!task) continue;
    const jobs = liveJobsByTask.get(task) || [];
    jobs.push(subagent.job_id);
    liveJobsByTask.set(task, jobs);
  }

  const matched = new Map();
  for (const [task, count] of historyTaskCounts) {
    const jobs = liveJobsByTask.get(task);
    if (count === 1 && jobs?.length === 1) matched.set(task, jobs[0]);
  }
  return matched;
}

function legacySubagentTaskOf(msg) {
  if (!msg || msg.role !== 'user') return '';
  if (msg.custom?.source === 'subagent') {
    return msg.custom.subagent_job_id ? '' : subagentTaskIdentity(msg.custom.subagent_task);
  }
  const text = (msg.content || []).filter(x => x.type === 'text').map(x => x.text).join('');
  return subagentTaskIdentity(parseSubagentNotification(text)?.task);
}

function subagentTaskIdentity(task) {
  return String(task || '').trim();
}

// normalizeConversationProjection adapts the REST transcript DTO used by
// persisted subagents to MessageList's established render model. Tool result
// output is outside the default transcript budget, but action and target are
// retained so persisted activity is as informative as live activity.
export function normalizeConversationProjection(raw, toolDetailBase = '') {
  return (raw || []).map(item => {
    if (item.role === 'tool') {
      const status = item.status === 'ok' ? 'done'
        : item.status === 'pending' ? 'running'
          : item.status || 'running';
      return {
        _type: 'tool_start',
        tool_call_id: item.id,
        tool_name: item.tool || 'tool',
        args: projectionToolArgs(item),
        activity: { action: item.action || '', target: item.target || '' },
        status,
        result: null,
        ...(toolDetailBase && item.id
          ? { detailUrl: `${toolDetailBase}?detail=full&item_id=${encodeURIComponent(item.id)}` }
          : {}),
      };
    }
    if (item.role === 'compaction_summary') {
      // The child's compaction reaches the stream as the same marker the parent
      // emits, so both render one card instead of leaving an unexplained gap.
      // Children persist only the summary text — no token or file counts — and
      // the card already omits what is absent.
      return {
        _type: 'compaction_marker',
        _msg_id: item.id,
        summary: item.text || '',
      };
    }
    return {
      role: item.role,
      _msg_id: item.id,
      content: item.text ? [{ type: 'text', text: item.text }] : [],
      ...(item.source ? { custom: { source: item.source } } : {}),
    };
  });
}

function projectionToolArgs(item) {
  const target = item.target || '';
  if (target.startsWith('{')) {
    try {
      const args = JSON.parse(target);
      if (args && typeof args === 'object' && !Array.isArray(args)) return args;
    } catch { /* truncated JSON remains useful as a display target below */ }
  }
  if (!target) return {};
  switch (item.tool) {
    case 'bash': return { command: target };
    case 'edit':
    case 'write': return { path: target };
    case 'fetch_content': return { url: target };
    case 'subagent': return { task: target };
    case 'web_search': return { query: target };
    default: return { target };
  }
}

function parseShellBody(body) {
  if (!body.startsWith('$ ')) return { command: '', output: body };
  const rest = body.slice(2);
  const nl = rest.indexOf('\n');
  if (nl < 0) return { command: rest, output: '' };
  const command = rest.slice(0, nl);
  let output = rest.slice(nl + 1);
  if (output === '(no output)') output = '';
  return { command, output };
}

function extractToolNote(result, rejected) {
  const text = (result || '').trim();
  if (!text) return null;

  if (rejected) {
    let reason = text;
    if (reason.startsWith('Error: ')) reason = reason.slice('Error: '.length);
    if (reason.startsWith('Permission denied: ')) reason = reason.slice('Permission denied: '.length);
    reason = reason.trim();
    if (!reason || reason === 'denied by user') return 'Rejected';
    return `Rejected reason: ${reason}`;
  }

  const marker = 'Permission feedback:';
  const idx = text.lastIndexOf(marker);
  if (idx < 0) return null;
  const fb = text.slice(idx + marker.length).trim();
  if (!fb) return null;
  return `Feedback: ${fb}`;
}

// --- Streaming delta batching ---

const pendingTextDeltas = {};
const pendingThinkingDeltas = {};
const pendingToolDeltas = {};
const pendingBashDeltas = {}; // sessionId → { jobId → { delta, ownerAgentId } }
const pendingToolCallBuffers = {}; // sessionId → { toolCallId → { args } }
const materializedTextDuringMessage = {};
let flushScheduled = false;

function scheduleFlush() {
  if (flushScheduled) return;
  flushScheduled = true;
  requestAnimationFrame(flushDeltas);
}

function flushDeltas() {
  flushScheduled = false;
  const state = store.get();

  const sessionIds = new Set([
    ...Object.keys(pendingTextDeltas),
    ...Object.keys(pendingThinkingDeltas),
    ...Object.keys(pendingToolDeltas),
    ...Object.keys(pendingBashDeltas),
  ]);

  const patches = {};
  for (const id of sessionIds) {
    const sess = state.sessions[id];
    if (!sess) {
      delete pendingTextDeltas[id];
      delete pendingThinkingDeltas[id];
      delete pendingToolDeltas[id];
      delete pendingBashDeltas[id];
      continue;
    }
    const patch = {};

    if (pendingTextDeltas[id]) {
      patch.streamingText = (sess.streamingText || '') + pendingTextDeltas[id];
      delete pendingTextDeltas[id];
    }

    if (pendingThinkingDeltas[id]) {
      patch.thinkingText = (sess.thinkingText || '') + pendingThinkingDeltas[id];
      delete pendingThinkingDeltas[id];
    }

    if (pendingToolDeltas[id]) {
      let messages = patch.messages || sess.messages;
      let changed = false;
      for (const [toolCallId, delta] of Object.entries(pendingToolDeltas[id])) {
        messages = messages.map(m => {
          if (m._type === 'tool_start' && m.tool_call_id === toolCallId) {
            changed = true;
            return { ...m, streamingResult: (m.streamingResult || '') + delta };
          }
          return m;
        });
      }
      if (changed) patch.messages = messages;
      delete pendingToolDeltas[id];
    }

    if (pendingBashDeltas[id]) {
      const subagents = { ...(patch.subagents || sess.subagents || {}) };
      for (const [jobId, pending] of Object.entries(pendingBashDeltas[id])) {
        const existing = subagents[pending.ownerAgentId || jobId];
        if (!existing) continue;
        const messages = existing.messages.map(m => m._type === 'tool_start' && m.tool_call_id === jobId
          ? { ...m, streamingResult: (m.streamingResult || '') + pending.delta }
          : m);
        subagents[pending.ownerAgentId || jobId] = {
          ...existing,
          messages,
        };
      }
      patch.subagents = subagents;
      delete pendingBashDeltas[id];
    }

    if (Object.keys(patch).length > 0) {
      patches[id] = patch;
    }
  }
  if (Object.keys(patches).length > 0) {
    const sessions = { ...state.sessions };
    for (const [id, patch] of Object.entries(patches)) {
      sessions[id] = { ...sessions[id], ...patch };
    }
    setState({ sessions });
  }
}

// --- WS event handlers ---

function parseAttentionNamespace(namespace) {
  const separator = typeof namespace === 'string' ? namespace.lastIndexOf(':') : -1;
  if (separator <= 0) return null;
  const incarnationText = namespace.slice(separator + 1);
  if (!/^\d+$/.test(incarnationText)) return null;
  const incarnation = Number(incarnationText);
  if (!Number.isSafeInteger(incarnation) || incarnation < 1) return null;
  return { serverInstance: namespace.slice(0, separator), incarnation };
}

// An init is usable for the cursor only when its namespace is well-formed and
// belongs to the runtime that sent it. This keeps malformed cursor data from
// becoming either a read boundary or a future frame's namespace.
export function attentionNamespaceFromInit(data) {
  const namespace = data?.attention_namespace;
  const parsed = parseAttentionNamespace(namespace);
  if (!parsed || parsed.serverInstance !== data?.server_instance) return '';
  return namespace;
}

// The namespace is an ordered runtime incarnation, not an opaque token: a
// delayed roster response for A must not reset state after the client accepted
// B. Different server processes are unordered, so only the roster may move
// between them; a socket may advance incarnations within its own process.
export function attentionNamespaceTransition(session, namespace, { allowCrossProcess = true } = {}) {
  const current = session?.attentionNamespace || '';
  const next = parseAttentionNamespace(namespace);
  if (!next) return { accepted: false, reset: false, namespace: current };
  if (!current) return { accepted: true, reset: false, namespace };
  const previous = parseAttentionNamespace(current);
  if (!previous) return { accepted: false, reset: false, namespace: current };
  if (current === namespace) return { accepted: true, reset: false, namespace };
  if (previous.serverInstance !== next.serverInstance) {
    return allowCrossProcess
      ? { accepted: true, reset: true, namespace }
      : { accepted: false, reset: false, namespace: current };
  }
  if (next.incarnation > previous.incarnation) return { accepted: true, reset: true, namespace };
  return { accepted: false, reset: false, namespace: current };
}

// Apply an accepted socket namespace transition before its init or frames use
// the cursor. An init is fresher than the roster, but follows the same ordered
// incarnation rule.
export function adoptAttentionNamespace(id, namespace) {
  const session = store.get().sessions[id];
  const transition = attentionNamespaceTransition(session, namespace);
  if (!transition.accepted) return transition;
  if (transition.reset) {
    updateSession(id, {
      attentionNamespace: transition.namespace,
      unseen: false,
      unseenSeq: 0,
      ackedThroughSeq: 0,
      readCandidateSeq: 0,
    });
  } else if (session?.attentionNamespace !== transition.namespace) {
    updateSession(id, { attentionNamespace: transition.namespace });
  }
  return transition;
}

// mergeSteers reconciles the authoritative server queue from an init snapshot
// with any local optimistic chips. The snapshot (each item carrying its
// client-minted ID) is authoritative. A local chip is kept only if it is still
// in flight (its POST hasn't returned, so confirmed !== true) and not already
// in the snapshot: that covers a steer sent moments before the cut. A confirmed
// chip absent from the snapshot was delivered or cancelled server-side, so it is
// dropped rather than resurrected.
function mergeSteers(snapshot, local) {
  // Snapshot steers are authoritative and already accepted by the server, so
  // they are confirmed: a later snapshot that omits them means delivered/
  // cancelled, not in-flight. command/images ride along so a reconnect restores
  // a queued command barrier as a command chip and badges an image message.
  const server = (snapshot || []).map(s => ({
    id: s.id,
    text: s.text,
    command: !!s.command,
    images: s.images || 0,
    confirmed: true,
  }));
  const serverIds = new Set(server.map(s => s.id));
  const inFlightLocal = (local || []).filter(s => s && s.id && s.confirmed !== true && !serverIds.has(s.id));
  const merged = [...server, ...inFlightLocal];
  return merged.length > 0 ? merged : null;
}

// runEpoch counts the WS writes that touch the live per-run fields (state,
// runStartedAtMs, runTokensUp/Down). sendMessage snapshots it before its
// optimistic patch so a send that turned out to be a steer can tell "nothing
// happened during the POST" from "a real run's events landed meanwhile" —
// values alone can't, a brand new run reports 0/0 too.
function nextRunEpoch(id) {
  return (store.get().sessions[id]?.runEpoch || 0) + 1;
}

export function handleWsInit(id, data) {
  delete pendingTextDeltas[id];
  delete pendingThinkingDeltas[id];
  delete pendingToolDeltas[id];
  delete pendingBashDeltas[id];
  delete pendingToolCallBuffers[id];
  delete pendingSubagentEvents[id];
  for (const key of Object.keys(subagentBuffers)) {
    if (key.startsWith(`${id}:`)) delete subagentBuffers[key];
  }
  delete materializedTextDuringMessage[id];
  // A finished subagent opened from its card lives only in local state: the
  // init snapshot lists live jobs only, so replacing the map outright would
  // delete the very transcript being read and bounce the reader back to the
  // parent — which is what happens on mobile every time the screen sleeps.
  const namespace = attentionNamespaceFromInit(data);
  const cursorTransition = namespace
    ? attentionNamespaceTransition(store.get().sessions[id], namespace)
    : { accepted: false, reset: false, namespace: store.get().sessions[id]?.attentionNamespace || '' };
  const prev = store.get().sessions[id] || {};
  const serverInstance = data.server_instance || prev.serverInstance || '';
  const viewing = prev.viewingSubagent;
  const keptLocal = viewing && prev.subagents && prev.subagents[viewing]
    && !(data.subagents || []).some(sa => sa && sa.job_id === viewing)
    ? { [viewing]: prev.subagents[viewing] }
    : null;
  // Same protection for a background bash being read in the BashJobView: once
  // the job ends the server may drop it from the bash_jobs snapshot, and
  // wiping its entry would eject the reader mid-read (a reconnect happens on
  // every mobile screen sleep, precisely while watching a long job).
  const viewingBash = prev.viewingBashJob;
  const keptBash = viewingBash && prev.subagents && prev.subagents[viewingBash]
    && !(data.bash_jobs || []).some(bj => bj && bj.job_id === viewingBash)
    ? { [viewingBash]: prev.subagents[viewingBash] }
    : null;
  const canAppendDelta = !!data.delta_base && canAppendHistoryDelta(prev.messages || [], data.delta_base);
  let messages = canAppendDelta
    ? withLiveToolsInPlace(appendNormalizedHistoryDelta(prev.messages, data.messages || [], data.subagents), data.live_tools)
    : withLiveTools(normalizeHistory(data.messages || [], data.subagents), data.live_tools);
  // The live init snapshot cannot contain terminal jobs, so restore their
  // persisted lifecycle cards separately. Upserting by job ID also upgrades
  // old notification-derived cards to the real terminal result/error.
  for (const outcome of chronologicalSubagentOutcomes(data.subagent_outcomes)) {
    messages = upsertTerminalSubagentOutcome(messages, {
      task: outcome.task || '', accentIndex: outcome.accent_index,
    }, outcome);
  }
  updateSession(id, {
    serverInstance,
    messages,
    historyTruncated: !!data.history_truncated,
    state: data.state || 'idle',
    contextPercent: data.context_percent ?? -1,
    contextWindow: data.context_window || 0,
    compactAt: data.compact_at || 0,
    compactAtMin: data.compact_at_min || 0,
    permissionMode: data.permission_mode || 'yolo',
    fast: !!data.fast,
    fastSupported: !!data.fast_supported,
    fastNote: data.fast_note || '',
    pendingPerm: data.pending_permission || null,
    pendingAsk: data.pending_ask || null,
    // The server's steer queue is authoritative and shared across all of this
    // session's clients. The snapshot replaces the queue; a local chip is kept
    // only if its client-minted ID is not yet in the snapshot (its POST was
    // still in flight when the cut was taken) so a just-sent steer isn't lost.
    pendingSteers: mergeSteers(data.pending_steers, store.get().sessions[id]?.pendingSteers),
    // Restore the in-flight streamed reply from the snapshot so a reconnect
    // during generation shows the whole partial message, not just the deltas
    // that land after the cut. Empty when nothing is streaming.
    streamingText: data.streaming_text || null,
    thinkingText: data.streaming_thinking || null,
    // Reconnect-safe elapsed counter: the server anchors the run-start time so
    // the activity indicator keeps counting from the real start, not from the
    // moment this pane reconnected. Null when idle.
    runStartedAtMs: data.run_started_at_ms || null,
    // Authoritative compacting flag from the snapshot: if the compaction
    // finished while this pane had no WS, the stale local spinner is cleared;
    // if one is still running, it is restored.
    compacting: !!data.compacting,
    // The auto-verify may have ended while this client had no socket, so the
    // snapshot must replace the stale local indicator with server truth.
    autoVerifying: !!data.auto_verifying,
    tasks: data.tasks || [],
    costUSD: data.cost_usd || 0,
    // Logical per-run traffic is authoritative in every init snapshot, so a
    // reconnect replaces stale local totals even when the run is already idle.
    runTokensUp: data.run_tokens_up || 0,
    runTokensDown: data.run_tokens_down || 0,
    runEpoch: nextRunEpoch(id),
    subagents: {
      ...(keptLocal || {}),
      ...(keptBash || {}),
      ...initBashJobs(data.bash_jobs, initSubagents(data.subagents)),
    },
    // subagentCount is otherwise live-only (WS subagent_count events). If an
    // async job finished while this pane had no WS (backgrounded on mobile),
    // that terminal count=0 event was missed and the badge/dot would stay
    // stuck. The init snapshot's data.subagents lists only *live* jobs
    // (running/cancelling), so recompute the authoritative async count from it.
    subagentCount: (data.subagents || []).filter(
      sa => sa && sa.async && (sa.status === 'running' || sa.status === 'cancelling')
    ).length,
    goalActive: !!data.goal_active,
    goalObjective: data.goal_active ? (data.goal_objective || '') : null,
    goalWorkDir: data.goal_active ? (data.goal_work_dir || '') : null,
    goalIteration: data.goal_iteration || 0,
    goalStalled: data.goal_stalled || 0,
    goalVerifying: !!data.goal_verifying,
    lastSeq: data.last_seq || 0,
  });
  if (!data.delta_base) seedOlderHistory(id, data.history_before);
  if (cursorTransition.accepted) {
    updateSession(id, { readCandidateSeq: data.last_seq || 0 });
  }
  acknowledgeInitAttention(id, data, namespace);
}

// An authoritative init is the read boundary: the selected session showed the
// snapshot this cursor belongs to. No presentation proof beyond that —
// selection plus a confirmed init plus a foregrounded tab is the whole
// contract.
function acknowledgeInitAttention(id, data, namespace) {
  finishHistoryHydration(id, { shown: true });
  if (namespace && visibleSessionIds(store.get()).includes(id)) {
    acknowledgeVisibleAttentionThrough(id, data.last_seq || 0, namespace).catch(() => {});
  }
}

// withLiveTools appends the tool calls that were in flight when the snapshot
// was cut. Such a call is in no message history yet — a tool call is written to
// history when its assistant message closes, its result when the tool ends — so
// a client that switches conversations and comes back would otherwise rebuild a
// row it can't name (liveVerb falls back to 'Calling') or lose it entirely for
// a long-running bash.
//
// Deduped by tool_call_id against the rebuilt history AND against itself, so a
// snapshot that overlaps history never doubles a row. Live events landing after
// the snapshot reconcile the same way: handleWsToolCallStart/handleWsToolStart
// look the ID up and patch the existing row instead of appending a second one.
function withLiveTools(messages, liveTools) {
  if (!Array.isArray(liveTools) || liveTools.length === 0) return messages;
  const byId = new Map();
  for (const t of liveTools) {
    if (t && t.tool_call_id && !byId.has(t.tool_call_id)) byId.set(t.tool_call_id, t);
  }
  // A call whose assistant message already closed IS in history (as a tool_call
  // with no result yet), so patch that row rather than appending a twin: it
  // gains the server's start anchor, and its phase comes from the authoritative
  // registry instead of history's "no result ⇒ running" guess.
  const out = messages.map(m => {
    if (!m || m._type !== 'tool_start') return m;
    const t = byId.get(m.tool_call_id);
    if (!t) return m;
    byId.delete(m.tool_call_id);
    return { ...m, ...liveToolRow(t), tool_name: t.tool_name || m.tool_name, args: t.args || m.args };
  });
  for (const t of byId.values()) out.push(liveToolRow(t));
  return out;
}

function withLiveToolsInPlace(messages, liveTools) {
  const reconciled = withLiveTools(messages, liveTools);
  if (reconciled !== messages) messages.splice(0, messages.length, ...reconciled);
  return messages;
}

function liveToolRow(t) {
  return {
    _type: 'tool_start',
    tool_call_id: t.tool_call_id,
    tool_name: t.tool_name || '',
    args: t.args || {},
    status: t.status === 'generating' ? 'generating' : 'running',
    result: null,
    // Server-anchored so the row's elapsed timer resumes from the real start
    // instead of restarting at the moment this pane reconnected.
    startedAt: t.started_at_ms || Date.now(),
  };
}

// initSubagents builds the session.subagents map from a WS init snapshot
// (live subagents + their transcript so far), normalizing each transcript the
// same way the main conversation history is normalized.
function initSubagents(raw) {
  const out = {};
  for (const sa of (raw || [])) {
    if (!sa || !sa.job_id) continue;
    out[sa.job_id] = {
      jobId: sa.job_id,
      originToolCallId: sa.origin_tool_call_id || '',
      task: sa.task || '',
      model: sa.model || '',
      thinking: sa.thinking || 'off',
      status: sa.status || 'running',
      async: !!sa.async,
      messages: normalizeHistory(sa.messages || []),
      streamingText: null,
      thinkingText: null,
      // Reconnect-safe: preserve the started-at anchor and accumulated usage
      // from the snapshot so a reconnected pane doesn't reset the subagent's
      // elapsed timer or token/cost tally back to nothing.
      startedAtMs: sa.started_at_ms || null,
      usage: (sa.input_tokens || sa.output_tokens || sa.cost_usd)
        ? { inputTokens: sa.input_tokens || 0, outputTokens: sa.output_tokens || 0, costUSD: sa.cost_usd || 0 }
        : null,
      contextPercent: sa.context_percent == null ? -1 : sa.context_percent,
      // Stable per-session creation ordinal from the server, so the accent
      // color derived from it survives WS reconnects (see stream-model.js's
      // subagentAccentIndex). Undefined when the server didn't send one
      // (older payload/specimen), falling back to position-based derivation.
      accentIndex: sa.accent_index,
    };
  }
  return out;
}

function initBashJobs(raw, existing = {}) {
  let out = { ...existing };
  for (const job of (raw || [])) {
    if (!job || !job.job_id) continue;
    out = attachBashJob(out, job);
  }
  return out;
}

function bashJobState(job, existing = null) {
  const command = job.command || existing?.task || '';
  const output = job.output || '';
  return {
    jobId: job.job_id,
    ownerAgentId: job.owner_agent_id || '',
    task: command,
    model: 'bash',
    kind: 'bash',
    cwd: job.cwd || existing?.cwd || '',
    status: job.status || existing?.status || 'running',
    async: true,
    messages: [{
      _type: 'tool_start', tool_call_id: job.job_id, tool_name: 'bash',
      args: { command, cwd: job.cwd || existing?.cwd || '' },
      status: (job.status === 'completed') ? 'done' : (job.status && job.status !== 'running' && job.status !== 'cancelling') ? 'error' : 'running',
      result: output || null,
      streamingResult: (job.status === 'running' || job.status === 'cancelling') ? output : null,
    }],
    streamingText: null, thinkingText: null, usage: null,
  };
}

function bashToolMessage(job, existing = null) {
  const command = job.command || existing?.args?.command || '';
  const status = job.status || existing?.status || 'running';
  const output = job.output || existing?.result || null;
  return {
    _type: 'tool_start', tool_call_id: job.job_id, tool_name: 'bash',
    args: { command, cwd: job.cwd || existing?.args?.cwd || '' },
    status: status === 'completed' ? 'done' : (status !== 'running' && status !== 'cancelling') ? 'error' : 'running',
    result: output,
    streamingResult: (status === 'running' || status === 'cancelling') ? (existing?.streamingResult || output) : null,
  };
}

function emptyOwnedSubagent(jobId, bashStatus = 'running') {
  return {
    jobId, task: '', model: '',
    status: bashStatus === 'running' || bashStatus === 'cancelling' ? 'running' : bashStatus,
    async: true, syntheticOwnedBashOwner: true,
    messages: [], streamingText: null, thinkingText: null, usage: null,
  };
}

// attachBashJob keeps root jobs as their own live entries, but puts an owned
// job's tool row directly in its launching subagent's transcript.
function attachBashJob(subagents, job) {
  const out = { ...subagents };
  const ownerJobId = job.owner_agent_id || '';
  if (!ownerJobId) {
    out[job.job_id] = bashJobState(job, out[job.job_id]);
    return out;
  }
  const owner = out[ownerJobId] || emptyOwnedSubagent(ownerJobId, job.status);
  const messages = [...(owner.messages || [])];
  const idx = messages.findIndex(m => m._type === 'tool_start' && m.tool_call_id === job.job_id);
  const message = bashToolMessage(job, idx >= 0 ? messages[idx] : null);
  if (idx >= 0) messages[idx] = message;
  else messages.push(message);
  out[ownerJobId] = {
    ...owner,
    // A real owner retains its own lifecycle. A placeholder only exists for
    // the start-before-subagent race, so its terminal bash is its last known
    // activity and must not leave a permanent live chip behind.
    status: owner.syntheticOwnedBashOwner && job.status && job.status !== 'running' && job.status !== 'cancelling'
      ? job.status
      : owner.status,
    messages,
  };
  return out;
}


export function handleWsMessageStart(id) {
  delete pendingTextDeltas[id];
  delete pendingThinkingDeltas[id];
  delete materializedTextDuringMessage[id];
  if (!store.get().sessions[id]) return;
  updateSession(id, {
    streamingText: null,
    thinkingText: null,
  });
}

export function handleWsTextDelta(id, delta) {
  if (!store.get().sessions[id]) return;
  pendingTextDeltas[id] = (pendingTextDeltas[id] || '') + delta;
  scheduleFlush();
}

export function handleWsThinkingDelta(id, delta) {
  if (!store.get().sessions[id]) return;
  pendingThinkingDeltas[id] = (pendingThinkingDeltas[id] || '') + delta;
  scheduleFlush();
}

export function handleWsMessageEnd(id, fullText, msgId = '') {
  const pendingText = pendingTextDeltas[id] || '';
  delete pendingTextDeltas[id];
  delete pendingThinkingDeltas[id];
  const sess = store.get().sessions[id];
  if (!sess) {
    delete materializedTextDuringMessage[id];
    return;
  }

  if (msgId && sess.messages.some(m => m._msg_id === msgId)) {
    delete materializedTextDuringMessage[id];
    updateSession(id, { streamingText: null, thinkingText: null });
    return;
  }

  // fullText is authoritative: it repairs deltas dropped under bus
  // backpressure and clients that connected mid-message. When tool calls
  // already materialized part of the text, derive the remaining tail from
  // fullText (it concatenates all text blocks with no separator); if they
  // diverge — a delta was lost before materializing — fall back to the
  // streamed tail rather than duplicate text.
  const streamed = (sess.streamingText || '') + pendingText;
  const materialized = materializedTextDuringMessage[id] || '';
  let assistantText;
  if (!materialized) {
    assistantText = fullText || streamed;
  } else if (fullText && fullText.startsWith(materialized)) {
    assistantText = fullText.slice(materialized.length);
  } else {
    assistantText = streamed;
  }

  const patch = {
    streamingText: null,
    thinkingText: null,
  };
  if (assistantText) {
    const msg = { role: 'assistant', _msg_id: msgId || undefined, content: [{ type: 'text', text: assistantText }] };
    patch.messages = [...sess.messages, msg];
  }

  delete materializedTextDuringMessage[id];
  updateSession(id, patch);
}

export function handleWsRunTokens(id, data) {
  updateSession(id, { runTokensUp: data.up || 0, runTokensDown: data.down || 0, runEpoch: nextRunEpoch(id) });
}

export function handleWsSubagentTitle(id, data) {
  const sess = store.get().sessions[id];
  if (!sess || !data?.job_id || !data?.title) return;
  const existing = sess.subagents?.[data.job_id] || { jobId: data.job_id };
  updateSession(id, { subagents: { ...sess.subagents, [data.job_id]: { ...existing, title: data.title } } });
}

export function handleWsToolCallStart(id, data) {
  const sess = store.get().sessions[id];
  if (!sess) return;

  // Check if a fallback block already exists (for missed/reconnected streams).
  const existingIdx = (sess.messages || []).findIndex(
    m => m._type === 'tool_start' && m.tool_call_id === data.tool_call_id,
  );

  if (existingIdx >= 0) {
    // Don't downgrade status — only update if still generating or missing.
    const existing = sess.messages[existingIdx];
    if (existing.status !== 'generating') return; // already advanced past generating
    const messages = sess.messages.map((m, idx) => {
      if (idx !== existingIdx) return m;
      return { ...m, tool_name: data.tool_name };
    });
    updateSession(id, { messages });
    return;
  }

  // Materialize streaming text before tool block (match TUI ordering).
  const pendingText = pendingTextDeltas[id] || '';
  const pendingThinking = pendingThinkingDeltas[id] || '';
  delete pendingTextDeltas[id];
  delete pendingThinkingDeltas[id];

  const messages = [...sess.messages];
  const patch = {};
  const textToMaterialize = (sess.streamingText || '') + pendingText;
  const thinkingToClear = (sess.thinkingText || '') + pendingThinking;
  if (textToMaterialize) {
    messages.push({
      role: 'assistant',
      content: [{ type: 'text', text: textToMaterialize }],
    });
    patch.streamingText = null;
    // Accumulate the materialized text (a message may materialize across several
    // tool calls) so message_end can derive the remaining tail from fullText.
    // Storing a boolean here would break that: startsWith/slice treat it as the
    // string "true", disabling the repair and duplicating text that starts with
    // "true".
    materializedTextDuringMessage[id] = (materializedTextDuringMessage[id] || '') + textToMaterialize;
  }
  if (thinkingToClear) {
    patch.thinkingText = null;
  }

  // Check if we have buffered args from early deltas.
  const buffered = pendingToolCallBuffers[id]?.[data.tool_call_id];

  messages.push({
    _type: 'tool_start',
    tool_call_id: data.tool_call_id,
    tool_name: data.tool_name,
    args: buffered?.args || {},
    status: 'generating',
    result: null,
    // Anchor for the live-row elapsed timer — set once, at the
    // earliest moment this tool call exists, and carried through the
    // generating→running transition below.
    startedAt: Date.now(),
  });

  if (buffered) {
    delete pendingToolCallBuffers[id][data.tool_call_id];
    if (Object.keys(pendingToolCallBuffers[id]).length === 0) {
      delete pendingToolCallBuffers[id];
    }
  }

  updateSession(id, { ...patch, messages });
}

export function handleWsToolCallDelta(id, data) {
  const sess = store.get().sessions[id];
  if (!sess) return;

  // Find existing tool block.
  const idx = sess.messages.findIndex(
    m => m._type === 'tool_start' && m.tool_call_id === data.tool_call_id
  );

  if (idx >= 0) {
    // Update args immutably.
    const messages = sess.messages.map((m, i) => {
      if (i !== idx) return m;
      return { ...m, args: data.args };
    });
    updateSession(id, { messages });
  } else {
    // Buffer for later — start event hasn't arrived yet.
    if (!pendingToolCallBuffers[id]) pendingToolCallBuffers[id] = {};
    pendingToolCallBuffers[id][data.tool_call_id] = { args: data.args };
  }
}

export function handleWsToolStart(id, data) {
  const sess = store.get().sessions[id];
  if (!sess) return;

  const existingIdx = (sess.messages || []).findIndex(
    m => m._type === 'tool_start' && m.tool_call_id === data.tool_call_id,
  );

  if (existingIdx >= 0) {
    const messages = sess.messages.map((m, idx) => {
      if (idx !== existingIdx) return m;
      return {
        ...m,
        tool_name: data.tool_name,
        args: data.args,
        start_line: data.start_line,
        status: 'running',
        // Keep the 'generating' phase's startedAt if it already has one.
        startedAt: m.startedAt || Date.now(),
      };
    });
    updateSession(id, { messages, runningTool: data.tool_name });
    return;
  }

  const toolMsg = {
    _type: 'tool_start',
    tool_call_id: data.tool_call_id,
    tool_name: data.tool_name,
    args: data.args,
    start_line: data.start_line,
    status: 'running',
    result: null,
    startedAt: Date.now(),
  };
  updateSession(id, { messages: [...sess.messages, toolMsg], runningTool: data.tool_name });
}

export function handleWsToolUpdate(id, data) {
  if (!store.get().sessions[id]) return;
  if (!pendingToolDeltas[id]) pendingToolDeltas[id] = {};
  pendingToolDeltas[id][data.tool_call_id] =
    (pendingToolDeltas[id][data.tool_call_id] || '') + data.delta;
  scheduleFlush();
}

export function handleWsToolEnd(id, data) {
  if (pendingToolDeltas[id]) {
    delete pendingToolDeltas[id][data.tool_call_id];
    if (Object.keys(pendingToolDeltas[id]).length === 0) delete pendingToolDeltas[id];
  }
  const sess = store.get().sessions[id];
  if (!sess) return;

  const nextStatus = data.rejected === true
    ? 'rejected'
    : (data.is_error ? 'error' : 'done');

  const note = extractToolNote(data.result, data.rejected === true);
  let matched = false;
  const messages = sess.messages.map(m => {
    if (!matched && m._type === 'tool_start' && m.tool_call_id === data.tool_call_id) {
      matched = true;
      return {
        ...m,
        status: nextStatus,
        result: data.result,
        streamingResult: null,
        note,
      };
    }
    return m;
  });
  updateSession(id, { messages, runningTool: null });
}

export function handleWsStateChange(id, data, seq = 0) {
  const state = store.get();
  const prev = state.sessions[id];
  const wasRunning = prev && (prev.state === 'running' || prev.state === 'permission');
  // A compaction settles through the state machine (the bus transitions to
  // error before publishing its terminal event), so a failure arrives here as a
  // plain error state. Read the flag BEFORE the patch clears it, so the toast
  // can name what actually failed instead of blaming "the run".
  const wasCompacting = prev?.compacting === true;
  const patch = { state: data.state, error: data.error || null, runEpoch: nextRunEpoch(id) };
  // Anchor the activity-indicator elapsed counter when a run begins. Only on
  // the transition into a running state, and only if not already set (a reconnect
  // snapshot may have seeded the authoritative server timestamp).
  const nowRunning = data.state === 'running' || data.state === 'permission';
  if (nowRunning && !wasRunning && !prev?.runStartedAtMs) {
    patch.runStartedAtMs = Date.now();
    // A fresh run starts: reset the live per-run token tally so it counts up
    // from zero again. Counts from the previous run persist until this point
    // (not cleared at idle), so the last run's totals stay visible in between.
    patch.runTokensUp = 0;
    patch.runTokensDown = 0;
  }
  updateSession(id, patch);
  if (data.state === 'idle' || data.state === 'error') {
    const sess = store.get().sessions[id];
    // Keep pendingSteers: a steer queued during the last turn stays genuinely
    // queued (mostrar la verdad). It's cleared only by Steered or a snapshot.
    if (sess) updateSession(id, { streamingText: null, thinkingText: null, compacting: false, runStartedAtMs: null });
    if (data.state === 'error' && seq > 0) {
      markUnseen(id, seq, true);
      acknowledgeVisibleLiveAttention(id, seq);
    }
    if (wasRunning) {
      flashSession(id, data.state === 'error' ? 'error' : 'done');
      // A successful/cancelled terminal state is followed by run_end, which
      // owns its authoritative occurrence. Only error state_change is itself
      // the terminal attention event (its run_end reuses that same ID).
      // Surface the reason for an error end so it's visible even when the tile
      // isn't focused — parity with the TUI's run-end error block. A usage/quota
      // limit reads as an actionable "resets in X" line rather than a fault.
      if (data.state === 'error' && data.error) {
        const isQuota = /quota exceeded|usage limit/i.test(data.error);
        let title = 'Run failed';
        if (isQuota) title = 'Usage limit reached';
        else if (wasCompacting) title = 'Compaction failed';
        addToast({
          sessionId: id,
          title,
          detail: data.error,
          type: 'attention',
        });
      }
      const visible = visibleSessionIds(store.get());
      if (!visible.includes(id) && sess) {
        if (data.state === 'error') {
          triggerAttention(sess, null, store.get().soundEnabled);
        } else {
          triggerDone(sess, store.get().soundEnabled);
        }
      }
    }
  }
}

export function handleWsAskUser(id, data, seq = 0) {
  updateSession(id, {
    pendingAsk: { id: data.id, questions: data.questions },
  });
  markUnseen(id, seq, true);
  acknowledgeVisibleLiveAttention(id, seq);
  const state = store.get();
  if (!visibleSessionIds(state).includes(id)) {
    flashSession(id, 'attention');
    const sess = state.sessions[id];
    if (sess) triggerAttention(sess, 'ask_user', state.soundEnabled);
  }
}

export function handleWsPermissionRequest(id, data, seq = 0) {
  updateSession(id, {
    state: 'permission',
    pendingPerm: {
      id: data.id,
      tool_name: data.tool_name,
      args: data.args,
      allow_pattern: data.allow_pattern || '',
    },
  });
  markUnseen(id, seq, true);
  acknowledgeVisibleLiveAttention(id, seq);
  flashSession(id, 'attention');
  const state = store.get();
  if (!visibleSessionIds(state).includes(id)) {
    const sess = state.sessions[id];
    if (sess) triggerAttention(sess, data.tool_name, state.soundEnabled);
  }
}

// A terminal event rendered live in the selected session is itself the read:
// the user was watching that run finish. It must POST /read rather than only
// clearing local state, or the next roster poll surfaces a dot for a result
// this client just showed.
function acknowledgeVisibleLiveAttention(id, seq) {
  const state = store.get();
  if (!visibleSessionIds(state).includes(id)) return;
  const session = state.sessions[id];
  if (seq > 0 && session?.attentionNamespace) {
    acknowledgeVisibleAttentionThrough(id, seq, session.attentionNamespace).catch(() => {});
  }
}

export function handleWsPermissionResolved(id, data) {
  const perm = store.get().sessions[id]?.pendingPerm;
  if (!perm) return;
  if (data?.id && data.id !== perm.id) return;
  updateSession(id, {
    pendingPerm: null,
  });
}

export function handleWsAskResolved(id, data) {
  const ask = store.get().sessions[id]?.pendingAsk;
  if (!ask) return;
  if (data?.id && data.id !== ask.id) return;
  updateSession(id, {
    pendingAsk: null,
  });
}

function flashSession(id, type) {
  updateSession(id, { flash: type });
  setTimeout(() => {
    if (store.get().sessions[id]?.flash === type) updateSession(id, { flash: null });
  }, 1300);
}

// markUnseen flags a session as having unread activity when the user isn't
// currently looking at it (not visible, or the tab is backgrounded), so a badge
// can nudge them back. Cleared by afterVisibilityChange when it comes into view.
function markUnseen(id, seq = 0, isNewOccurrence = false) {
  const state = store.get();
  const hidden = typeof document !== 'undefined' && document.hidden;
  const sess = state.sessions[id];
  if (!sess || seq === 0) return;
  const visible = visibleSessionIds(state).includes(id) && !hidden;
  const unseenSeq = Math.max(sess.unseenSeq || 0, seq);
  if (visible) {
    if (unseenSeq !== (sess.unseenSeq || 0)) updateSession(id, { unseenSeq });
    return;
  }
  const arrival = (isNewOccurrence || !sess.unseen) ? (sess.attentionArrival || 0) + 1 : sess.attentionArrival || 0;
  if (!sess.unseen || arrival !== sess.attentionArrival || unseenSeq !== (sess.unseenSeq || 0)) {
    updateSession(id, { unseen: true, unseenSeq, attentionArrival: arrival });
  }
}

// isSessionAway is true when the user isn't looking at a session (tab hidden or
// the session not on screen) — the same condition markUnseen uses. Toasts for
// in-chat activity (subagent/bash completion) only fire when away, since a
// visible delegation/background block already reports the outcome.
function isSessionAway(id) {
  const state = store.get();
  const hidden = typeof document !== 'undefined' && document.hidden;
  return hidden || !visibleSessionIds(state).includes(id);
}

export function handleWsConfigChange(id, data) {
  const sess = store.get().sessions[id];
  const patch = {
    model: data.model || sess?.model,
    provider: data.provider || sess?.provider,
    thinking: data.thinking || sess?.thinking,
  };
  // Fast mode travels with the model: whether it is on, and whether the model
  // it is now on can serve it at all — a model switch can take the option away.
  if (data.fast !== undefined) patch.fast = data.fast;
  if (data.fast_supported !== undefined) patch.fastSupported = data.fast_supported;
  if (data.fast_note !== undefined) patch.fastNote = data.fast_note;
  if (data.permission_mode) {
    patch.permissionMode = data.permission_mode;
  }
  // compact_at is sent only on a threshold change, and 0 is a real value
  // ("compact at the model window") — so presence, not truthiness, is the test.
  if (data.compact_at != null) {
    patch.compactAt = data.compact_at;
  }
  // A model switch carries the new window, since it is the denominator every
  // context reading uses. Without it the ring and the compaction limit would
  // keep measuring against the window of the model we just left.
  if (data.context_window) {
    patch.contextWindow = data.context_window;
  }
  updateSession(id, patch);
}

export function handleWsContextUpdate(id, data) {
  if (data.context_percent != null) {
    updateSession(id, { contextPercent: data.context_percent });
  }
}

// handleWsMcpChange reflects a live MCP server transition: it refreshes the
// glanceable status-line summary and bumps mcpTick, a monotonic counter an open
// MCP panel watches to re-fetch the full per-server detail (the summary alone
// can't carry per-server state/scope changes).
export function handleWsMcpChange(id, data) {
  const mcp = {
    total: data.total || 0,
    ready: data.ready || 0,
    disabled: data.disabled || 0,
    unhealthy: data.unhealthy || 0,
    pending: data.pending || 0,
  };
  const prev = store.get().sessions[id];
  updateSession(id, { mcp, mcpTick: ((prev && prev.mcpTick) || 0) + 1 });
}

export function handleWsSessionCost(id, data) {
  if (data.cost_usd != null) {
    updateSession(id, { costUSD: data.cost_usd });
  }
}

// handleWsRateLimit reflects a request's live rate-limit headers. OpenAI/Codex
// has no usage endpoint, so its last observed account-wide windows are kept in
// the global snapshot. Anthropic also patches its global plan snapshot here so
// the widget does not lag the 60s poll; extra-usage spend (€) stays poller-owned.
export function handleWsRateLimit(id, data) {
  // Per-session utilizations: always record when the header was present
  // (pct >= 0). This is what the OpenAI widget reads (no global poller), and it
  // keeps each session's meter independent in a mixed-provider layout.
  const patch = { onOverage: !!data.on_overage };
  if (data.five_hour_pct >= 0) patch.rlFiveHourPct = data.five_hour_pct;
  if (data.seven_day_pct >= 0) patch.rlSevenDayPct = data.seven_day_pct;
  updateSession(id, patch);

  // Patch the global (poller-owned) snapshot only for Anthropic sessions: those
  // windows are account-wide and share the /api/usage shape. An OpenAI session
  // must NOT overwrite the Anthropic snapshot in a mixed layout.
  const sess = store.get().sessions[id];
  const isAnthropic = !sess?.provider || sess.provider === 'anthropic';
  if (sess?.provider === 'openai') {
    const current = store.get().usage || { available: false, version: 2, providers: {}, provider_status: {} };
    const prior = current.providers?.openai || {};
    const openai = {
      ...prior,
      provider: 'openai',
      auth_kind: 'oauth',
      stability: 'response_headers',
    };
    if (data.five_hour_pct >= 0) openai.five_hour = { utilization: data.five_hour_pct };
    if (data.seven_day_pct >= 0) openai.seven_day = { utilization: data.seven_day_pct };
    setState({ usage: {
      ...current,
      available: true,
      providers: { ...(current.providers || {}), openai },
      provider_status: { ...(current.provider_status || {}), openai: { available: true, auth_kind: 'oauth' } },
    } });
    return;
  }
  if (!isAnthropic) return;

  const u = store.get().usage;
  if (u && u.available) {
    let changed = false;
    const usage = { ...u };
    // Only apply a window when the header was present (pct >= 0); never overwrite
    // a known value with an unknown one.
    if (u.five_hour && data.five_hour_pct >= 0) {
      usage.five_hour = { ...u.five_hour, utilization: data.five_hour_pct };
      changed = true;
    }
    if (u.seven_day && data.seven_day_pct >= 0) {
      usage.seven_day = { ...u.seven_day, utilization: data.seven_day_pct };
      changed = true;
    }
    if (changed) setState({ usage });
  }
}

export function handleWsSubagentCount(id, count) {
  updateSession(id, { subagentCount: count });
}

export function handleWsSubagentComplete(id, data) {
  const statusIcon = data.status === 'completed' ? '✓' : data.status === 'failed' ? '✗' : '⊘';
  // Keep the toast short: `task` is the full delegated prompt (often long,
  // multi-paragraph), and the full output is already available below as an
  // expandable subagent card in the chat, so the toast is just a heads-up.
  // Only surface a toast when the session isn't already on screen — a visible
  // delegation block already reports the outcome (SUBAGENTS-REDESIGN-SPEC §4).
  const taskLine = (data.task || data.job_id || '').split('\n')[0];
  if (isSessionAway(id)) {
    addToast({
      sessionId: id,
      title: `Subagent ${statusIcon} ${data.status}`,
      detail: truncateText(taskLine, 140),
      type: data.status === 'completed' ? 'done' : 'attention',
    });
  }

  // This legacy event is a model-delivery mechanism, not a UI lifecycle
  // event. subagent_end owns the one terminal card even when subagent_wait
  // consumed the model result and no completion notification was emitted.
  markUnseen(id);
}

export function handleWsBashComplete(id, data) {
  const sess = store.get().sessions[id];
  if (!sess) return;
  if (data.owner_agent_id) {
    // bash_job_end normally finalized this exact row first. This fallback
    // also finalizes a start-before-subagent placeholder when its end event
    // was missed, preserving the child transcript without a root card.
    const owner = sess.subagents?.[data.owner_agent_id];
    const row = owner?.messages?.find(m => m._type === 'tool_start' && m.tool_call_id === data.job_id);
    if (!row || row.status === 'running' || row.status === 'generating') {
      updateSession(id, {
        subagents: attachBashJob(sess.subagents || {}, {
          ...data,
          output: data.text || '',
        }),
      });
    }
    return;
  }
  const statusIcon = data.status === 'completed' ? '✓' : data.status === 'failed' ? '✗' : '⊘';
  const cmdLine = (data.command || data.job_id || '').split('\n')[0];
  // Only toast when the session isn't on screen — a visible background block
  // already reports the outcome (SUBAGENTS-REDESIGN-SPEC §4).
  if (isSessionAway(id)) {
    addToast({
      sessionId: id,
      title: `Bash ${statusIcon} ${data.status}`,
      detail: truncateText(cmdLine, 140),
      type: data.status === 'completed' ? 'done' : 'attention',
    });
  }

  // Add a bash card to the chat (mirrors TUI's bash notification block).
  const messages = [...(sess.messages || [])];
  messages.push({
    _type: 'tool_start',
    tool_call_id: `bash-complete-${data.job_id}`,
    tool_name: 'bash',
    args: { command: data.command || '' },
    status: data.status === 'failed' ? 'error' : 'done',
    result: data.text || '',
  });
  updateSession(id, { messages });
  markUnseen(id);
}

// --- Live subagent sub-conversations (agent tray) ---
//
// Each subagent's transcript lives at session.subagents[jobId] and is fed by
// the SAME pure conversation reducer as the main chat, so it renders
// identically. Streaming deltas are batched per (sessionId:jobId) via rAF,
// mirroring flushDeltas for the main conversation.

const subagentBuffers = {};      // "sessionId:jobId" → reducer buffers
const pendingSubagentEvents = {}; // sessionId → [{ jobId, evt }]
let subagentFlushScheduled = false;

function subBufKey(id, jobId) { return id + ':' + jobId; }

function getSubBuffers(id, jobId) {
  const k = subBufKey(id, jobId);
  if (!subagentBuffers[k]) subagentBuffers[k] = newBuffers();
  return subagentBuffers[k];
}

function scheduleSubagentFlush() {
  if (subagentFlushScheduled) return;
  subagentFlushScheduled = true;
  requestAnimationFrame(flushSubagentEvents);
}

function flushSubagentEvents() {
  subagentFlushScheduled = false;
  const ids = Object.keys(pendingSubagentEvents);
  for (const id of ids) {
    const queue = pendingSubagentEvents[id];
    delete pendingSubagentEvents[id];
    const sess = store.get().sessions[id];
    if (!sess) continue;
    const subs = { ...(sess.subagents || {}) };
    let changed = false;
    for (const { jobId, evt } of queue) {
      const existing = subs[jobId];
      // An init is authoritative about live jobs. A queued event from before
      // that snapshot must not recreate a job the server no longer lists;
      // terminal jobs likewise cannot receive more transcript updates.
      const isTerminal = existing
        && (existing.status === 'completed' || existing.status === 'failed' || existing.status === 'cancelled');
      if (!existing || isTerminal) continue;
      // Shallow clone the mutable transcript fields before reducing.
      const target = {
        messages: existing.messages || [],
        streamingText: existing.streamingText ?? null,
        thinkingText: existing.thinkingText ?? null,
      };
      applyNestedEvent(target, getSubBuffers(id, jobId), evt);
      subs[jobId] = {
        ...existing,
        messages: target.messages,
        streamingText: target.streamingText,
        thinkingText: target.thinkingText,
      };
      changed = true;
    }
    if (changed) updateSession(id, { subagents: subs });
  }
}

export function handleWsSubagentStart(id, data) {
  const sess = store.get().sessions[id];
  if (!sess) return;
  const jobId = data.job_id;
  if (!jobId) return;
  const subs = { ...(sess.subagents || {}) };
  const existing = subs[jobId];
  // Race: promoting a subagent right as it finishes can deliver this
  // subagent_start (async:true, echoing the promotion) AFTER the
  // subagent_end that already marked it terminal. Never downgrade a
  // terminal status back to 'running' — only running/cancelling (or no
  // existing entry) may become 'running' here.
  const isTerminal = existing
    && (existing.status === 'completed' || existing.status === 'failed' || existing.status === 'cancelled');
  subs[jobId] = {
    jobId,
    originToolCallId: data.origin_tool_call_id || (existing && existing.originToolCallId) || '',
    task: data.task || (existing && existing.task) || '',
    model: data.model || (existing && existing.model) || '',
    thinking: data.thinking || (existing && existing.thinking) || 'off',
    status: isTerminal ? existing.status : 'running',
    async: data.async ?? (existing ? existing.async : true),
    messages: (existing && existing.messages) || [],
    streamingText: (existing && existing.streamingText) ?? null,
    thinkingText: (existing && existing.thinkingText) ?? null,
    startedAtMs: data.started_at_ms || (existing && existing.startedAtMs) || null,
    usage: (existing && existing.usage) || null,
    // See initSubagents: preserved across a promotion echo (existing wins if
    // the live event omits it, though the backend always sends it).
    accentIndex: data.accent_index ?? (existing && existing.accentIndex),
  };
  // A start can race the parent's already-materialized tool row. Attach the
  // durable job ID immediately so the launch acknowledgement never projects
  // as a completed Result while the child is actually live.
  const originToolCallId = data.origin_tool_call_id || existing?.originToolCallId;
  const messages = originToolCallId
    ? (sess.messages || []).map(m => m?._type === 'tool_start' && m.tool_call_id === originToolCallId
      ? { ...m, subagentJobId: jobId }
      : m)
    : sess.messages;
  updateSession(id, { subagents: subs, ...(originToolCallId ? { messages } : {}) });
}

// handleWsSubagentUsage applies the backend's live, cumulative token/cost
// tally and context reading for one subagent (subagent_usage). The backend
// sends the running total on every event, so this SETS them rather than
// accumulating.
// Silently ignored if the subagent isn't known yet (e.g. usage arrived before
// subagent_start, or the subagent was already pruned).
export function handleWsSubagentUsage(id, data) {
  const sess = store.get().sessions[id];
  const jobId = data?.job_id;
  if (!sess || !jobId) return;
  const existing = sess.subagents?.[jobId];
  if (!existing) return;
  const subs = {
    ...sess.subagents,
    [jobId]: {
      ...existing,
      usage: {
        inputTokens: data.input_tokens || 0,
        outputTokens: data.output_tokens || 0,
        costUSD: data.cost_usd || 0,
      },
      // The child's own context fill. -1 (unknown) when its model carries no
      // window; the parent's percentage is never a stand-in, since the two
      // agents hold different transcripts.
      contextPercent: data.context_percent == null ? -1 : data.context_percent,
    },
  };
  updateSession(id, { subagents: subs });
}

export function handleWsSubagentEvent(id, data) {
  if (!store.get().sessions[id]) return;
  const jobId = data.job_id;
  const evt = data.event;
  if (!jobId || !evt) return;
  if (!pendingSubagentEvents[id]) pendingSubagentEvents[id] = [];
  pendingSubagentEvents[id].push({ jobId, evt });
  scheduleSubagentFlush();
}

export function handleWsSubagentEnd(id, data) {
  const sess = store.get().sessions[id];
  if (!sess) return;
  const jobId = data.job_id;
  if (!jobId) return;
  // Flush any queued events for this subagent first so the final transcript
  // is complete before we mark it ended.
  flushSubagentEvents();
  const after = store.get().sessions[id];
  if (!after) return;
  const subs = { ...(after.subagents || {}) };
  const existing = subs[jobId] || {
    jobId,
    task: data.task || '',
    model: '',
    status: data.status || 'completed',
    async: !!data.async,
    messages: [],
    streamingText: null,
    thinkingText: null,
    usage: null,
  };
  subs[jobId] = {
    ...existing,
    status: data.status || 'completed',
    streamingText: null,
    thinkingText: null,
    usage: {
      inputTokens: data.input_tokens || 0,
      outputTokens: data.output_tokens || 0,
      costUSD: data.cost_usd || 0,
    },
  };
  delete subagentBuffers[subBufKey(id, jobId)];
  const terminal = subs[jobId];
  updateSession(id, {
    subagents: subs,
    messages: upsertTerminalSubagentOutcome(after.messages, terminal, data),
  });
}

// upsertTerminalSubagentOutcome is the UI's terminal lifecycle projection.
// It is keyed by job ID rather than model-delivery ownership: a waiter-owned
// result, a natural async notification, and a promoted sync child therefore
// all produce exactly one card. A later replay/reconnect simply replaces it.
export function upsertTerminalSubagentOutcome(messages, subagent, data) {
  const jobId = data?.job_id;
  if (!jobId) return messages || [];
  const status = data.status || subagent?.status || 'completed';
  const current = Array.isArray(messages) ? messages : [];
  const key = `subagent-${jobId}`;
  const index = current.findIndex(m => m?._type === 'tool_start' && m.tool_call_id === key);
  const existing = index >= 0 ? current[index] : null;
  // Old sidecar transcripts did not carry explicit result/error. Do not let
  // their empty fields erase the historical parent notification that is the
  // only available outcome for that job.
  const legacyResult = existing?.result || '';
  const legacyError = existing?.error || existing?.result || '';
  const result = status === 'completed' ? (data.result || legacyResult) : '';
  const error = status === 'failed' ? (data.error || legacyError) : '';
  const row = {
    _type: 'tool_start',
    tool_call_id: `subagent-${jobId}`,
    subagentJobId: jobId,
    tool_name: 'subagent',
    args: { task: data.task || subagent?.task || '' },
    status: status === 'completed' ? 'done' : status === 'cancelled' ? 'cancelled' : 'error',
    accentIndex: Number.isInteger(subagent?.accentIndex) ? subagent.accentIndex : undefined,
    // A successful but empty child response has no Result action. Failed
    // children expose their actual error as Error; cancelled has neither.
    result,
    error,
    excerpt: !!data.excerpt,
    finishedAtMs: data.finished_at_ms || null,
  };
  if (index < 0) return insertTerminalSubagentOutcome(current, row);
  // A model-delivery notification may already have normalized to this card,
  // but its parent-message timestamp is not the child's completion time. Move
  // it to the authoritative finished_at_ms position rather than retaining a
  // live/reload ordering discrepancy.
  const withoutExisting = [...current.slice(0, index), ...current.slice(index + 1)];
  return insertTerminalSubagentOutcome(withoutExisting, { ...existing, ...row });
}

// Restored terminal cards belong at their real completion point in the parent
// history. Core message timestamps are seconds; lifecycle outcomes are millis.
function insertTerminalSubagentOutcome(messages, row) {
  const finished = row.finishedAtMs || 0;
  if (!finished) return [...messages, row];
  const index = messages.findIndex(message => {
    const timestamp = messageTimelineMs(message);
    return timestamp > 0 && timestamp > finished;
  });
  if (index < 0) return [...messages, row];
  return [...messages.slice(0, index), row, ...messages.slice(index)];
}

function messageTimelineMs(message) {
  if (!message) return 0;
  if (message.finishedAtMs) return message.finishedAtMs;
  return message.timestamp ? message.timestamp * 1000 : 0;
}

export function handleWsBashJobStart(id, data) {
  const sess = store.get().sessions[id];
  if (!sess || !data.job_id) return;
  updateSession(id, { subagents: attachBashJob(sess.subagents || {}, data) });
}

export function handleWsBashJobOutput(id, data) {
  if (!store.get().sessions[id] || !data.job_id || !data.delta) return;
  pendingBashDeltas[id] = pendingBashDeltas[id] || {};
  const existing = pendingBashDeltas[id][data.job_id];
  pendingBashDeltas[id][data.job_id] = {
    delta: (existing?.delta || '') + data.delta,
    ownerAgentId: data.owner_agent_id || existing?.ownerAgentId || '',
  };
  scheduleFlush();
}

export function handleWsBashJobEnd(id, data) {
  const sess = store.get().sessions[id];
  if (!sess || !data.job_id) return;
  const targetJobId = data.owner_agent_id || data.job_id;
  const existing = sess.subagents?.[targetJobId];
  if (!existing) return;
  const status = data.status || 'completed';
  const messages = existing.messages.map(m => m._type === 'tool_start' && m.tool_call_id === data.job_id
    ? { ...m, status: status === 'completed' ? 'done' : 'error', result: data.output || '', streamingResult: null } : m);
  if (!data.owner_agent_id) {
    updateSession(id, { subagents: { ...sess.subagents, [targetJobId]: { ...existing, status, messages } } });
    return;
  }
  updateSession(id, {
    subagents: {
      ...sess.subagents,
      [targetJobId]: {
        ...existing,
        status: existing.syntheticOwnedBashOwner ? status : existing.status,
        messages,
      },
    },
  });
}

export function handleWsRunEnd(id, data = {}, seq = 0) {
  delete pendingTextDeltas[id];
  delete pendingThinkingDeltas[id];
  delete pendingToolDeltas[id];
	delete pendingBashDeltas[id];
  delete pendingToolCallBuffers[id];
  delete materializedTextDuringMessage[id];

  // Mark any generating tools as cancelled.
  const sess = store.get().sessions[id];
  if (sess) {
    let changed = false;
    const messages = sess.messages.map(m => {
      if (m._type === 'tool_start' && m.status === 'generating') {
        changed = true;
        return { ...m, status: 'error', result: 'Run ended before execution' };
      }
      return m;
    });
    updateSession(id, {
      messages: changed ? messages : sess.messages,
      streamingText: null,
      thinkingText: null,
      // Do NOT clear pendingSteers here: a steer that arrived during the run's
      // last turn stays genuinely queued (it will be delivered on the next
      // run). The chip must reflect the truth and is removed only by a Steered
      // event (real consumption) or the authoritative reconnect snapshot.
      runningTool: null,
      compacting: false,
    });
  } else {
    updateSession(id, { streamingText: null, thinkingText: null, runningTool: null, compacting: false });
  }
  // The backend tracker marks only a non-cancelled, non-error RunEnded.
  // Errors were already marked by their StateChanged(error) event.
  if (data.cancelled || data.has_error || seq === 0) return;
  markUnseen(id, seq, sess?.state !== 'error');
  acknowledgeVisibleLiveAttention(id, seq);
}

// handleWsUserMessage renders a user prompt that started a new run on EVERY
// client, not just the one that issued it (another tab, or an external API
// client such as the voice app). Dedup is by MsgID, which the server shares
// with the message it appends to history: the sending tab already painted it
// optimistically under the same ID, and a reconnect snapshot may already
// contain it too.
export function handleWsUserMessage(id, data) {
	const sess = store.get().sessions[id];
	if (!sess || !data) return;
	const messages = sess.messages || [];
	if (data.msg_id && messages.some(m => m._msg_id === data.msg_id)) return;
  const secretBatch = secretBatchFromMessage(data);
  if (secretBatch) {
    updateSession(id, { messages: [...messages, { _type: 'secret_batch', _msg_id: data.msg_id || undefined, aliases: secretBatch }] });
    return;
  }
  const content = Array.isArray(data.content) && data.content.length > 0
    ? data.content
    : [{ type: 'text', text: data.text || '' }];
  // Completion notifications are still delivered to the parent model as user
  // messages. The structured child lifecycle owns their presentation, though:
  // while its live/terminal job exists, rendering this envelope as another
  // user turn would duplicate the terminal outcome and diverge from reload.
	if (isStructuredSubagentNotification(sess, data.text || '')) return;
  // moa's own reminder, not something the user typed: same thin system line
  // the reloaded transcript shows, instead of a user turn full of markup.
  if (data.custom?.source === 'compaction_notice') {
    updateSession(id, { messages: [...messages, {
      _type: 'system',
      _msg_id: data.msg_id || undefined,
      text: '⚠ Context filling up — asked the agent to save unsaved work',
    }] });
    return;
  }
	const userMsg = { role: 'user', _msg_id: data.msg_id || undefined, content, custom: data.custom };
	updateSession(id, { messages: [...messages, userMsg] });
}

export function handleWsSteer(id, data) {
	const sess = store.get().sessions[id];
	if (!sess) return;
	const messages = sess.messages || [];
  let steers = [...(sess.pendingSteers || [])];
  // Reconcile purely by authoritative ID. Steer IDs are minted client-side
  // before the chip appears, so every chip already has its final identity and
  // two queued messages with identical text never collapse into one chip. A
  // steer from another device carries an ID this client never had, so it just
  // appends the message without touching local chips.
  if (data.id) {
    steers = steers.filter(s => s.id !== data.id);
  }
  // Dedup the injected user message by MsgID: a non-atomic reconnect snapshot
  // may already contain it (the agent appended it to state before the cut),
  // and this Steered event (seq > cut) would otherwise add it a second time.
	const already = data.msg_id && messages.some(m => m._msg_id === data.msg_id);
  const patch = { pendingSteers: steers.length > 0 ? steers : null };
  if (!already) {
    const secretBatch = secretBatchFromMessage(data);
    if (secretBatch) {
      patch.messages = [...messages, { _type: 'secret_batch', _msg_id: data.msg_id || undefined, aliases: secretBatch }];
      updateSession(id, patch);
      return;
    }
    // A steer with attachments arrives with its blocks (same projection as
    // user_message), so the delivered message shows its thumbnails live; a
    // text-only steer only carries text.
    const content = Array.isArray(data.content) && data.content.length > 0
      ? data.content
      : [{ type: 'text', text: data.text }];
    if (!isStructuredSubagentNotification(sess, data.text || '')) {
		const userMsg = { role: 'user', _msg_id: data.msg_id || undefined, _steer_id: data.id || undefined, content, custom: data.custom };
		patch.messages = [...messages, userMsg];
    }
  }
  updateSession(id, patch);
}

function secretBatchFromMessage(data) {
  if (data?.custom?.source === 'secret_batch') {
    return Array.isArray(data.custom.secret_aliases) ? data.custom.secret_aliases : [];
  }
  return null;
}

function isStructuredSubagentNotification(session, text) {
  const notification = parseSubagentNotification(text);
  if (!notification?.jobId) return false;
  if (session?.subagents?.[notification.jobId]) return true;
  return (session?.messages || []).some(m =>
    m?._type === 'tool_start' && m.tool_call_id === `subagent-${notification.jobId}`,
  );
}

// Sidecar storage historically returns newest-first. Terminal cards are parent
// timeline entries, so restore them from their actual completion anchors, not
// reverse directory/list order. Unknown old timestamps stay after dated rows
// in their stable source order.
function chronologicalSubagentOutcomes(outcomes) {
  return [...(outcomes || [])].sort((a, b) => {
    const at = a?.finished_at_ms || 0;
    const bt = b?.finished_at_ms || 0;
    if (!at) return bt ? 1 : 0;
    if (!bt) return -1;
    return at - bt;
  });
}

// handleWsSteersCanceled clears the shared queue on every client when the
// queued (not yet delivered) steers were dropped (e.g. dequeued for editing).
export function handleWsSteersCanceled(id) {
  const sess = store.get().sessions[id];
  if (!sess || !sess.pendingSteers) return;
  updateSession(id, { pendingSteers: null });
}

// handleWsCommandQueued adds a queued slash-command barrier to the shared queue
// on every client. The command was enqueued mid-run (policy = queue) and will
// run at the next idle point, preserving strict send order. Reconciled by ID:
// the client that issued it minted the ID for its optimistic chip, so this
// authoritative event just confirms it (or, for another device, appends it).
export function handleWsCommandQueued(id, data) {
  const sess = store.get().sessions[id];
  if (!sess || !data || !data.id) return;
  const steers = [...(sess.pendingSteers || [])];
  if (steers.some(s => s.id === data.id)) {
    // Already present as an optimistic chip — confirm it (see mergeSteers).
    updateSession(id, {
      pendingSteers: steers.map(s => (s.id === data.id ? { ...s, confirmed: true } : s)),
    });
    return;
  }
  steers.push({ id: data.id, text: data.raw, command: true, confirmed: true });
  updateSession(id, { pendingSteers: steers });
}

// handleWsCommandDequeued removes a queued command barrier when it leaves the
// queue — either executed at idle (executed=true) or dropped because it failed
// permanently (executed=false, err set). The command chip disappears; a failure
// surfaces as a toast so a queued command that never ran isn't lost silently.
export function handleWsCommandDequeued(id, data) {
  const sess = store.get().sessions[id];
  if (!sess || !data || !data.id) return;
  const steers = (sess.pendingSteers || []).filter(s => s.id !== data.id);
  updateSession(id, { pendingSteers: steers.length > 0 ? steers : null });
  if (!data.executed && data.err) {
    addToast({ sessionId: id, title: 'Queued command failed', detail: `${data.raw}: ${data.err}`, type: 'error' });
  }
}

export function handleWsTasksUpdate(id, data) {
  updateSession(id, { tasks: data.tasks || [] });
}

export function handleWsCommand(id, data) {
  if (data.command === 'clear') {
    resetOlderHistory(id);
    updateSession(id, { messages: [], streamingText: null, thinkingText: null });
  } else if (data.command === 'compact') {
    // Don't replace the transcript with the compacted LLM context. When the
    // command event includes the durable tree marker, append that exact row;
    // otherwise wait for init/history hydration rather than fabricating a
    // second, non-durable representation.
    const sess = store.get().sessions[id];
    const markers = normalizeHistory(data.messages || []).filter(row => row._type === 'compaction_marker');
    if (sess && markers.length > 0) {
      const known = new Set(sess.messages.map(message => message?._msg_id).filter(Boolean));
      const fresh = markers.filter(marker => marker._msg_id && !known.has(marker._msg_id));
      if (fresh.length > 0) updateSession(id, { messages: [...sess.messages, ...fresh] });
    }
  } else if (data.command === 'skill') {
    // A skill loaded by the user is an ordinary message appended to the
    // conversation. Without this the row only shows up on the next reload, so
    // invoking a skill looks like nothing happened.
    const sess = store.get().sessions[id];
    if (sess && data.messages) {
      const known = new Set(sess.messages.map(message => message?._msg_id).filter(Boolean));
      const fresh = normalizeHistory(data.messages).filter(row => row._msg_id && !known.has(row._msg_id));
      if (fresh.length > 0) updateSession(id, { messages: [...sess.messages, ...fresh] });
    }
  } else if (data.command === 'branch') {
    // Branch switched — reload messages from new branch path.
    if (data.messages) {
      resetOlderHistory(id);
      updateSession(id, { messages: normalizeHistory(data.messages), historyTruncated: !!data.history_truncated });
    }
  }
}

function appendCompactionMarker(id, rawMarker) {
  const sess = store.get().sessions[id];
  if (!sess || !rawMarker) return;
  const [marker] = normalizeHistory([rawMarker]);
  if (!marker?._msg_id || sess.messages.some(message => message?._msg_id === marker._msg_id)) return;
  updateSession(id, { messages: [...sess.messages, marker] });
}

/** Parse a subagent notification from a user message text (mirrors TUI's parseSubagentNotification). */
function subagentRestoreStatus(raw) {
  // Backend persists completed | failed | cancelled. Map to the projection's
  // tool_start status vocabulary, keeping `cancelled` distinct from `error`
  // so DelegationBlock can render ⊘ instead of ✗.
  const s = String(raw || '');
  if (s === 'completed') return 'done';
  if (s === 'cancelled') return 'cancelled';
  return 'error';
}

function parseSubagentNotification(text) {
  const prefixes = {
    '[subagent completed] ': 'completed',
    '[subagent failed] ': 'failed',
    '[subagent cancelled] ': 'cancelled',
  };
  for (const [prefix, status] of Object.entries(prefixes)) {
    if (text.startsWith(prefix)) {
      const rest = text.slice(prefix.length);
      const firstNewline = rest.indexOf('\n');
      const jobLine = firstNewline >= 0 ? rest.slice(0, firstNewline) : rest;
      const jobMatch = /^Job (\S+) (?:finished|failed|was cancelled)\.$/.exec(jobLine);
      let task = '';
      let result = '';
      const payload = firstNewline >= 0 ? rest.slice(firstNewline + 1) : '';
      if (payload.startsWith('Task: ')) {
        const taskAndResult = payload.slice('Task: '.length);
        const markers = [
          '\n\nResult (last 50 lines):\n',
          '\n\nResult (truncated — use subagent_status for full output):\n',
          '\n\nResult:\n',
          '\nError: ',
        ];
        let markerAt = -1;
        let marker = '';
        for (const candidate of markers) {
          const at = taskAndResult.indexOf(candidate);
          if (at >= 0 && (markerAt < 0 || at < markerAt)) {
            markerAt = at;
            marker = candidate;
          }
        }
        if (markerAt >= 0) {
          task = taskAndResult.slice(0, markerAt).trim();
          result = taskAndResult.slice(markerAt + marker.length).trim();
        } else {
          task = taskAndResult.trim();
        }
      }
      return { jobId: jobMatch ? jobMatch[1] : '', task, status, result };
    }
  }
  return null;
}

/** Parse an async bash completion notification from a user message text (mirrors TUI's parseBashNotification). */
function parseBashNotification(text) {
  const prefixes = {
    '[bash job completed] ': 'completed',
    '[bash job failed] ': 'failed',
    '[bash job cancelled] ': 'cancelled',
  };
  for (const [prefix, status] of Object.entries(prefixes)) {
    if (text.startsWith(prefix)) {
      const rest = text.slice(prefix.length);
      const lines = rest.split('\n');
      let command = '';
      if (lines.length >= 2 && lines[1].startsWith('Command: ')) {
        command = lines[1].slice('Command: '.length);
      }
      return { command, status };
    }
  }
  return null;
}

export function handleWsGoalChange(id, data) {
  const sess = store.get().sessions[id];
  const patch = {
    goalActive: !!data.active,
    goalObjective: data.active ? (data.objective || '') : null,
    goalWorkDir: data.active ? (data.work_dir || '') : null,
    goalIteration: data.iteration || 0,
    goalStalled: data.stalled || 0,
  };
  if (!data.active) patch.goalVerifying = false;
  // Live start line, matching the persisted "start" marker shown on reopen.
  // Only on a fresh activation (iteration 0) so a reconnect's goal_change echo
  // doesn't re-announce an already-running goal.
  if (sess && data.active && !sess.goalActive && (data.iteration || 0) === 0) {
    patch.messages = [...sess.messages, { _type: 'system', text: `🎯 Goal started: ${data.objective || ''}` }];
  }
  updateSession(id, patch);
}

export function handleWsGoalIteration(id, data) {
  const sess = store.get().sessions[id];
  if (!sess) return;
  const verdict = data.satisfied ? 'satisfied' : 'not done yet';
  let text = `🎯 Goal iteration ${data.iteration} — ${verdict}`;
  if (data.feedback && data.feedback.trim()) text += `\n${data.feedback}`;
  updateSession(id, {
    messages: [...sess.messages, { _type: 'system', text }],
    goalIteration: data.iteration || sess.goalIteration || 0,
  });
}

export function handleWsGoalVerify(id, data) {
  const sess = store.get().sessions[id];
  if (!sess) return;
  updateSession(id, { goalVerifying: !!data.active });
}

export function handleWsGoalEnd(id, data) {
  const sess = store.get().sessions[id];
  if (!sess) return;
  updateSession(id, {
    goalActive: false,
    goalObjective: null,
    goalWorkDir: null,
    goalVerifying: false,
    messages: [...sess.messages, { _type: 'system', text: `🎯 Goal ended: ${data.reason || ''}` }],
  });
  markUnseen(id);
}

export function handleWsAutoVerifyStart(id, data) {
  // The directory only travels with a manual /verify aimed at another
  // repository; auto-verify always runs in the session's own.
  updateSession(id, {
    autoVerifying: true,
    verifyDir: data?.dir || null,
    verifyManual: Boolean(data?.manual),
  });
}

export function handleWsAutoVerifyEnd(id, data) {
  updateSession(id, { autoVerifying: false, verifyDir: null, verifyManual: false });
}

export function handleWsCompactionStart(id) {
  updateSession(id, { compacting: true });
}

export function handleWsCompactionEnd(id, data) {
  updateSession(id, { compacting: false });
  appendCompactionMarker(id, data?.marker);
}
