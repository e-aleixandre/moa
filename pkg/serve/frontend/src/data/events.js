// events.js — wake-on-event: the pending-event inbox on the client.
//
// Events ride the existing session poll rather than a timer of their own: the
// inbox is shown inside the session list, so refreshing them apart from it
// would let the two disagree about what is waiting.

import { api } from './api.js';
import { store, setState } from './store.js';
import { loadSessions } from './session-actions.js';
import { setActiveSession } from './tile-actions.js';
import { sessionTitle } from './util/format.js';

function sameEvents(prev, next) {
  if (prev === next) return true;
  if (!prev || !next || prev.length !== next.length) return false;
  return prev.every((event, i) => event.id === next[i].id && event.suggested === next[i].suggested);
}

// loadEvents refreshes the inbox. A failure keeps the previous list: an event
// that is still waiting must not vanish from the drawer because one poll
// failed.
export async function loadEvents() {
  try {
    const events = await api('GET', '/api/events');
    const list = Array.isArray(events) ? events : [];
    if (sameEvents(store.get().events, list)) return;
    setState({ events: list });
  } catch (e) {
    console.error('loadEvents failed:', e);
  }
}

// removeEvent drops a settled event from the local inbox immediately, so the
// card cannot be tapped twice while the next poll is still on its way.
function removeEvent(id) {
  setState({ events: store.get().events.filter((event) => event.id !== id) });
}

// routeEvent sends an event to a session — the suggested one, or a session
// created for it — and opens that session, which is where the event now lives.
export async function routeEvent(id, sessionId) {
  const event = await api('POST', `/api/events/${encodeURIComponent(id)}/route`, { session_id: sessionId });
  removeEvent(id);
  await loadSessions();
  if (event?.routed_to) setActiveSession(event.routed_to);
  return event;
}

export async function routeEventToNewSession(id) {
  const event = await api('POST', `/api/events/${encodeURIComponent(id)}/route`, { new: true });
  removeEvent(id);
  await loadSessions();
  if (event?.routed_to) setActiveSession(event.routed_to);
  return event;
}

export async function dismissEvent(id) {
  await api('POST', `/api/events/${encodeURIComponent(id)}/dismiss`);
  removeEvent(id);
}

// inboxCards projects pending events for the session list: the event plus the
// title of the session it would go to, which is what the primary action names.
// A suggestion whose session has since disappeared from the roster is dropped,
// so the card offers "New session" instead of naming something that is gone.
export function inboxCards(sessions, events) {
  return (events || []).map((event) => {
    const target = event.suggested ? sessions?.[event.suggested] : null;
    return {
      event: target || !event.suggested ? event : { ...event, suggested: '' },
      suggestedTitle: target ? sessionTitle(target) : '',
    };
  });
}

// inboxSig is the change signal the chrome selectors compare on: identity plus
// the suggestion, which is all a card renders from the roster.
export function inboxSig(cards) {
  return (cards || []).map((c) => `${c.event.id}\0${c.event.suggested || ''}\0${c.suggestedTitle}`).join('\n');
}
