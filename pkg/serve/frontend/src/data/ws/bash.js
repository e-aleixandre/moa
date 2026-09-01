// WebSocket background bash event handling.

import { wsState } from './shared.js';
import { truncateText } from '../util/format.js';
import { addToast } from '../notifications.js';
import { store, updateSession } from '../store.js';
import { isSessionAway } from './attention.js';
import { markUnseen } from './attention.js';
import { attachBashJob } from './init.js';
import { scheduleFlush } from './stream.js';

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


export function handleWsBashJobStart(id, data) {
  const sess = store.get().sessions[id];
  if (!sess || !data.job_id) return;
  updateSession(id, { subagents: attachBashJob(sess.subagents || {}, data) });
}

export function handleWsBashJobOutput(id, data) {
  if (!store.get().sessions[id] || !data.job_id || !data.delta) return;
  wsState.pendingBashDeltas[id] = wsState.pendingBashDeltas[id] || {};
  const existing = wsState.pendingBashDeltas[id][data.job_id];
  wsState.pendingBashDeltas[id][data.job_id] = {
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

