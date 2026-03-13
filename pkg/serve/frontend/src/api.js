// api.js — fetch helpers + centralized WS manager

import {
  handleWsInit, handleWsTextDelta, handleWsThinkingDelta,
  handleWsMessageEnd, handleWsToolStart, handleWsToolUpdate, handleWsToolEnd,
  handleWsStateChange, handleWsPermissionRequest,
  handleWsConfigChange,
  handleWsSubagentCount, handleWsSubagentComplete, handleWsRunEnd,
  handleWsCommand, handleWsTasksUpdate, handleWsPlanMode,
} from './state.js';

const HEADERS = { 'Content-Type': 'application/json', 'X-Moa-Request': '1' };

export async function api(method, path, body) {
  const opts = { method, headers: HEADERS };
  if (body) opts.body = JSON.stringify(body);
  const r = await fetch(path, opts);
  if (!r.ok) throw new Error(`${r.status}: ${await r.text()}`);
  if (r.status === 204 || r.status === 202) return null;
  return r.json();
}

// --- Centralized WS Manager ---

const connections = new Map(); // sessionId → WebSocket

export function syncConnections(visibleIds) {
  const wantedSet = new Set(visibleIds);

  // Close connections that are no longer visible
  for (const [id, ws] of connections) {
    if (!wantedSet.has(id)) {
      ws.close();
      connections.delete(id);
    }
  }

  // Open connections for newly visible sessions
  for (const id of visibleIds) {
    if (!connections.has(id)) {
      openWs(id);
    }
  }
}

function openWs(sessionId) {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  const ws = new WebSocket(`${proto}//${location.host}/api/sessions/${sessionId}/ws`);

  ws.onmessage = (e) => {
    const evt = JSON.parse(e.data);
    routeEvent(sessionId, evt);
  };

  ws.onclose = () => {
    // Only remove if still the current connection for this session
    if (connections.get(sessionId) === ws) {
      connections.delete(sessionId);
    }
  };

  ws.onerror = () => {
    ws.close();
  };

  connections.set(sessionId, ws);
}

function routeEvent(sessionId, evt) {
  switch (evt.type) {
    case 'init':
      handleWsInit(sessionId, evt.data);
      break;
    case 'text_delta':
      handleWsTextDelta(sessionId, evt.data.delta);
      break;
    case 'thinking_delta':
      handleWsThinkingDelta(sessionId, evt.data.delta);
      break;
    case 'message_end':
      handleWsMessageEnd(sessionId, evt.data.text);
      break;
    case 'tool_start':
      handleWsToolStart(sessionId, evt.data);
      break;
    case 'tool_update':
      handleWsToolUpdate(sessionId, evt.data);
      break;
    case 'tool_end':
      handleWsToolEnd(sessionId, evt.data);
      break;
    case 'state_change':
      handleWsStateChange(sessionId, evt.data);
      break;
    case 'permission_request':
      handleWsPermissionRequest(sessionId, evt.data);
      break;
    case 'config_change':
      handleWsConfigChange(sessionId, evt.data);
      break;
    case 'subagent_count':
      handleWsSubagentCount(sessionId, evt.data.count);
      break;
    case 'subagent_complete':
      handleWsSubagentComplete(sessionId, evt.data);
      break;
    case 'run_end':
      handleWsRunEnd(sessionId);
      break;
    case 'command':
      handleWsCommand(sessionId, evt.data);
      break;
    case 'tasks_update':
      handleWsTasksUpdate(sessionId, evt.data);
      break;
    case 'plan_mode':
      handleWsPlanMode(sessionId, evt.data);
      break;
  }
}
