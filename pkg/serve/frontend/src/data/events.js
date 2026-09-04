// events.js — wake-on-event: the event inbox on the client.
//
// The inbox is its own surface (a pushed screen on mobile, a second list in
// the Spine), not a group inside the session list: a session is something you
// are working in, an event is something waiting to be filed, and mixing them
// made the list mean two things at once.
//
// Events still ride the existing session poll rather than a timer of their
// own: the inbox names the sessions an event can be sent to, so refreshing the
// two apart would let them disagree about what is open.

import { api } from './api.js';
import { store, setState, visibleSessionIds } from './store.js';
import { loadSessions } from './session-actions.js';
import { openSession } from './tile-actions.js';
import { addToast, removeToast } from './notifications.js';
import { modelCodename, projectKey, projectLabel, sessionTitle } from './util/format.js';

// relAge is the session list's clock, kept identical to Spine/sessions.js and
// the mobile chrome's: an event's age must not read like a different clock.
function relAge(at) {
  const ms = eventCreatedAt(at);
  if (!ms) return '';
  const min = Math.floor((Date.now() - ms) / 60000);
  if (min < 1) return 'now';
  if (min < 60) return `${min}m`;
  const h = Math.floor(min / 60);
  if (h < 24) return `${h}h`;
  return `${Math.floor(h / 24)}d`;
}

// Event.Created is time.Time on the server, so /api/events encodes it as an
// ISO timestamp. Catalog fixtures use epoch milliseconds; accept both at the
// transport boundary instead of making their sort order depend on coercion.
function eventCreatedAt(at) {
  const ms = typeof at === 'number' ? at : Date.parse(at);
  return Number.isFinite(ms) ? ms : 0;
}

function sameEvents(prev, next) {
  if (prev === next) return true;
  if (!prev || !next || prev.length !== next.length) return false;
  return prev.every((event, i) => event.id === next[i].id
    && event.state === next[i].state
    && event.routed_to === next[i].routed_to
    && event.pending_reason === next[i].pending_reason);
}

// ── the inbox surface's open/closed state ───────────────────────────────────
// One flag for both layouts: mobile pushes a screen, the desktop swaps the
// spine's list, and a toast has to be able to open either without knowing
// which one it is talking to.
export function openInbox() { setState({ inboxOpen: true }); }
export function closeInbox() { setState({ inboxOpen: false }); }
export function toggleInbox() { setState({ inboxOpen: !store.get().inboxOpen }); }

// PENDING_REASON_COPY is why a row is still waiting. Tokens match
// events.PendingReason on the server; unknown values are hidden, not shown raw.
export const PENDING_REASON_COPY = {
  inbox: 'this source always waits in the inbox',
  no_session: 'no session open in this project',
  many_sessions: 'several sessions are open',
  session_unavailable: 'the target session is missing or unavailable',
  session_busy: 'the session is busy and autorun is off',
  rate_limited: 'this source is sending too many events',
};

export function pendingReasonLabel(reason) {
  return PENDING_REASON_COPY[reason] || '';
}

// isOpenEventCandidate matches serve.isOpenEventCandidate: live, not error,
// not waiting on a permission, not saved.
export function isOpenEventCandidate(session) {
  if (!session?.id) return false;
  switch (session.state) {
    case 'error':
    case 'permission':
    case 'saved':
      return false;
    default:
      return true;
  }
}

export function eventCreateSpec(event) {
  const spec = {};
  if (event?.create_model) spec.model = event.create_model;
  if (event?.create_thinking) spec.thinking = event.create_thinking;
  return spec;
}

export function eventCreateActionLabel(event, { specs = [], defaultModel = '' } = {}) {
  const raw = event?.create_model || defaultModel;
  const spec = (specs || []).find((item) => item.id === raw || item.catalogId === raw || item.alias === raw);
  const name = spec?.codename || modelCodename(raw) || raw;
  const thinking = event?.create_thinking || '';
  if (name && thinking) return `New session · ${name} ${thinking}`;
  if (name) return `New session · ${name}`;
  return 'New session';
}

// ── arrival announcements ───────────────────────────────────────────────────
// A poll that brings ids nobody has seen is the only moment an event is news.
// The first poll of a session is NOT news: everything in it is history the
// user is opening the app to read.
let knownIds = null;
let burst = null; // { toastId, count, at } — the 60 s coalescing window

const BURST_WINDOW_MS = 60000;

export function __resetEventAnnouncementsForTests() {
  knownIds = null;
  burst = null;
}

// announceArrivals toasts what just arrived. Delivered events point at their
// session, pending ones at the inbox. More than one inside a minute collapses
// into a single count: a burst of alerts must not become a wall of toasts.
export function announceArrivals(arrivals, { visible = [], now = Date.now() } = {}) {
  // Never toast for the conversation already on screen: the event's own block
  // is right there, and a toast would announce something already visible.
  const news = (arrivals || []).filter((event) => !(event.routed_to && visible.includes(event.routed_to)));
  if (news.length === 0) return null;

  const coalescing = burst && now - burst.at < BURST_WINDOW_MS;
  const count = (coalescing ? burst.count : 0) + news.length;
  if (coalescing) removeToast(burst.toastId);

  if (count > 1) {
    const toastId = addToast({
      title: `${count} new events`,
      detail: 'waiting in Inbox',
      type: 'info',
      onOpen: openInbox,
    });
    burst = { toastId, count, at: now };
    return toastId;
  }

  const [event] = news;
  const delivered = event.state === 'routed' && event.routed_to;
  const target = delivered ? store.get().sessions?.[event.routed_to] : null;
  const toastId = addToast({
    title: `${event.source} · ${event.title || 'event'}`,
    detail: delivered ? `→ ${target ? sessionTitle(target) : 'session'}` : 'waiting in Inbox',
    type: 'info',
    ...(delivered ? { sessionId: event.routed_to } : { onOpen: openInbox }),
  });
  burst = { toastId, count: 1, at: now };
  return toastId;
}

// loadEvents refreshes the inbox. A failure keeps the previous list: an event
// that is still waiting must not vanish because one poll failed.
export async function loadEvents() {
  try {
    const events = await api('GET', '/api/events');
    const list = Array.isArray(events) ? events : [];
    const first = knownIds === null;
    const previous = knownIds || new Set();
    knownIds = new Set(list.map((event) => event.id));
    if (!first) {
      const arrivals = list.filter((event) => !previous.has(event.id));
      if (arrivals.length > 0) {
        announceArrivals(arrivals, { visible: visibleSessionIds(store.get()) });
      }
    }
    if (sameEvents(store.get().events, list)) return;
    setState({ events: list });
  } catch (e) {
    console.error('loadEvents failed:', e);
  }
}

// settleEvent marks an event as decided locally the moment it is acted on, so
// it cannot be acted on twice while the next poll is on its way. The row stays
// in the list: the inbox keeps its history, it does not empty itself.
function settleEvent(id, patch) {
  setState({
    events: store.get().events.map((event) => (event.id === id ? { ...event, ...patch } : event)),
  });
}

function routeFailureToast(e) {
  addToast({
    title: 'Could not send event',
    detail: String(e.message || e),
    type: 'error',
  });
}

// routeEvent sends an event to a session and opens that session, which is
// where the event now lives. The row stays pending until the server agrees —
// an optimistic settle would close a decision that never happened.
export async function routeEvent(id, sessionId) {
  try {
    const event = await api('POST', `/api/events/${encodeURIComponent(id)}/route`, { session_id: sessionId });
    if (event) settleEvent(id, event);
    await loadSessions();
    const target = event?.routed_to || sessionId;
    if (target) {
      closeInbox();
      openSession(target);
    }
    return event;
  } catch (e) {
    routeFailureToast(e);
    throw e;
  }
}

// routeEventToNewSession creates the session for the event. Model and thinking
// are sent only when chosen (source snapshot or an explicit override); omitting
// them lets the server apply its default rather than inventing "medium".
export async function routeEventToNewSession(id, spec) {
  try {
    const event = await api('POST', `/api/events/${encodeURIComponent(id)}/route`, {
      new: true,
      ...(spec?.model ? { model: spec.model } : {}),
      ...(spec?.thinking ? { thinking: spec.thinking } : {}),
    });
    if (event) settleEvent(id, event);
    await loadSessions();
    if (event?.routed_to) {
      closeInbox();
      openSession(event.routed_to);
    }
    return event;
  } catch (e) {
    routeFailureToast(e);
    throw e;
  }
}

export async function dismissEvent(id) {
  settleEvent(id, { state: 'dismissed' });
  await api('POST', `/api/events/${encodeURIComponent(id)}/dismiss`);
}

// dismissSource is the bulk case: a source that fires all night is ignored in
// one gesture instead of one row at a time. Only what is still WAITING is
// touched — history is not rewritten.
export async function dismissSource(source) {
  const result = await api('POST', '/api/events/dismiss', { source });
  // The server dismisses this source atomically. Mirror every pending row now
  // rather than issuing one single-dismiss request per row, which can race a
  // fresh poll and does not use the bulk endpoint.
  setState({
    events: store.get().events.map((event) => (
      event.source === source && (event.state || 'new') === 'new'
        ? { ...event, state: 'dismissed' }
        : event
    )),
  });
  return result;
}

// inboxCards projects the inbox: the event plus the open sessions of its
// project. An event without a project can only be sent to an existing session,
// so it offers every open one. The list is what makes the choice possible, so
// it is computed here — the surface only renders it, and the two cannot
// disagree about which sessions are live.
//
// Saved sessions are not candidates: an event delivered to a session nobody is
// running would sit unread with no turn behind it.
export function inboxCards(sessions, events) {
  const all = Object.values(sessions || {});
  return (events || []).map((event) => {
    const project = projectKey(event.project);
    const targets = all
      .filter((s) => isOpenEventCandidate(s) && (!project || projectKey(s.cwd) === project))
      .sort((a, b) => (b.updated || 0) - (a.updated || 0))
      .map((s) => ({
        id: s.id,
        title: sessionTitle(s),
        state: s.state || 'idle',
        when: relAge(s.updated),
        brief: s.last || s.needsLabel,
        path: s.cwd,
        origin: s.origin,
      }));
    // A settled event names where it went; if that session is gone from the
    // roster the row falls back to a generic phrase rather than an empty name.
    const routedTo = event.routed_to ? sessions?.[event.routed_to] : null;
    return {
      event,
      age: relAge(event.created),
      pending: (event.state || 'new') === 'new',
      sessions: targets,
      project,
      projectLabel: projectLabel(event.project),
      routedToTitle: routedTo ? sessionTitle(routedTo) : '',
    };
  });
}

export function inboxPendingCount(cards) {
  return (cards || []).filter((card) => card.pending).length;
}

// inboxGroups is what the surface paints: the chosen filter, newest first,
// grouped by project ONLY when more than one project is involved — a single
// project would be a header repeating what the whole screen already is.
export function inboxGroups(cards, filter = 'pending') {
  const shown = (cards || [])
    .filter((card) => (filter === 'pending' ? card.pending : true))
    .sort((a, b) => eventCreatedAt(b.event.created) - eventCreatedAt(a.event.created));
  const projects = new Set(shown.map((card) => card.project));
  if (projects.size <= 1) return [{ key: '', label: '', cards: shown }];
  const groups = [];
  for (const card of shown) {
    let group = groups.find((g) => g.key === card.project);
    if (!group) {
      group = { key: card.project, label: card.projectLabel, cards: [] };
      groups.push(group);
    }
    group.cards.push(card);
  }
  return groups;
}

// inboxSig is the change signal the chrome selectors compare on: identity, the
// event's state, and the sessions its sheet would offer.
export function inboxSig(cards) {
  return (cards || [])
    .map((c) => [
      c.event.id,
      c.event.state || 'new',
      c.event.pending_reason || '',
      c.age || '',
      c.routedToTitle || '',
      c.sessions.map((s) => `${s.id}:${s.state}:${s.when}:${s.brief || ''}:${s.path || ''}:${s.origin || ''}`).join(','),
    ].join('\0'))
    .join('\n');
}
