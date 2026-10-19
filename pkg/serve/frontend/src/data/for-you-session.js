// for-you-session.js — For you narrowed to ONE conversation: what this
// session waits on the user for and what it told him. The general For you
// (sidebar, drawer) keeps everything; this is the header's own view.
//
// An owner is the project's standing agent, so its view also covers the
// sessions that report to it: a child's permission is the owner's business.

import { forYouSlice } from './for-you.js';

// forYouScopeIds — the session ids a conversation answers for: itself and,
// when it is an owner's conversation, every session whose ownerId is that
// owner. The owner is found in the owners list by its conversation
// (`session_id`): an owner conversation carries no ownerId of its own.
export function forYouScopeIds(sessionId, sessions = {}, owners = []) {
  if (!sessionId) return new Set();
  const ids = new Set([sessionId]);
  const ownerId = owners.find((o) => o.session_id === sessionId)?.id;
  if (!ownerId) return ids;
  for (const s of Object.values(sessions)) {
    if (s && (s.ownerId || s.owner_id) === ownerId) ids.add(s.id);
  }
  return ids;
}

// forYouForSession — the header button's numbers and the sheet's lists. The
// news keep the read ones (the server keeps them 7 days): the sheet is also
// where a read News is found again.
export function forYouForSession(state, sessionId, owners = []) {
  const slice = forYouSlice(state);
  const ids = forYouScopeIds(sessionId, state?.sessions, owners);
  const items = slice.items.filter((it) => ids.has(it.session_id));
  const news = slice.news.filter((n) => ids.has(n.session_id));
  return { ids, items, news, waiting: items.length, unread: news.filter((n) => !n.read_at).length };
}

// sessionForYouLabel — what the header button says, in words, for the three
// states (waiting, unread News, nothing).
export function sessionForYouLabel(waiting = 0, unread = 0) {
  return `For you in this conversation${waiting > 0 ? `, ${waiting} waiting` : ''}${unread > 0 ? `, ${unread} new` : ''}`;
}
