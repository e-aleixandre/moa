// conversation-reducer.js — pure conversation state transitions.
//
// These functions turn a stream of (already-parsed) conversation events into
// a message list, WITHOUT touching the global store or producing side effects
// (no notifications, no flashing). They operate on a plain "target" object:
//
//   { messages: [], streamingText: string|null, thinkingText: string|null }
//
// plus a per-target "buffers" object used to stitch streaming state across
// events (materialized text, buffered tool-call args). The session handlers
// and the subagent handlers share these so a subagent's transcript renders
// exactly like the main conversation.
//
// Each reducer mutates `target`/`buffers` in place and returns `target`.
// Callers are responsible for persisting the result (e.g. updateSession) and
// for any batching of high-frequency deltas.

export function newBuffers() {
  return {
    materializedText: '', // text already flushed into a message this turn
    toolCallArgs: {},      // toolCallId → args, buffered before tool_call_start
  };
}

export function ensureTarget(target) {
  if (!target) return { messages: [], streamingText: null, thinkingText: null };
  if (!Array.isArray(target.messages)) target.messages = [];
  return target;
}

export function reduceMessageStart(target, buffers) {
  target = ensureTarget(target);
  target.streamingText = null;
  target.thinkingText = null;
  buffers.materializedText = '';
  return target;
}

export function reduceTextDelta(target, buffers, delta) {
  target = ensureTarget(target);
  if (delta) target.streamingText = (target.streamingText || '') + delta;
  return target;
}

export function reduceThinkingDelta(target, buffers, delta) {
  target = ensureTarget(target);
  if (delta) target.thinkingText = (target.thinkingText || '') + delta;
  return target;
}

// reduceToolCallStart materializes any pending streaming text into a message
// (to keep chronological order) then appends a generating tool block.
export function reduceToolCallStart(target, buffers, data) {
  target = ensureTarget(target);

  // If a block already exists for this tool call, only advance from generating.
  const existingIdx = target.messages.findIndex(
    m => m._type === 'tool_start' && m.tool_call_id === data.tool_call_id,
  );
  if (existingIdx >= 0) {
    const existing = target.messages[existingIdx];
    if (existing.status === 'generating') {
      target.messages = target.messages.map((m, i) =>
        i === existingIdx ? { ...m, tool_name: data.tool_name } : m);
    }
    return target;
  }

  const textToMaterialize = target.streamingText || '';
  if (textToMaterialize) {
    target.messages = [...target.messages, {
      role: 'assistant',
      content: [{ type: 'text', text: textToMaterialize }],
    }];
    target.streamingText = null;
    buffers.materializedText = (buffers.materializedText || '') + textToMaterialize;
  }
  if (target.thinkingText) target.thinkingText = null;

  const buffered = buffers.toolCallArgs[data.tool_call_id];
  target.messages = [...target.messages, {
    _type: 'tool_start',
    tool_call_id: data.tool_call_id,
    tool_name: data.tool_name,
    args: buffered || {},
    status: 'generating',
    result: null,
  }];
  if (buffered) delete buffers.toolCallArgs[data.tool_call_id];
  return target;
}

export function reduceToolCallDelta(target, buffers, data) {
  target = ensureTarget(target);
  const idx = target.messages.findIndex(
    m => m._type === 'tool_start' && m.tool_call_id === data.tool_call_id,
  );
  if (idx >= 0) {
    target.messages = target.messages.map((m, i) =>
      i === idx ? { ...m, args: data.args } : m);
  } else {
    buffers.toolCallArgs[data.tool_call_id] = data.args;
  }
  return target;
}

export function reduceToolStart(target, buffers, data) {
  target = ensureTarget(target);
  const existingIdx = target.messages.findIndex(
    m => m._type === 'tool_start' && m.tool_call_id === data.tool_call_id,
  );
  if (existingIdx >= 0) {
    target.messages = target.messages.map((m, i) =>
      i === existingIdx
        ? { ...m, tool_name: data.tool_name, args: data.args, start_line: data.start_line, status: 'running' }
        : m);
    return target;
  }
  target.messages = [...target.messages, {
    _type: 'tool_start',
    tool_call_id: data.tool_call_id,
    tool_name: data.tool_name,
    args: data.args,
    start_line: data.start_line,
    status: 'running',
    result: null,
  }];
  return target;
}

export function reduceToolUpdate(target, buffers, data) {
  target = ensureTarget(target);
  target.messages = target.messages.map(m => {
    if (m._type === 'tool_start' && m.tool_call_id === data.tool_call_id) {
      return { ...m, streamingResult: (m.streamingResult || '') + data.delta };
    }
    return m;
  });
  return target;
}

export function reduceToolEnd(target, buffers, data, extractNote) {
  target = ensureTarget(target);
  const nextStatus = data.rejected === true
    ? 'rejected'
    : (data.is_error ? 'error' : 'done');
  const note = extractNote ? extractNote(data.result, data.rejected === true) : null;
  target.messages = target.messages.map(m => {
    if (m._type === 'tool_start' && m.tool_call_id === data.tool_call_id) {
      return { ...m, status: nextStatus, result: data.result, streamingResult: null, note };
    }
    return m;
  });
  return target;
}

// reduceMessageEnd finalizes the assistant message. fullText is authoritative
// (repairs deltas dropped under backpressure). When tool calls already
// materialized part of the text, derive the remaining tail from fullText; if
// they diverge, fall back to the streamed tail rather than duplicate.
//
// NOTE: buffers.materializedText is a string (possibly ""). Storing a boolean
// here would break startsWith/slice — a fullText starting with "true" would be
// silently truncated. Keep it a string.
export function reduceMessageEnd(target, buffers, fullText) {
  target = ensureTarget(target);
  const streamed = target.streamingText || '';
  const materialized = buffers.materializedText || '';
  let assistantText;
  if (!materialized) {
    assistantText = fullText || streamed;
  } else if (fullText && fullText.startsWith(materialized)) {
    assistantText = fullText.slice(materialized.length);
  } else {
    assistantText = streamed;
  }
  target.streamingText = null;
  target.thinkingText = null;
  if (assistantText) {
    target.messages = [...target.messages, {
      role: 'assistant',
      content: [{ type: 'text', text: assistantText }],
    }];
  }
  buffers.materializedText = '';
  return target;
}

export function reduceRunEnd(target, buffers) {
  target = ensureTarget(target);
  target.messages = target.messages.map(m => {
    if (m._type === 'tool_start' && m.status === 'generating') {
      return { ...m, status: 'error', result: 'Run ended before execution' };
    }
    return m;
  });
  target.streamingText = null;
  target.thinkingText = null;
  buffers.materializedText = '';
  return target;
}

// applyNestedEvent routes one nested WS event ({type, data}) to the right
// reducer for a target. Used by the subagent handlers, where events arrive
// wrapped inside subagent_event. Returns the updated target.
export function applyNestedEvent(target, buffers, evt, extractNote) {
  const data = evt.data || {};
  switch (evt.type) {
    case 'message_start': return reduceMessageStart(target, buffers);
    case 'text_delta':    return reduceTextDelta(target, buffers, data.delta);
    case 'thinking_delta':return reduceThinkingDelta(target, buffers, data.delta);
    case 'message_end':   return reduceMessageEnd(target, buffers, data.text);
    case 'tool_call_start':return reduceToolCallStart(target, buffers, data);
    case 'tool_call_delta':return reduceToolCallDelta(target, buffers, data);
    case 'tool_start':    return reduceToolStart(target, buffers, data);
    case 'tool_update':   return reduceToolUpdate(target, buffers, data);
    case 'tool_end':      return reduceToolEnd(target, buffers, data, extractNote);
    case 'run_end':       return reduceRunEnd(target, buffers);
    default:              return ensureTarget(target);
  }
}
