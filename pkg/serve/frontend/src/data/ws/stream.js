// WebSocket streaming and run event handling.

import { wsState } from './shared.js';
import { store, setState, updateSession } from '../store.js';
import { markUnseen, acknowledgeVisibleLiveAttention } from './attention.js';
import { parseSubagentNotification, skillForkLaunchRow } from './history.js';
import { nextRunEpoch } from './init.js';

export function scheduleFlush() {
  if (wsState.flushScheduled) return;
  wsState.flushScheduled = true;
  requestAnimationFrame(flushDeltas);
}

export function flushDeltas() {
  wsState.flushScheduled = false;
  const state = store.get();

  const sessionIds = new Set([
    ...Object.keys(wsState.pendingTextDeltas),
    ...Object.keys(wsState.pendingThinkingDeltas),
    ...Object.keys(wsState.pendingToolDeltas),
    ...Object.keys(wsState.pendingBashDeltas),
  ]);

  const patches = {};
  for (const id of sessionIds) {
    const sess = state.sessions[id];
    if (!sess) {
      delete wsState.pendingTextDeltas[id];
      delete wsState.pendingThinkingDeltas[id];
      delete wsState.pendingToolDeltas[id];
      delete wsState.pendingBashDeltas[id];
      continue;
    }
    const patch = {};

    if (wsState.pendingTextDeltas[id]) {
      patch.streamingText = (sess.streamingText || '') + wsState.pendingTextDeltas[id];
      delete wsState.pendingTextDeltas[id];
    }

    if (wsState.pendingThinkingDeltas[id]) {
      patch.thinkingText = (sess.thinkingText || '') + wsState.pendingThinkingDeltas[id];
      delete wsState.pendingThinkingDeltas[id];
    }

    if (wsState.pendingToolDeltas[id]) {
      let messages = patch.messages || sess.messages;
      let changed = false;
      for (const [toolCallId, delta] of Object.entries(wsState.pendingToolDeltas[id])) {
        messages = messages.map(m => {
          if (m._type === 'tool_start' && m.tool_call_id === toolCallId) {
            changed = true;
            return { ...m, streamingResult: (m.streamingResult || '') + delta };
          }
          return m;
        });
      }
      if (changed) patch.messages = messages;
      delete wsState.pendingToolDeltas[id];
    }

    if (wsState.pendingBashDeltas[id]) {
      const subagents = { ...(patch.subagents || sess.subagents || {}) };
      for (const [jobId, pending] of Object.entries(wsState.pendingBashDeltas[id])) {
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
      delete wsState.pendingBashDeltas[id];
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

export function handleWsMessageStart(id) {
  delete wsState.pendingTextDeltas[id];
  delete wsState.pendingThinkingDeltas[id];
  delete wsState.materializedTextDuringMessage[id];
  if (!store.get().sessions[id]) return;
  updateSession(id, {
    streamingText: null,
    thinkingText: null,
  });
}

export function handleWsTextDelta(id, delta) {
  if (!store.get().sessions[id]) return;
  wsState.pendingTextDeltas[id] = (wsState.pendingTextDeltas[id] || '') + delta;
  scheduleFlush();
}

export function handleWsThinkingDelta(id, delta) {
  if (!store.get().sessions[id]) return;
  wsState.pendingThinkingDeltas[id] = (wsState.pendingThinkingDeltas[id] || '') + delta;
  scheduleFlush();
}

export function handleWsMessageEnd(id, fullText, msgId = '') {
  const pendingText = wsState.pendingTextDeltas[id] || '';
  delete wsState.pendingTextDeltas[id];
  delete wsState.pendingThinkingDeltas[id];
  const sess = store.get().sessions[id];
  if (!sess) {
    delete wsState.materializedTextDuringMessage[id];
    return;
  }

  if (msgId && sess.messages.some(m => m._msg_id === msgId)) {
    delete wsState.materializedTextDuringMessage[id];
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
  const materialized = wsState.materializedTextDuringMessage[id] || '';
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

  delete wsState.materializedTextDuringMessage[id];
  updateSession(id, patch);
}

export function handleWsRunTokens(id, data) {
  updateSession(id, { runTokensUp: data.up || 0, runTokensDown: data.down || 0, runEpoch: nextRunEpoch(id) });
}


export function handleWsRunEnd(id, data = {}, seq = 0) {
  delete wsState.pendingTextDeltas[id];
  delete wsState.pendingThinkingDeltas[id];
  delete wsState.pendingToolDeltas[id];
	delete wsState.pendingBashDeltas[id];
  delete wsState.pendingToolCallBuffers[id];
  delete wsState.materializedTextDuringMessage[id];

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
  // A skill the user launched with /<name>: same launch row live and on reload.
  const forkRow = skillForkLaunchRow(data);
  if (forkRow) {
    updateSession(id, { messages: [...messages, forkRow] });
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
    // A forked skill launched while the agent was busy arrives on this lane
    // (an internal steer), not as a user_message. Same row either way.
    const forkRow = skillForkLaunchRow(data);
    if (forkRow) {
      patch.messages = [...messages, forkRow];
      updateSession(id, patch);
      return;
    }
    if (!isStructuredSubagentNotification(sess, data.text || '')) {
		const userMsg = { role: 'user', _msg_id: data.msg_id || undefined, _steer_id: data.id || undefined, content, custom: data.custom };
		patch.messages = [...messages, userMsg];
    }
  }
  updateSession(id, patch);
}

export function secretBatchFromMessage(data) {
  if (data?.custom?.source === 'secret_batch') {
    return Array.isArray(data.custom.secret_aliases) ? data.custom.secret_aliases : [];
  }
  return null;
}

export function isStructuredSubagentNotification(session, text) {
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
