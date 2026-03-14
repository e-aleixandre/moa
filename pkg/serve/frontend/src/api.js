// api.js — fetch helpers + centralized WS manager

import {
  handleWsInit, handleWsTextDelta, handleWsThinkingDelta,
  handleWsMessageEnd, handleWsToolStart, handleWsToolUpdate, handleWsToolEnd,
  handleWsStateChange, handleWsPermissionRequest,
  handleWsConfigChange,
  handleWsSubagentCount, handleWsSubagentComplete, handleWsRunEnd,
  handleWsCommand, handleWsTasksUpdate, handleWsPlanMode,
  handleWsAskUser, handleWsContextUpdate, handleWsSteer,
} from './state.js';

const HEADERS = { 'Content-Type': 'application/json', 'X-Moa-Request': '1' };

export async function api(method, path, body) {
  const opts = { method, headers: HEADERS };
  if (body) opts.body = JSON.stringify(body);
  const r = await fetch(path, opts);
  if (!r.ok) throw new Error(`${r.status}: ${await r.text()}`);
  if (r.status === 204) return null;
  const text = await r.text();
  if (!text) return null;
  return JSON.parse(text);
}

// --- Centralized WS Manager ---

const connections = new Map();    // sessionId → { ws, backoff, timer }
const pendingTimers = new Map();  // sessionId → timeoutId (for reconnects awaiting retry)
const wantedIds = new Set();      // sessions that should have a connection
const MAX_BACKOFF = 16000;

export function syncConnections(visibleIds) {
  wantedIds.clear();
  for (const id of visibleIds) wantedIds.add(id);

  // Close connections and cancel pending reconnects for sessions no longer visible
  for (const [id, entry] of connections) {
    if (!wantedIds.has(id)) {
      entry.ws.close();
      connections.delete(id);
    }
  }
  for (const [id, timer] of pendingTimers) {
    if (!wantedIds.has(id)) {
      clearTimeout(timer);
      pendingTimers.delete(id);
    }
  }

  // Open connections for newly visible sessions (that aren't already connecting/pending)
  for (const id of visibleIds) {
    if (!connections.has(id) && !pendingTimers.has(id)) {
      openWs(id, 1000);
    }
  }
}

function openWs(sessionId, initialBackoff) {
  pendingTimers.delete(sessionId);
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  const ws = new WebSocket(`${proto}//${location.host}/api/sessions/${sessionId}/ws`);
  const entry = { ws, backoff: initialBackoff };
  connections.set(sessionId, entry);

  ws.onmessage = (e) => {
    const evt = JSON.parse(e.data);
    if (evt.type === 'init') entry.backoff = 1000; // reset on successful handshake
    routeEvent(sessionId, evt);
  };

  ws.onclose = () => {
    if (connections.get(sessionId)?.ws !== ws) return; // superseded
    connections.delete(sessionId);
    if (!wantedIds.has(sessionId)) return; // intentionally removed
    // Reconnect with exponential backoff (read from entry — may have been reset by init)
    const delay = entry.backoff;
    const nextBackoff = Math.min(delay * 2, MAX_BACKOFF);
    const timer = setTimeout(() => {
      pendingTimers.delete(sessionId);
      if (wantedIds.has(sessionId) && !connections.has(sessionId)) {
        openWs(sessionId, nextBackoff);
      }
    }, delay);
    pendingTimers.set(sessionId, timer);
  };

  ws.onerror = () => {
    ws.close(); // triggers onclose → reconnect
  };
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
    case 'ask_user':
      handleWsAskUser(sessionId, evt.data);
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
    case 'steer':
      handleWsSteer(sessionId, evt.data);
      break;
    case 'context_update':
      handleWsContextUpdate(sessionId, evt.data);
      break;
  }
}
