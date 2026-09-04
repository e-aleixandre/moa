// Pure model for the compaction-summarizer setting. The wire shape is
// { compact_model, choices: [{ spec, name, provider }] }, where compact_model is
// either the "session" keyword or a model spec.
//
// Kept apart from the component so the rules that matter — what counts as
// custom, what to select when switching modes, what to send — can be tested
// without a DOM.

import { modelAccent } from "../../data/selectors.js";
import { modelCodename } from "../../data/util/format.js";

export const SESSION = "session";

// isCustom reports whether a spec names a specific model rather than "whatever
// the session is using".
export function isCustom(spec) {
  const value = String(spec || "").trim();
  return value !== "" && value.toLowerCase() !== SESSION;
}

// normalizeChoices keeps only usable entries. The server already filters by
// credential; this guards against a malformed payload reaching the selector,
// where a blank option would look like a real choice.
export function normalizeChoices(choices) {
  if (!Array.isArray(choices)) return [];
  const seen = new Set();
  const out = [];
  for (const c of choices) {
    const spec = String(c?.spec || "").trim();
    if (!spec || seen.has(spec)) continue;
    seen.add(spec);
    out.push({
      spec,
      name: String(c?.name || spec).trim() || spec,
      provider: String(c?.provider || "").trim(),
    });
  }
  return out;
}

export function selectorModels(choices) {
  return normalizeChoices(choices).map((choice) => {
    const codename = modelCodename(choice.name) || choice.name;
    return { id: choice.spec, catalogId: choice.spec, name: choice.name,
      provider: choice.provider || "Other", codename,
      sub: codename === choice.name ? "" : choice.name.replace(codename, "").trim(),
      accent: modelAccent(choice.name), alias: choice.spec };
  });
}

// specForMode decides what to send when the user flips between "session model"
// and "custom".
//
// Switching to custom needs a concrete model: the current one when it is still
// offered, otherwise the first available. It returns null when there is nothing
// to pick, so the caller can leave the setting alone instead of sending a
// request that would be rejected.
export function specForMode(mode, currentSpec, choices) {
  if (mode !== "custom") return SESSION;
  const available = normalizeChoices(choices);
  if (available.length === 0) return null;
  if (isCustom(currentSpec) && available.some((c) => c.spec === currentSpec)) {
    return currentSpec;
  }
  return available[0].spec;
}

// summaryLabel is the one-line description of what will happen on the next
// compaction. The ordinary case names no model on purpose: the session's model
// is whatever the session is using, and naming it here would go stale the
// moment the session's model changes.
export function summaryLabel(spec, choices) {
  if (!isCustom(spec)) return "Summarize with the session's own model.";
  const match = normalizeChoices(choices).find((c) => c.spec === spec);
  const name = match?.name || spec;
  return `Summarize with ${name}, whatever model the session uses.`;
}
