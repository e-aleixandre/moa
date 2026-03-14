// ws-handlers.js — WebSocket event handlers and streaming delta batching

import { triggerAttention, triggerDone } from './notifications.js';
import { store, updateSession, visibleSessionIds } from './store.js';

// --- Message normalization ---

export function normalizeHistory(raw) {
  const result = [];
  const resultMap = {};
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
            result.push({ role: 'assistant', content: [{ type: 'text', text: textParts.join('') }] });
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
          });
        }
      }
      if (textParts.length > 0) {
        result.push({ role: 'assistant', content: [{ type: 'text', text: textParts.join('') }] });
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
    } else if (msg.role === 'user') {
      result.push(msg);
    }
  }
  return result;
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

// --- Streaming delta batching ---

const pendingTextDeltas = {};
const pendingThinkingDeltas = {};
const pendingToolDeltas = {};
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
  ]);

  for (const id of sessionIds) {
    const sess = state.sessions[id];
    if (!sess) {
      delete pendingTextDeltas[id];
      delete pendingThinkingDeltas[id];
      delete pendingToolDeltas[id];
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

    if (Object.keys(patch).length > 0) {
      updateSession(id, patch);
    }
  }
}

// --- WS event handlers ---

export function handleWsInit(id, data) {
  delete pendingTextDeltas[id];
  delete pendingThinkingDeltas[id];
  delete pendingToolDeltas[id];
  updateSession(id, {
    messages: normalizeHistory(data.messages || []),
    state: data.state || 'idle',
    contextPercent: data.context_percent ?? -1,
    permissionMode: data.permission_mode || 'yolo',
    pendingPerm: data.pending_permission || null,
    pendingAsk: data.pending_ask || (store.get().sessions[id]?.pendingAsk || null),
    streamingText: null,
    thinkingText: null,
    tasks: data.tasks || [],
    planMode: data.plan_mode || 'off',
    planFile: data.plan_file || null,
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

export function handleWsMessageEnd(id, fullText) {
  delete pendingTextDeltas[id];
  delete pendingThinkingDeltas[id];
  const sess = store.get().sessions[id];
  if (!sess) return;
  const msg = { role: 'assistant', content: [{ type: 'text', text: fullText }] };
  updateSession(id, {
    messages: [...sess.messages, msg],
    streamingText: null,
    thinkingText: null,
  });
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
        status: 'running',
      };
    });
    updateSession(id, { messages });
    return;
  }

  const toolMsg = {
    _type: 'tool_start',
    tool_call_id: data.tool_call_id,
    tool_name: data.tool_name,
    args: data.args,
    status: 'running',
    result: null,
  };
  updateSession(id, { messages: [...sess.messages, toolMsg] });
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

  const messages = sess.messages.map(m => {
    if (m._type === 'tool_start' && m.tool_call_id === data.tool_call_id) {
      return { ...m, status: nextStatus, result: data.result, streamingResult: null };
    }
    return m;
  });
  updateSession(id, { messages });
}

export function handleWsStateChange(id, data) {
  const state = store.get();
  const prev = state.sessions[id];
  const wasRunning = prev && (prev.state === 'running' || prev.state === 'permission');
  updateSession(id, { state: data.state, error: data.error || null });
  if (data.state === 'idle' || data.state === 'error') {
    const sess = store.get().sessions[id];
    if (sess) updateSession(id, { streamingText: null, thinkingText: null, pendingSteers: null });
    if (wasRunning) {
      flashSession(id, data.state === 'error' ? 'error' : 'done');
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
    pendingPerm: { id: data.id, tool_name: data.tool_name, args: data.args },
  });
  flashSession(id, 'attention');
  const state = store.get();
  const visible = visibleSessionIds(state);
  if (!visible.includes(id)) {
    const sess = state.sessions[id];
    if (sess) triggerAttention(sess, data.tool_name, state.soundEnabled);
  }
}

function flashSession(id, type) {
  updateSession(id, { flash: type });
  setTimeout(() => {
    if (store.get().sessions[id]?.flash === type) updateSession(id, { flash: null });
  }, 1300);
}

export function handleWsConfigChange(id, data) {
  const sess = store.get().sessions[id];
  const patch = {
    model: data.model || sess?.model,
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

export function handleWsSubagentCount(id, count) {
  updateSession(id, { subagentCount: count });
}

export function handleWsSubagentComplete(id, data) {
  const sess = store.get().sessions[id];
  if (!sess) return;
  updateSession(id, { messages: [...sess.messages, { _type: 'system', text: data.text }] });
}

export function handleWsRunEnd(id) {
  delete pendingTextDeltas[id];
  delete pendingThinkingDeltas[id];
  delete pendingToolDeltas[id];
  updateSession(id, { streamingText: null, thinkingText: null, pendingSteers: null });
}

export function handleWsSteer(id, data) {
  const sess = store.get().sessions[id];
  if (!sess) return;
  const userMsg = { role: 'user', content: [{ type: 'text', text: data.text }] };
  const steers = [...(sess.pendingSteers || [])];
  const idx = steers.indexOf(data.text);
  if (idx >= 0) steers.splice(idx, 1);
  updateSession(id, {
    messages: [...sess.messages, userMsg],
    pendingSteers: steers.length > 0 ? steers : null,
  });
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
    if (data.messages) {
      updateSession(id, { messages: normalizeHistory(data.messages) });
    }
  }
}
