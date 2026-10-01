// owner-closed.js — the user opening an owner brings it back from the closed
// group. Only explicit openings call this (a click, the palette, a link, a
// toast); auto-selection and the server's own wake-ups do not, or closing an
// owner whose child is still working would undo itself on the next landing.

import { api } from './api.js';
import { store, setState } from './store.js';

export function reopenClosedOwnerOf(sessionId) {
  const state = store.get();
  const own = (state.owners?.list || []).find((o) => o.session_id === sessionId && o.closed);
  if (!own) return;
  setState((s) => ({
    owners: {
      ...s.owners,
      list: (s.owners?.list || []).map((o) => (o.id === own.id ? { ...o, closed: false } : o)),
    },
  }));
  // A failed write puts the server's truth back on the next owners load.
  api('PATCH', `/api/owners/${own.id}`, { closed: false }).catch(() => {});
}
