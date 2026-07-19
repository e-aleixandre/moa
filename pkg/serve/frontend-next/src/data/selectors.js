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

// deriveModelSpecs maps /api/models entries ({id, name, provider, alias?}) into
// the shape ModelSelector expects ({id, name, desc, sigil, accent}). `id` here
// is the full "provider/id" spec configureSession sends over the wire (matches
// the old SettingsDropdown's `m.provider + '/' + m.id`). Shared by the desktop
// ChatHead popover and the mobile model sheet.
export function deriveModelSpecs(models) {
  return (models || []).map((m) => ({
    id: `${m.provider}/${m.id}`,
    name: m.name,
    desc: m.alias || m.provider,
    sigil: (m.name || m.id || '?').charAt(0).toUpperCase(),
    accent: modelAccent(m.name),
  }));
}

// matchSelectedModel finds the spec whose display name matches the session's
// current model string (session.model is the display name the backend reports,
// e.g. "GPT-5.6 Sol" — not the "provider/id" spec).
export function matchSelectedModel(specs, sessionModel) {
  if (!sessionModel) return undefined;
  const short = shortModel(sessionModel);
  const found = specs.find((s) => s.name === sessionModel || s.name === short);
  return found?.id;
}
