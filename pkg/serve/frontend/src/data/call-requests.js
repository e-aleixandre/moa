// call-requests.js — the owners' requests to talk, live on the client.
//
// /api/voice/requests/ws sends the complete list on every change, so there is
// nothing to merge: the slice is replaced. A reconnect gets the current list
// first, and a failed socket falls back to nothing worse than a late bell.

import { api } from './api.js';
import { store, setState } from './store.js';
import { openOwnerConversation, ownersSlice } from './owners.js';
import { closeEndedCallNotificationsNow } from './call-notifications.js';
import { presenceFrame, PRESENCE_RENEW_MS } from './presence.js';
import { startVoiceCall, hangupVoiceCall } from './voice-call-session.js';

const EMPTY = Object.freeze({ list: [], loaded: false });

export function callRequestsSlice(state) {
  return state?.callRequests || EMPTY;
}

export function setCallRequests(list) {
  setState({ callRequests: { list: Array.isArray(list) ? list : [], loaded: true } });
  closeEndedCallNotificationsNow(list);
}

// feedSeq counts the snapshots the socket has delivered. A GET that started
// before the latest one is older than what is on screen, and is dropped.
let feedSeq = 0;

export function loadCallRequests() {
  const seq = feedSeq;
  return api('GET', '/api/voice/requests')
    .then((res) => { if (seq === feedSeq) setCallRequests(res?.requests); })
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
// held one. A 409 means it was already gone, which is the same outcome; any
// other failure leaves the request where it is and rejects, so the card that
// asked can say so (a toast would sit under the phone's call screen).
export async function writeCallRequest(id) {
  try {
    await api('POST', `/api/voice/requests/${encodeURIComponent(id)}/write`);
  } catch (error) {
    if (error?.status !== 409) throw error;
  }
  return true;
}

// callHeldRequest is Call on a held request: the server keeps it for the
// moment the current call is hung up, then the call to the owner opens.
export async function callHeldRequest(request) {
  try {
    await api('POST', `/api/voice/requests/${encodeURIComponent(request.id)}/accept`);
  } catch (error) {
    // 409: it is already gone. Anything else leaves the current call alone
    // and rejects, for the card to show.
    if (error?.status !== 409) throw error;
    return false;
  }
  await hangupVoiceCall();
  const started = startVoiceCall(request.session_id, { callRequestId: request.id });
  // The call must open where its controls are: on a phone that is the call
  // screen, elsewhere the owner's conversation.
  if (started && !store.get().isMobile) {
    const owner = (ownersSlice(store.get()).list || []).find((o) => o.id === request.owner_id);
    openOwnerConversation(owner || { session_id: request.session_id });
  }
  return started;
}

// ── The socket ────────────────────────────────────────────────────────────

// startFeedPresence tells the server whether this document is visible, on the
// requests feed: a client showing Tasks, the inbox or a detail still rings from
// its incoming card, which the conversation presence cannot say. Same frame and
// renewal as that presence; the server lets a report lapse after ~45 s.
export function startFeedPresence(ws, doc = globalThis.document, timers = globalThis) {
  if (!doc) return () => {};
  const report = (renew) => {
    const visible = doc.visibilityState === 'visible';
    if (ws.readyState !== 1 || (renew && !visible)) return;
    ws.send(presenceFrame(visible));
  };
  report(false);
  const timer = timers.setInterval(() => report(true), PRESENCE_RENEW_MS);
  const onVisibility = () => report(false);
  doc.addEventListener('visibilitychange', onVisibility);
  return () => {
    timers.clearInterval(timer);
    doc.removeEventListener('visibilitychange', onVisibility);
  };
}

let socket = null;
let stopPresence = () => {};
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
  ws.onopen = () => {
    if (socket !== ws) return;
    stopPresence();
    stopPresence = startFeedPresence(ws);
  };
  ws.onmessage = (e) => {
    if (socket !== ws) return;
    backoff = 1000;
    let evt = null;
    try { evt = JSON.parse(e.data); } catch { return; }
    if (evt?.type === 'call_requests') {
      feedSeq += 1;
      setCallRequests(evt.requests);
      // A call starting or ending changes a question's call state in For
      // you; the server records the end just after the feed says so.
      import('./for-you.js').then((m) => { m.loadForYou(); setTimeout(() => m.loadForYou(), 2000); }).catch(() => {});
    }
  };
  ws.onclose = () => {
    if (socket !== ws) return;
    socket = null;
    stopPresence();
    stopPresence = () => {};
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
