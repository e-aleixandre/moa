// for-you.js — "For you": everything any session is waiting on the user for,
// read from GET /api/for-you (open asynchronous questions from the question
// store, plus the blocking question, permission and password requests of the
// loaded sessions). Refreshed with the roster, so it rides the same poll and
// the same foreground/online refreshes; answering goes through the session's
// own endpoints, never through here.

import { api } from './api.js';
import { setState, store } from './store.js';
import { navigate } from './router.js';

const EMPTY = { items: [], news: [], loaded: false };

export function forYouSlice(state) {
  return state?.forYou || EMPTY;
}

let inflight = null;
export function loadForYou() {
  if (inflight) return inflight;
  inflight = api('GET', '/api/for-you')
    .then((res) => {
      const items = Array.isArray(res?.items) ? res.items : [];
      setState((s) => {
        const prev = forYouSlice(s);
        // No news field: the server could not read them; keep what is shown.
        const news = Array.isArray(res?.news) ? res.news : prev.news;
        if (prev.loaded && JSON.stringify(prev.items) === JSON.stringify(items)
          && JSON.stringify(prev.news) === JSON.stringify(news)) return null;
        return { forYou: { items, news, loaded: true } };
      });
    })
    .catch(() => {})
    .finally(() => { inflight = null; });
  return inflight;
}

// __resetForYouForTests forgets a load another test left in flight.
export function __resetForYouForTests() {
  inflight = null;
}

// forgetForYouItem drops an item the user just answered, so the row leaves at
// once instead of at the next poll; the refresh that follows confirms it.
export function forgetForYouItem(id) {
  setState((s) => {
    const prev = forYouSlice(s);
    const items = prev.items.filter((it) => it.id !== id);
    return items.length === prev.items.length ? null : { forYou: { ...prev, items } };
  });
  loadForYou();
}

// unreadNewsCount — what sessions told the user that he has not read.
export function unreadNewsCount(state) {
  return forYouSlice(state).news.filter((n) => !n.read_at).length;
}

// markNewsRead archives the given news items on the server (read is
// archived), each at the revision this client showed: an update published
// meanwhile stays unread. "Mark all read" passes every unread item shown.
// The local copy flips at once so the dot goes with the tap.
export function markNewsRead(shown) {
  const acks = (shown || []).filter((n) => n?.id).map((n) => ({ id: n.id, revision: n.revision }));
  if (!acks.length) return Promise.resolve();
  const now = new Date().toISOString();
  const key = (n) => `${n.id}@${n.revision}`;
  const want = new Set(acks.map(key));
  setState((s) => {
    const prev = forYouSlice(s);
    const news = prev.news.map((n) => (!n.read_at && want.has(key(n)) ? { ...n, read_at: now } : n));
    return { forYou: { ...prev, news } };
  });
  return api('POST', '/api/for-you/news/read', { items: acks }).catch(() => {}).finally(() => loadForYou());
}

export function forYouCount(state) {
  return forYouSlice(state).items.length;
}

let from = null;
export function openForYouView() {
  const view = store.get().view;
  if (view === 'foryou') return;
  from = view;
  loadForYou();
  navigate('foryou');
}

export function closeForYouView() {
  if (store.get().view !== 'foryou') return;
  const back = from === 'foryou' ? null : from;
  from = null;
  navigate(back);
}

export function toggleForYouView() {
  if (store.get().view === 'foryou') closeForYouView();
  else openForYouView();
}

// questionCall says where an open question's call escalation is, for the pin
// and the For you row: { state: 'pending'|'calling'|'missed', text, tone }
// or null. pending counts down by the minute to call_at; a live call request
// about this question (the requests feed) is "calling" at once, before the
// next refresh says so.
export function questionCall(item, now = Date.now(), requests = []) {
  if (!item?.id) return null;
  if ((requests || []).some((r) => r?.question_id === item.id)) {
    return { state: 'calling', text: 'Calling you…', tone: 'amber' };
  }
  if (item.call_state === 'calling') return { state: 'calling', text: 'Calling you…', tone: 'amber' };
  if (item.call_state === 'missed') return { state: 'missed', text: 'Missed call', tone: 'red' };
  if (item.call_state !== 'pending') return null;
  const at = Date.parse(item.call_at || '');
  if (!Number.isFinite(at)) return null;
  const mins = Math.max(1, Math.ceil((at - now) / 60000));
  if (mins <= 60) return { state: 'pending', text: `Calls you in ${mins} min`, tone: 'amber' };
  const when = new Date(at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  return { state: 'pending', text: `Calls you at ${when}`, tone: 'amber' };
}

// canStopQuestionCall: an idle session's pending or ringing question call can
// be stopped on its own: Stop, with no run to abort. A running session has
// Stop in its live bar, which stops the call too.
export function canStopQuestionCall(item, session) {
  if (!item?.session_id || (item.call_state !== 'pending' && item.call_state !== 'calling')) return false;
  return session?.state !== 'running' && session?.state !== 'permission';
}

// stopQuestionCall is that Stop: the call is cancelled, the question stays.
export function stopQuestionCall(sessionId) {
  return api('POST', `/api/sessions/${sessionId}/cancel`).finally(() => loadForYou());
}

// callInfoFor merges what For you knows (fresher: refreshed on every call
// change) over the session's own pending ask.
export function callInfoFor(state, ask) {
  if (!ask?.id) return ask;
  const it = forYouSlice(state).items.find((i) => i.id === ask.id);
  if (!it) return ask;
  return { ...ask, call_at: it.call_at, call_state: it.call_state };
}
