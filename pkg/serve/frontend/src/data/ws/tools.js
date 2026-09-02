// WebSocket tool-call event handling.

import { wsState } from './shared.js';
import { store, updateSession } from '../store.js';
import { extractToolNote } from './history.js';
import { scheduleFlush } from './stream.js';

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
  const pendingText = wsState.pendingTextDeltas[id] || '';
  const pendingThinking = wsState.pendingThinkingDeltas[id] || '';
  delete wsState.pendingTextDeltas[id];
  delete wsState.pendingThinkingDeltas[id];

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
    wsState.materializedTextDuringMessage[id] = (wsState.materializedTextDuringMessage[id] || '') + textToMaterialize;
  }
  if (thinkingToClear) {
    patch.thinkingText = null;
  }

  // Check if we have buffered args from early deltas.
  const buffered = wsState.pendingToolCallBuffers[id]?.[data.tool_call_id];

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
    delete wsState.pendingToolCallBuffers[id][data.tool_call_id];
    if (Object.keys(wsState.pendingToolCallBuffers[id]).length === 0) {
      delete wsState.pendingToolCallBuffers[id];
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
    if (!wsState.pendingToolCallBuffers[id]) wsState.pendingToolCallBuffers[id] = {};
    wsState.pendingToolCallBuffers[id][data.tool_call_id] = { args: data.args };
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
  if (!wsState.pendingToolDeltas[id]) wsState.pendingToolDeltas[id] = {};
  wsState.pendingToolDeltas[id][data.tool_call_id] =
    (wsState.pendingToolDeltas[id][data.tool_call_id] || '') + data.delta;
  scheduleFlush();
}

export function handleWsToolEnd(id, data) {
  if (wsState.pendingToolDeltas[id]) {
    delete wsState.pendingToolDeltas[id][data.tool_call_id];
    if (Object.keys(wsState.pendingToolDeltas[id]).length === 0) delete wsState.pendingToolDeltas[id];
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
