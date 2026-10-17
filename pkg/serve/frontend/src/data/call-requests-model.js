// call-requests-model.js — what the live call requests (an owner asking to
// talk) mean on screen. Pure: the slice is fed by call-requests.js.

export function liveRequests(requests) {
  return Array.isArray(requests) ? requests : [];
}

// A ringing request past expires_at has ended whether or not the feed has
// said so yet. Held and accepted requests carry no such limit.
export function ringExpired(request, now = Date.now()) {
  if (request?.state !== 'ringing') return false;
  const at = Date.parse(request.expires_at || '');
  return Number.isFinite(at) && at <= now;
}

// msUntilExpiry: how long until the oldest live ringing request lapses; null
// when none can.
export function msUntilExpiry(request, now = Date.now()) {
  if (request?.state !== 'ringing') return null;
  const at = Date.parse(request.expires_at || '');
  return Number.isFinite(at) ? Math.max(0, at - now) : null;
}

// incoming is the request to show as ringing: the oldest still ringing.
export function incomingRequest(requests, now = Date.now()) {
  return liveRequests(requests).find((r) => r.state === 'ringing' && !ringExpired(r, now)) || null;
}

// heldRequests wait behind the call in progress.
export function heldRequests(requests) {
  return liveRequests(requests).filter((r) => r.state === 'held');
}

export function requestOfSession(requests, sessionId) {
  return liveRequests(requests).find((r) => r.session_id === sessionId && r.state !== 'accepted' && r.state !== 'connecting') || null;
}

// wantsToTalk: owner ids with a request still asking for you.
export function wantsToTalkIds(requests) {
  return new Set(liveRequests(requests).filter((r) => r.state === 'ringing' || r.state === 'held').map((r) => r.owner_id));
}

export function incomingVerb(request) {
  return request?.level === 'ring' ? 'is calling' : 'wants to talk';
}

export function incomingTitle(request) {
  return `${request?.owner_name || 'An owner'} ${incomingVerb(request)}`;
}

export function heldTitle(request) {
  return `${request?.owner_name || 'An owner'} wanted to talk`;
}

// requestFromLocation reads the notification's deep link.
export function requestIdFromSearch(search) {
  try { return new URLSearchParams(search || '').get('call_request') || ''; } catch { return ''; }
}

// callBlockModel is the transcript's compact mark for a request_call result.
const WHY = {
  missed: 'no answer',
  declined: 'written instead',
  plan_failed: 'could not connect',
  never: 'calls are off for this owner',
  outside_hours: 'outside your allowed hours',
  paused: 'calls are paused',
  no_plan: 'no voice plan',
  cancelled_by_user: 'cancelled',
  busy: 'you were on another call',
};

export function callBlockModel(custom) {
  const c = custom?.call_request;
  if (!c || typeof c !== 'object') return null;
  const reason = typeof c.reason === 'string' ? c.reason : '';
  if (c.outcome === 'answered') return { answered: true, title: 'Call · answered', reason };
  const why = WHY[c.why] || '';
  return { answered: false, title: why ? `Missed call · ${why}` : 'Missed call', reason };
}
