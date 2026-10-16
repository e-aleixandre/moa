// call-requests.js — the owners' requests to talk, live on the client.
//
// /api/voice/requests/ws sends the complete list on every change, so there is
// nothing to merge: the slice is replaced. A reconnect gets the current list
// first, and a failed socket falls back to nothing worse than a late bell.

import { api } from './api.js';
import { store, setState } from './store.js';
import { openOwnerConversation, ownersSlice } from './owners.js';
import { startVoiceCall, hangupVoiceCall } from './voice-call-session.js';

const EMPTY = Object.freeze({ list: [], loaded: false });

export function callRequestsSlice(state) {
  return state?.callRequests || EMPTY;
}

export function setCallRequests(list) {
  setState({ callRequests: { list: Array.isArray(list) ? list : [], loaded: true } });
}

export function loadCallRequests() {
  return api('GET', '/api/voice/requests')
    .then((res) => setCallRequests(res?.requests))
    .catch(() => {});
}

// ── Actions ───────────────────────────────────────────────────────────────

// answerCallRequest picks up a ringing request: the call is opened with the
// request's id, which the server claims for this answer.
export function answerCallRequest(request) {
  if (!request?.session_id) return false;
  return startVoiceCall(request.session_id, { callRequestId: request.id });
}

// writeCallRequest is Write it instead on a ringing request and Cancel on a
// held one. A 409 means it was already gone, which is the same outcome.
export function writeCallRequest(id) {
  return api('POST', `/api/voice/requests/${encodeURIComponent(id)}/write`).catch(() => {});
}

// callHeldRequest is Call on a held request: the server keeps it for the
// moment the current call is hung up, then the call to the owner opens.
export async function callHeldRequest(request) {
  try {
    await api('POST', `/api/voice/requests/${encodeURIComponent(request.id)}/accept`);
  } catch {
    return false;
  }
  await hangupVoiceCall();
  return startVoiceCall(request.session_id, { callRequestId: request.id });
}

// ── The socket ────────────────────────────────────────────────────────────

let socket = null;
let backoff = 1000;
let retry = null;
let started = false;

function connect() {
  clearTimeout(retry);
  if (typeof WebSocket === 'undefined' || typeof location === 'undefined') return;
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  let ws;
  try {
    ws = new WebSocket(`${proto}//${location.host}/api/voice/requests/ws`);
  } catch {
    schedule();
    return;
  }
  socket = ws;
  ws.onmessage = (e) => {
    if (socket !== ws) return;
    backoff = 1000;
    let evt = null;
    try { evt = JSON.parse(e.data); } catch { return; }
    if (evt?.type === 'call_requests') setCallRequests(evt.requests);
  };
  ws.onclose = () => {
    if (socket !== ws) return;
    socket = null;
    schedule();
  };
}

function schedule() {
  if (!started) return;
  clearTimeout(retry);
  retry = setTimeout(connect, backoff);
  backoff = Math.min(backoff * 2, 30000);
}

export function startCallRequestsSync() {
  if (started) return;
  started = true;
  loadCallRequests();
  connect();
}

// Back in the foreground: a backgrounded PWA loses its socket, and a request
// may have rung meanwhile.
export function resumeCallRequestsSync() {
  if (!started) return;
  loadCallRequests();
  if (!socket || socket.readyState > 1) {
    backoff = 1000;
    connect();
  }
}

export function currentCallRequests() {
  return callRequestsSlice(store.get()).list;
}

// openCallRequestLink is the notification's ?call_request=<id>: it opens the
// owner's conversation when the request is still alive. The incoming card
// shows by itself; a request that already ended opens nothing.
export async function openCallRequestLink(id) {
  if (!id) return false;
  await loadCallRequests();
  const request = currentCallRequests().find((r) => r.id === id);
  if (!request) return false;
  const owner = (ownersSlice(store.get()).list || []).find((o) => o.id === request.owner_id);
  return openOwnerConversation(owner || { session_id: request.session_id });
}
