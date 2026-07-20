// selectors.js — pure derivations the UI containers read from the store state.
// These bridge the tile-tree session model (shared with the old SPA, reused
// verbatim — see the 5C decision) and the single-session conversation screen of
// frontend-next.

import { findTile } from './tileTree.js';
import { shortModel, modelCodename, contextWindowLabel } from './util/format.js';

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

// deriveModelSpecs maps /api/models entries ({id, name, provider, alias?,
// max_input?}) into the shape the model grid expects: {id, name, provider,
// codename, sub, accent}. `id` here is the full "provider/id" spec
// configureSession sends over the wire (matches the old SettingsDropdown's
// `m.provider + '/' + m.id`). `codename` is the one-word vocabulary the rest
// of the UI already uses (modelCodename — Opus/Sonnet/Sol/Terra…); models
// without a known codename (e.g. "GPT-5.5", "GPT-5.3 Codex") fall back to
// their full display name so the chip still shows something meaningful ("no
// codename" case from MODEL-SELECTOR-ALT-SPEC-FABLE §1b). `sub` is the rest of
// the name (vendor word + codename stripped) plus the context window, e.g.
// "4.8 · 1M ctx" for "Claude Opus 4.8", or just the context when the codename
// swallowed the whole name. Shared by the desktop ChatHead popover and the
// mobile model sheet.
export function deriveModelSpecs(models) {
  return (models || []).map((m) => {
    const codename = modelCodename(m.name) || m.name || m.id;
    const usedKnownCodename = codename !== (m.name || m.id);
    const rest = usedKnownCodename ? stripWords(m.name, [codename, 'Claude']) : '';
    const ctx = contextWindowLabel(m.max_input);
    const sub = [rest, ctx].filter(Boolean).join(' \u00b7 ');
    return {
      id: `${m.provider}/${m.id}`,
      name: m.name,
      provider: m.provider,
      codename,
      sub,
      accent: modelAccent(m.name),
    };
  });
}

// stripWords removes each given word (whole-word, case-insensitive) from
// `text` and collapses the leftover whitespace. Used to turn a full display
// name ("Claude Opus 4.8") into the residual version string ("4.8") once the
// codename ("Opus") and vendor noise ("Claude") are pulled out into their own
// slots of the model chip.
function stripWords(text, words) {
  let out = text || '';
  for (const w of words) {
    if (!w) continue;
    out = out.replace(new RegExp(`\\b${escapeRegExp(w)}\\b`, 'ig'), '');
  }
  return out.replace(/\s+/g, ' ').trim();
}

function escapeRegExp(s) {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

// THINKING_CYCLE — the order the desktop meter-click shortcut steps through
// (MODEL-SELECTOR-ALT-SPEC-FABLE §2/§6.2): off→low→medium→high→xhigh→off.
// Same vocabulary as ThinkingMeter/Segmented elsewhere.
const THINKING_CYCLE = ['off', 'low', 'medium', 'high', 'xhigh'];

// nextThinkingLevel returns the next level in THINKING_CYCLE after `level`,
// wrapping back to "off" after "xhigh". Unknown/missing levels start the
// cycle at "low" (one step past "off"), so a stray click always moves it.
export function nextThinkingLevel(level) {
  const idx = THINKING_CYCLE.indexOf(level);
  const from = idx === -1 ? 0 : idx;
  return THINKING_CYCLE[(from + 1) % THINKING_CYCLE.length];
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
