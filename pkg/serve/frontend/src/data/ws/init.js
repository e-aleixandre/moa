// WebSocket init snapshot handling.

import { wsState } from './shared.js';
import { appendNormalizedHistoryDelta, normalizeHistory } from './history.js';
import { attentionNamespaceFromInit, attentionNamespaceTransition } from './attention.js';
import { chronologicalSubagentOutcomes, upsertTerminalSubagentOutcome } from './subagents.js';
import { canAppendHistoryDelta, finishHistoryHydration } from '../history-hydration.js';
import { acknowledgeVisibleAttentionThrough } from '../api.js';
import { store, updateSession, visibleSessionIds } from '../store.js';
import { seedOlderHistory } from '../history-paging.js';

export function mergeSteers(snapshot, local) {
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
export function nextRunEpoch(id) {
  return (store.get().sessions[id]?.runEpoch || 0) + 1;
}

export function handleWsInit(id, data) {
  delete wsState.pendingTextDeltas[id];
  delete wsState.pendingThinkingDeltas[id];
  delete wsState.pendingToolDeltas[id];
  delete wsState.pendingBashDeltas[id];
  delete wsState.pendingToolCallBuffers[id];
  delete wsState.pendingSubagentEvents[id];
  for (const key of Object.keys(wsState.subagentBuffers)) {
    if (key.startsWith(`${id}:`)) delete wsState.subagentBuffers[key];
  }
  delete wsState.materializedTextDuringMessage[id];
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
export function withLiveTools(messages, liveTools) {
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

export function withLiveToolsInPlace(messages, liveTools) {
  const reconciled = withLiveTools(messages, liveTools);
  if (reconciled !== messages) messages.splice(0, messages.length, ...reconciled);
  return messages;
}

export function liveToolRow(t) {
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
export function initSubagents(raw) {
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

export function initBashJobs(raw, existing = {}) {
  let out = { ...existing };
  for (const job of (raw || [])) {
    if (!job || !job.job_id) continue;
    out = attachBashJob(out, job);
  }
  return out;
}

export function bashJobState(job, existing = null) {
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

export function bashToolMessage(job, existing = null) {
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

export function emptyOwnedSubagent(jobId, bashStatus = 'running') {
  return {
    jobId, task: '', model: '',
    status: bashStatus === 'running' || bashStatus === 'cancelling' ? 'running' : bashStatus,
    async: true, syntheticOwnedBashOwner: true,
    messages: [], streamingText: null, thinkingText: null, usage: null,
  };
}

// attachBashJob keeps root jobs as their own live entries, but puts an owned
// job's tool row directly in its launching subagent's transcript.
export function attachBashJob(subagents, job) {
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

