// WebSocket subagent event handling.

import { wsState } from './shared.js';
import { newBuffers, applyNestedEvent } from '../conversation-reducer.js';
import { truncateText } from '../util/format.js';
import { addToast } from '../notifications.js';
import { store, updateSession } from '../store.js';
import { isSessionAway, markUnseen } from './attention.js';
import { noteArtifactDelivery } from './tools.js';

export function handleWsSubagentTitle(id, data) {
  const sess = store.get().sessions[id];
  if (!sess || !data?.job_id || !data?.title) return;
  const existing = sess.subagents?.[data.job_id] || { jobId: data.job_id };
  updateSession(id, { subagents: { ...sess.subagents, [data.job_id]: { ...existing, title: data.title } } });
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



export function subBufKey(id, jobId) { return id + ':' + jobId; }

export function getSubBuffers(id, jobId) {
  const k = subBufKey(id, jobId);
  if (!wsState.subagentBuffers[k]) wsState.subagentBuffers[k] = newBuffers();
  return wsState.subagentBuffers[k];
}

export function scheduleSubagentFlush() {
  if (wsState.subagentFlushScheduled) return;
  wsState.subagentFlushScheduled = true;
  requestAnimationFrame(flushSubagentEvents);
}

export function flushSubagentEvents() {
  wsState.subagentFlushScheduled = false;
  const ids = Object.keys(wsState.pendingSubagentEvents);
  for (const id of ids) {
    const queue = wsState.pendingSubagentEvents[id];
    delete wsState.pendingSubagentEvents[id];
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
    // The title is generated asynchronously AFTER the first start event, so a
    // later start (promotion echo) never carries it: the existing one wins
    // rather than being erased back to the model id.
    title: data.title || (existing && existing.title) || '',
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
  if (!wsState.pendingSubagentEvents[id]) wsState.pendingSubagentEvents[id] = [];
  wsState.pendingSubagentEvents[id].push({ jobId, evt });
  // A delegated send_file publishes into the PARENT conversation, so a nested
  // tool_end refreshes the same open drawer a foreground delivery would.
  if (evt.type === 'tool_end') noteArtifactDelivery(id, evt.data);
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
  delete wsState.subagentBuffers[subBufKey(id, jobId)];
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
    // The generated title is the card's identity label. After a reload with no
    // live entry left, the init outcome is the ONLY carrier of it, so it is
    // stored on the card; an event without one never erases what is known.
    subagentTitle: data.title || subagent?.title || existing?.subagentTitle || '',
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
export function insertTerminalSubagentOutcome(messages, row) {
  const finished = row.finishedAtMs || 0;
  if (!finished) return [...messages, row];
  const index = messages.findIndex(message => {
    const timestamp = messageTimelineMs(message);
    return timestamp > 0 && timestamp > finished;
  });
  if (index < 0) return [...messages, row];
  return [...messages.slice(0, index), row, ...messages.slice(index)];
}

export function messageTimelineMs(message) {
  if (!message) return 0;
  if (message.finishedAtMs) return message.finishedAtMs;
  return message.timestamp ? message.timestamp * 1000 : 0;
}


export function chronologicalSubagentOutcomes(outcomes) {
  return [...(outcomes || [])].sort((a, b) => {
    const at = a?.finished_at_ms || 0;
    const bt = b?.finished_at_ms || 0;
    if (!at) return bt ? 1 : 0;
    if (!bt) return -1;
    return at - bt;
  });
}

