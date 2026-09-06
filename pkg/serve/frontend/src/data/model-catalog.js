// model-catalog.js — the shared /api/models resource. One request for the
// whole app instead of a private fetch per status strip: the catalog is what
// turns a backend effort label ("low" on Astra) into the stable selector
// position the meter draws, so every strip must read the same answer.
//
// The slice is deliberately NOT persisted: it is server state, cheap to
// refetch, and a stale catalog would mislabel a thinking meter.

import { store, setState } from './store.js';
import { api } from './api.js';
import { deriveModelSpecs, matchSelectedModel, thinkingPositionFor } from './selectors.js';

// idle → loading → ready | error. `entries` holds derived specs, and only a
// successful response fills it: an error keeps the catalog unknown rather than
// pretending the backend has no models.
export const MODEL_CATALOG_IDLE = Object.freeze({ status: 'idle', entries: null });

export function modelCatalog(state) {
  return state?.modelCatalog || MODEL_CATALOG_IDLE;
}

// One promise for concurrent callers (bootstrap + every consumer that opens a
// selector), so five strips mounting at once still make one request.
let inFlight = null;

export function loadModelCatalog() {
  const slice = modelCatalog(store.get());
  if (slice.status === 'ready') return Promise.resolve(slice.entries);
  if (inFlight) return inFlight;
  setState({ modelCatalog: { status: 'loading', entries: null } });
  inFlight = api('GET', '/api/models')
    .then((models) => {
      const entries = deriveModelSpecs(models);
      setState({ modelCatalog: { status: 'ready', entries } });
      return entries;
    })
    .catch(() => {
      // An error is recoverable: leave the catalog unknown so the next open,
      // reconnect or foreground can ask again.
      setState({ modelCatalog: { status: 'error', entries: null } });
      return null;
    })
    .finally(() => { inFlight = null; });
  return inFlight;
}

// ensureModelCatalog is the "on demand" entry: opening a selector with an idle
// or failed catalog retries, a ready one costs nothing.
export function ensureModelCatalog() {
  const slice = modelCatalog(store.get());
  if (slice.status === 'idle' || slice.status === 'error') loadModelCatalog();
}

// catalogSpec resolves a spec from whatever identity the caller holds: a
// session carries the display name, a subagent carries the raw model it was
// spawned with ("gpt-6-astra"). Returns undefined when the catalog has no
// matching entry — never a guess.
export function catalogSpec(specs, model) {
  if (!model || !specs) return undefined;
  const byDisplayName = matchSelectedModel(specs, model);
  if (byDisplayName) return specs.find((spec) => spec.id === byDisplayName);
  const raw = String(model).toLowerCase();
  return specs.find((spec) => String(spec.id).toLowerCase() === raw
    || String(spec.catalogId).toLowerCase() === raw
    || String(spec.alias || '').toLowerCase() === raw);
}

// catalogThinkingPosition maps an effective effort to its selector position, or
// null while the catalog cannot answer. null is the meter's "unknown": drawing
// the effort as if it were a position is exactly the bug this replaces (Astra's
// "low" is position zero, not one bar).
export function catalogThinkingPosition(slice, { model, provider, thinking }) {
  if (!slice || slice.status !== 'ready') return null;
  const spec = catalogSpec(slice.entries, model);
  return spec ? thinkingPositionFor(thinking, spec, provider) : null;
}
