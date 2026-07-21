// ws-handlers.js — WebSocket event handlers and streaming delta batching

import { triggerAttention, triggerDone, addToast } from './notifications.js';
import { store, setState, updateSession, visibleSessionIds } from './store.js';
import { newBuffers, applyNestedEvent } from './conversation-reducer.js';
import { truncateText } from './util/format.js';

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
  for (const msg of raw) {
    if (msg.role === 'assistant') {
      const textParts = [];
      for (const c of (msg.content || [])) {
        if (c.type === 'text' && c.text) {
          textParts.push(c.text);
        } else if (c.type === 'tool_call') {
          if (textParts.length > 0) {
            result.push({ role: 'assistant', _msg_id: msg.msg_id, content: [{ type: 'text', text: textParts.join('') }] });
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
            tool_call_id: c.tool_call_id,
            tool_name: c.tool_name,
            args: c.arguments || {},
            status,
            result: resultText,
            note: extractToolNote(resultText, status === 'rejected'),
          });
        }
      }
      if (textParts.length > 0) {
        result.push({ role: 'assistant', _msg_id: msg.msg_id, content: [{ type: 'text', text: textParts.join('') }] });
      }
    } else if (msg.role === 'shell' || (msg.role === 'user' && msg.custom?.shell)) {
      const text = (msg.content || []).filter(x => x.type === 'text').map(x => x.text).join('');
      const { command, output } = parseShellBody(text);
      result.push({
        _type: 'tool_start',
        tool_call_id: 'shell_' + result.length,
        tool_name: 'bash',
        args: { command },
        status: 'done',
        result: output,
      });
    } else if (msg.role === 'goal') {
      // Persistent goal-lifecycle marker (start / iteration verdict / end).
      // Rendered as a system line, matching the live goal event styling.
      const text = (msg.content || []).filter(x => x.type === 'text').map(x => x.text).join('');
      result.push({ _type: 'system', text });
    } else if (msg.role === 'user') {
      if (msg.custom?.source === 'subagent') {
        // When a real job ID is available, key the restored card
        // `subagent-<jobId>` so projectStream folds it into the turn's
        // delegation block by that ID. Unmatched legacy cards retain a
        // synthetic key. accentIndex, if saved, keeps the row's color stable
        // across reloads; the projection falls back to a jobId hash otherwise.
        const jobId = msg.custom.subagent_job_id ||
          legacySubagentJobIds.get(subagentTaskIdentity(msg.custom.subagent_task));
        result.push({
          _type: 'tool_start',
          tool_call_id: jobId ? 'subagent-' + jobId : 'subagent_' + result.length,
          tool_name: 'subagent',
          args: { task: msg.custom.subagent_task || '' },
          status: subagentRestoreStatus(msg.custom.subagent_status),
          accentIndex: Number.isInteger(msg.custom.subagent_accent_index)
            ? msg.custom.subagent_accent_index
            : undefined,
          result: msg.custom.subagent_result || '',
        });
      } else if (msg.custom?.source === 'bash_job') {
        const bashText = (msg.content || []).filter(x => x.type === 'text').map(x => x.text).join('');
        result.push({
          _type: 'tool_start',
          tool_call_id: 'bash_complete_' + result.length,
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
          const jobId = legacySubagentJobIds.get(subagentTaskIdentity(subagent.task));
          result.push({
            _type: 'tool_start',
            tool_call_id: jobId ? 'subagent-' + jobId : 'subagent_' + result.length,
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
              tool_call_id: 'bash_complete_' + result.length,
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
export function normalizeConversationProjection(raw) {
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
      };
    }
    return {
      role: item.role,
      _msg_id: item.id,
      content: item.text ? [{ type: 'text', text: item.text }] : [],
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

export function handleWsInit(id, data) {
  delete pendingTextDeltas[id];
  delete pendingThinkingDeltas[id];
  delete pendingToolDeltas[id];
	delete pendingBashDeltas[id];
  delete pendingToolCallBuffers[id];
  delete materializedTextDuringMessage[id];
  updateSession(id, {
    messages: normalizeHistory(data.messages || [], data.subagents),
    historyTruncated: !!data.history_truncated,
    state: data.state || 'idle',
    contextPercent: data.context_percent ?? -1,
    permissionMode: data.permission_mode || 'yolo',
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
    tasks: data.tasks || [],
    planMode: data.plan_mode || 'off',
    planFile: data.plan_file || null,
    costUSD: data.cost_usd || 0,
    subagents: initBashJobs(data.bash_jobs, initSubagents(data.subagents)),
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

export function handleWsMessageEnd(id, fullText, msgId = '', inputTokens = 0, outputTokens = 0) {
  const pendingText = pendingTextDeltas[id] || '';
  delete pendingTextDeltas[id];
  delete pendingThinkingDeltas[id];
  const sess = store.get().sessions[id];
  if (!sess) {
    delete materializedTextDuringMessage[id];
    return;
  }

  // Tally the run's live token counts. A run may span several model calls
  // (each tool round-trip is another call). The counts reset when the next run
  // begins (see handleWsStateChange) and are kept after the run ends until the
  // next one.
  //   ↓ output ACCUMULATES: each call generates new output tokens.
  //   ↑ input is the LAST call's value, NOT a sum: every call replays the whole
  //     accumulated context (system + prior turns + tool results), so summing
  //     would double-count the same context on every step and inflate ↑ wildly.
  //     The latest call's input already includes all tool results fed back so
  //     far, so it's the honest "how big is what the model is chewing on" number.
  //     Zero-input messages (rare) don't clobber the last real value.
  const runTokensUp = inputTokens > 0 ? inputTokens : (sess.runTokensUp || 0);
  const runTokensDown = (sess.runTokensDown || 0) + (outputTokens || 0);

  if (msgId && sess.messages.some(m => m._msg_id === msgId)) {
    delete materializedTextDuringMessage[id];
    updateSession(id, { streamingText: null, thinkingText: null, runTokensUp, runTokensDown });
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
    runTokensUp,
    runTokensDown,
  };
  if (assistantText) {
    const msg = { role: 'assistant', _msg_id: msgId || undefined, content: [{ type: 'text', text: assistantText }] };
    patch.messages = [...sess.messages, msg];
  }

  delete materializedTextDuringMessage[id];
  updateSession(id, patch);
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

  // Clean up buffer.
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
  const messages = sess.messages.map(m => {
    if (m._type === 'tool_start' && m.tool_call_id === data.tool_call_id) {
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

export function handleWsStateChange(id, data) {
  const state = store.get();
  const prev = state.sessions[id];
  const wasRunning = prev && (prev.state === 'running' || prev.state === 'permission');
  const patch = { state: data.state, error: data.error || null };
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
    if (wasRunning) {
      flashSession(id, data.state === 'error' ? 'error' : 'done');
      markUnseen(id);
      // Surface the reason for an error end so it's visible even when the tile
      // isn't focused — parity with the TUI's run-end error block. A usage/quota
      // limit reads as an actionable "resets in X" line rather than a fault.
      if (data.state === 'error' && data.error) {
        const isQuota = /quota exceeded|usage limit/i.test(data.error);
        addToast({
          sessionId: id,
          title: isQuota ? 'Usage limit reached' : 'Run failed',
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

export function handleWsAskUser(id, data) {
  updateSession(id, {
    pendingAsk: { id: data.id, questions: data.questions },
  });
  const state = store.get();
  const visible = visibleSessionIds(state);
  if (!visible.includes(id)) {
    flashSession(id, 'attention');
    const sess = state.sessions[id];
    if (sess) triggerAttention(sess, 'ask_user', state.soundEnabled);
  }
}

export function handleWsPermissionRequest(id, data) {
  updateSession(id, {
    state: 'permission',
    pendingPerm: {
      id: data.id,
      tool_name: data.tool_name,
      args: data.args,
      allow_pattern: data.allow_pattern || '',
    },
  });
  flashSession(id, 'attention');
  const state = store.get();
  const visible = visibleSessionIds(state);
  if (!visible.includes(id)) {
    const sess = state.sessions[id];
    if (sess) triggerAttention(sess, data.tool_name, state.soundEnabled);
  }
}

// Another client (or a run abort) resolved the permission — clear the modal
// so it doesn't hang on this client. Guard by id so a newer request survives.
export function handleWsPermissionResolved(id, data) {
  const sess = store.get().sessions[id];
  if (!sess || !sess.pendingPerm) return;
  if (data && data.id && sess.pendingPerm.id !== data.id) return;
  updateSession(id, { pendingPerm: null });
}

export function handleWsAskResolved(id, data) {
  const sess = store.get().sessions[id];
  if (!sess || !sess.pendingAsk) return;
  if (data && data.id && sess.pendingAsk.id !== data.id) return;
  updateSession(id, { pendingAsk: null });
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
function markUnseen(id) {
  const state = store.get();
  const visible = visibleSessionIds(state);
  const hidden = typeof document !== 'undefined' && document.hidden;
  if (visible.includes(id) && !hidden) return;
  const sess = state.sessions[id];
  if (sess && !sess.unseen) updateSession(id, { unseen: true });
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
  if (data.permission_mode) {
    patch.permissionMode = data.permission_mode;
  }
  updateSession(id, patch);
}

export function handleWsContextUpdate(id, data) {
  if (data.context_percent != null) {
    updateSession(id, { contextPercent: data.context_percent });
  }
}

export function handleWsSessionCost(id, data) {
  if (data.cost_usd != null) {
    updateSession(id, { costUSD: data.cost_usd });
  }
}

// handleWsRateLimit reflects a request's live rate-limit headers: a per-session
// "on extra usage" flag, the per-session 5h/weekly utilizations (the only usage
// source for OpenAI/Codex, which has no poller), plus — for Anthropic — an
// instant refresh of the global plan-usage snapshot (account-wide windows) so
// the widget doesn't lag the 60s poll. The extra-usage spend (€) stays sourced
// from the poller.
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

  // Add a subagent card to the chat (mirrors TUI's subagent block).
  const sess = store.get().sessions[id];
  if (!sess) return;
  const messages = [...(sess.messages || [])];
  const priorAccent = sess.subagents && sess.subagents[data.job_id]
    ? sess.subagents[data.job_id].accentIndex
    : undefined;
  messages.push({
    _type: 'tool_start',
    tool_call_id: `subagent-${data.job_id}`,
    tool_name: 'subagent',
    args: { task: data.task || '' },
    // Preserve cancelled distinctly (⊘) from a real failure (✗).
    status: data.status === 'completed' ? 'done' : data.status === 'cancelled' ? 'cancelled' : 'error',
    accentIndex: Number.isInteger(priorAccent) ? priorAccent : undefined,
    result: data.text || '',
  });
  updateSession(id, { messages });
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
      const existing = subs[jobId] || {
        jobId, task: '', model: '', status: 'running', async: true,
        messages: [], streamingText: null, thinkingText: null, usage: null,
      };
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
  updateSession(id, { subagents: subs });
}

// handleWsSubagentUsage applies the backend's live, cumulative token/cost
// tally for one subagent (subagent_usage). The backend sends the running
// total on every event, so this SETS sub.usage rather than accumulating it.
// Silently ignored if the subagent isn't known yet (e.g. usage arrived before
// subagent_start, or the subagent was already pruned).
export function handleWsSubagentUsage(id, jobId, inputTokens, outputTokens, costUSD) {
  const sess = store.get().sessions[id];
  if (!sess || !jobId) return;
  const existing = sess.subagents?.[jobId];
  if (!existing) return;
  const subs = {
    ...sess.subagents,
    [jobId]: {
      ...existing,
      usage: {
        inputTokens: inputTokens || 0,
        outputTokens: outputTokens || 0,
        costUSD: costUSD || 0,
      },
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
  const existing = subs[jobId];
  if (!existing) return;
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
  updateSession(id, { subagents: subs });
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

export function handleWsRunEnd(id) {
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
}

export function handleWsSteer(id, data) {
  const sess = store.get().sessions[id];
  if (!sess) return;
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
  const already = data.msg_id && sess.messages.some(m => m._msg_id === data.msg_id);
  const patch = { pendingSteers: steers.length > 0 ? steers : null };
  if (!already) {
    const userMsg = { role: 'user', _msg_id: data.msg_id || undefined, _steer_id: data.id || undefined, content: [{ type: 'text', text: data.text }] };
    patch.messages = [...sess.messages, userMsg];
  }
  updateSession(id, patch);
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

export function handleWsPlanMode(id, data) {
  updateSession(id, {
    planMode: data.mode || 'off',
    planFile: data.plan_file || null,
  });
}

export function handleWsCommand(id, data) {
  if (data.command === 'clear') {
    updateSession(id, { messages: [], streamingText: null, thinkingText: null });
  } else if (data.command === 'compact') {
    // Don't replace messages — display stays intact.
    // Append a compaction marker for visual feedback.
    const sess = store.get().sessions[id];
    if (sess) {
      const marker = { _type: 'system', text: '✂ Context compacted' };
      updateSession(id, { messages: [...sess.messages, marker] });
    }
  } else if (data.command === 'branch') {
    // Branch switched — reload messages from new branch path.
    if (data.messages) {
      updateSession(id, { messages: normalizeHistory(data.messages), historyTruncated: !!data.history_truncated });
    }
  }
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
      const lines = rest.split('\n');
      let task = '';
      let resultStart = 2;
      if (lines.length >= 2 && lines[1].startsWith('Task: ')) {
        task = lines[1].slice('Task: '.length);
      }
      let result = lines.slice(resultStart).join('\n').trim();
      // Strip known result prefixes
      for (const p of ['Result (last 50 lines):\n', 'Error: ']) {
        if (result.startsWith(p)) {
          result = result.slice(p.length).trim();
          break;
        }
      }
      return { task, status, result };
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

export function handleWsAutoVerifyStart(id) {
  updateSession(id, { autoVerifying: true });
}

export function handleWsAutoVerifyEnd(id, data) {
  updateSession(id, { autoVerifying: false });
}

export function handleWsCompactionStart(id) {
  updateSession(id, { compacting: true });
}

export function handleWsCompactionEnd(id) {
  updateSession(id, { compacting: false });
}
