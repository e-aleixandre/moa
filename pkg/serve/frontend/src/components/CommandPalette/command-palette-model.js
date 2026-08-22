// command-palette-model.js — pure helpers for the palette's create/model steps.
//
// Kept out of CommandPalette.jsx for the same reason model-selector-model.js is
// kept out of ModelSelector.jsx: the list building and the step-back rules are
// the parts worth testing, and they don't need the component's hooks.

import {
  groupByProvider,
  pinnedModelSpecs,
  specMatches,
} from "../ModelSelector/model-selector-model.js";

// defaultModelSpec — the model a create with no explicit choice uses: the
// server's default when it advertises one, else the first catalogued model so
// the row never renders empty.
export function defaultModelSpec(caps, specs) {
  return caps?.defaultModel || specs?.[0]?.id || "";
}

// modelStepItems builds the flat, palette-shaped item list of the model step:
// group headers plus { kind: "model" } rows, in the same reading order the
// ModelSelector already established (pinned first, then providers), and the
// same filter (codename / display name / alias / provider) when there's a
// query. Rows carry `spec` so activation just reads spec.id — the very string
// createSession already receives.
export function modelStepItems(specs, pinnedIDs, query) {
  const list = specs || [];
  const q = (query || "").trim().toLowerCase();
  const out = [];
  if (q) {
    const hits = list.filter((spec) => specMatches(spec, q));
    if (!hits.length) {
      out.push({ kind: "note", text: `No models match “${query.trim()}”` });
      return out;
    }
    out.push({ kind: "group", label: `Results · ${hits.length}` });
    for (const spec of hits) out.push({ kind: "model", spec });
    return out;
  }
  const pinned = pinnedModelSpecs(list, pinnedIDs);
  if (pinned.length) {
    out.push({ kind: "group", label: "Pinned" });
    for (const spec of pinned) out.push({ kind: "model", spec });
  }
  for (const group of groupByProvider(list)) {
    out.push({ kind: "group", label: group.provider || "models" });
    for (const spec of group.items) out.push({ kind: "model", spec });
  }
  return out;
}

// stepBack answers "where does a back gesture land from here?" for both the
// ⌫-on-empty-query and Escape keys. The model step is a sub-step of create, so
// it always steps back into it; create only returns to search when search is
// where the palette came from (opened straight into create, there's no previous
// screen to fall into).
export function stepBack(step, initialStep) {
  if (step === "model") return "create";
  if (step === "create" && initialStep !== "create") return "search";
  return "close";
}
