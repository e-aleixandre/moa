// selectors.js — pure derivations the UI containers read from the store state.
// These bridge the tile-tree session model (inherited from the SPA this
// replaced, reused verbatim) and the single-session conversation screen.

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
// max_input?, reasoning_efforts?}) into the shape the model selector expects: {id, catalogId,
// name, provider, codename, sub, accent, alias, reasoningEfforts}. `id` here is the full "provider/id" spec
// configureSession sends over the wire (matches the old SettingsDropdown's
// `m.provider + '/' + m.id`). `codename` is the one-word vocabulary the rest
// of the UI already uses (modelCodename — Opus/Sonnet/Sol/Terra…); models
// without a known codename (e.g. "GPT-5.5", "GPT-5.3 Codex") fall back to
// their full display name so the chip still shows something meaningful ("no
// codename" case from MODEL-SELECTOR-ALT-SPEC-FABLE §1b). `sub` is the rest of
// the name (vendor word + codename stripped) plus the context window, e.g.
// "4.8 · 1M ctx" for "Claude Opus 4.8", or just the context when the codename
// swallowed the whole name. `alias` is the backend's shortest CLI alias
// ("sol", "codex"…), kept so the selector's filter matches it. Shared by the
// desktop ChatHead popover and the mobile model sheet.
export function deriveModelSpecs(models) {
  return (models || []).map((m) => {
    const codename = modelCodename(m.name) || m.name || m.id;
    const usedKnownCodename = codename !== (m.name || m.id);
    const rest = usedKnownCodename ? stripWords(m.name, [codename, 'Claude']) : '';
    const ctx = contextWindowLabel(m.max_input);
    const sub = [rest, ctx].filter(Boolean).join(' \u00b7 ');
    return {
      id: `${m.provider}/${m.id}`,
      catalogId: m.id,
      name: m.name,
      provider: m.provider,
      codename,
      sub,
      accent: modelAccent(m.name),
      alias: m.alias || '',
      reasoningEfforts: m.reasoning_efforts || [],
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

// thinkingOptionsFor pairs the stable persisted selector positions with the
// effective effort labels supplied by the backend. Most models have no custom
// efforts, so their labels remain the positions themselves.
export function thinkingOptionsFor(spec, sessionProvider) {
  const levels = thinkingLevelsFor(spec, sessionProvider) || THINKING_CYCLE;
  const efforts = spec?.reasoningEfforts;
  return levels.map((value, index) => ({
    value,
    label: efforts?.[index] || value,
    bars: THINKING_CYCLE.indexOf(value),
  }));
}

// thinkingPositionFor converts an effective backend effort back to the stable
// selector position. This keeps the selection meaningful when a model labels
// its five positions differently (such as Astra's low through max).
export function thinkingPositionFor(level, spec, sessionProvider) {
  const options = thinkingOptionsFor(spec, sessionProvider);
  return options.find((option) => option.label === level)?.value
    || clampThinkingLevel(level, options.map((option) => option.value));
}

// nextThinkingLevel returns the next level in THINKING_CYCLE after `level`,
// wrapping back to "off" after "xhigh". Unknown/missing levels start the
// cycle at "low" (one step past "off"), so a stray click always moves it.
export function nextThinkingLevel(level, levels = THINKING_CYCLE) {
  const cycle = levels.length ? levels : THINKING_CYCLE;
  const idx = cycle.indexOf(level);
  const from = idx === -1 ? 0 : idx;
  return cycle[(from + 1) % cycle.length];
}

// thinkingLevelsFor is the selector's option list for the current model.
// Undefined means the full THINKING_CYCLE. xAI cannot persist off/xhigh;
// Meta cannot disable reasoning at all, so off is not offered either;
// Fable 5.1 thinks on every turn so off is not a real setting.
export function thinkingLevelsFor(spec, sessionProvider) {
  const provider = spec?.provider || sessionProvider;
  if (provider === "xai") return ["low", "medium", "high"];
  if (provider === "meta") return ["low", "medium", "high", "xhigh"];
  const id = String(spec?.catalogId || spec?.id || "").toLowerCase();
  if (id.includes("fable-5-1") || id.includes("fable-5.1")) {
    return ["low", "medium", "high", "xhigh"];
  }
  return undefined;
}

export function clampThinkingLevel(level, levels) {
  if (!levels || levels.includes(level)) return level || "off";
  return levels.includes("high") ? "high" : levels[0];
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
