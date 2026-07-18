// selectors.js — pure derivations the UI containers read from the store state.
// These bridge the tile-tree session model (shared with the old SPA, reused
// verbatim — see the 5C decision) and the single-session conversation screen of
// frontend-next.

import { findTile } from './tileTree.js';
import { shortModel } from './util/format.js';

// focusedSessionId returns the sessionId assigned to the focused tile (desktop)
// or the active session (mobile), or null when nothing is shown. The
// conversation screen is single-session, so it renders exactly this session.
export function focusedSessionId(state) {
  if (!state) return null;
  if (state.isMobile) return state.activeSession || null;
  const tile = findTile(state.tileTree, state.focusedTile);
  return tile ? tile.sessionId || null : null;
}

// focusedSession returns the focused session object, or null.
export function focusedSession(state) {
  const id = focusedSessionId(state);
  return id ? state.sessions[id] || null : null;
}

// MODEL_ACCENT tints a model name in the ChatHead pill. Mirrors the accents the
// catalog established (sol=lavender, fable=peach, terra=teal); unknown models
// fall back to a neutral overlay so a new model never renders uncolored/broken.
const MODEL_ACCENT = {
  sol: 'lavender',
  fable: 'peach',
  terra: 'teal',
  haiku: 'overlay1',
};

export function modelAccent(model) {
  const short = shortModel(model || '').toLowerCase();
  for (const key of Object.keys(MODEL_ACCENT)) {
    if (short.includes(key)) return MODEL_ACCENT[key];
  }
  return 'lavender';
}
