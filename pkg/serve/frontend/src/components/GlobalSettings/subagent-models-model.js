// Pure list logic behind the subagent model allowlist, kept out of the
// component so the "empty means everything" rule can be tested directly —
// getting it backwards would silently forbid every delegation.

// scopeForAllowed maps a stored policy onto the segmented control: an empty
// (or missing) list is the unrestricted default, not an empty selection.
export function scopeForAllowed(allowed) {
  return allowed && allowed.length ? "selected" : "all";
}

// nextAllowedModels returns the list to persist after toggling one model,
// rebuilt in catalog order so the saved policy reads like the list on screen
// rather than in click order.
export function nextAllowedModels(models, allowed, id, checked) {
  return (models || [])
    .filter((model) => (model.id === id ? checked : (allowed || []).includes(model.id)))
    .map((model) => model.id);
}
