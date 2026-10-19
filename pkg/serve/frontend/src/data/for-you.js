// for-you.js — "For you": everything any session is waiting on the user for,
// read from GET /api/for-you (open asynchronous questions from the question
// store, plus the blocking question, permission and password requests of the
// loaded sessions). Refreshed with the roster, so it rides the same poll and
// the same foreground/online refreshes; answering goes through the session's
// own endpoints, never through here.

import { api } from './api.js';
import { setState, store } from './store.js';
import { navigate } from './router.js';

const EMPTY = { items: [], loaded: false };

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
        if (prev.loaded && JSON.stringify(prev.items) === JSON.stringify(items)) return null;
        return { forYou: { items, loaded: true } };
      });
    })
    .catch(() => {})
    .finally(() => { inflight = null; });
  return inflight;
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
