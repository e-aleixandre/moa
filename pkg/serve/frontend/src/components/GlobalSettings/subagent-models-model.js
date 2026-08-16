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
    .filter((model) => (model.catalogId === id ? checked : (allowed || []).includes(model.catalogId)))
    .map((model) => model.catalogId);
}

// allowedCount reports how many of a provider's models are allowed, for the
// "3/5" badge on the collapsed provider row — the only feedback the user gets
// about a folded group.
export function allowedCount(models, allowed) {
  const ids = new Set(allowed || []);
  return (models || []).filter((model) => ids.has(model.catalogId)).length;
}

// createAllowedModelsWriter serializes the PATCHes behind the allowlist.
//
// The list is stored as one whole policy, so two writes racing means the
// loser's payload wins on the server. Rapid toggling used to send a snapshot
// taken when the click happened, which was already stale by the time the
// request left; here every send reads the CURRENT desired list, and changes
// that arrive while a request is in flight are coalesced into the next one.
// The server's answer is only adopted when nothing newer is pending, so a
// late reply can never resurrect a state the user has already moved past.
export function createAllowedModelsWriter({ send, apply, onError }) {
  let current = [];
  let confirmed = [];
  let sending = false;
  let dirty = false;

  const flush = () => {
    if (sending || !dirty) return;
    sending = true;
    dirty = false;
    send(current)
      .then((policy) => {
        sending = false;
        confirmed = policy?.allowed_models || [];
        if (!dirty) {
          current = confirmed;
          apply?.(confirmed);
        }
        flush();
      })
      .catch((error) => {
        sending = false;
        dirty = false;
        current = confirmed;
        apply?.(confirmed);
        onError?.(error);
      });
  };

  return {
    // reset seeds the writer with what the server already has (initial load).
    reset(ids) {
      current = ids || [];
      confirmed = current;
      dirty = false;
    },
    current: () => current,
    // update computes the next list from the latest one, not from a snapshot
    // captured in a render closure. Returning null means "nothing to do".
    update(compute) {
      const next = compute(current);
      if (!next) return;
      current = next;
      apply?.(next);
      dirty = true;
      flush();
    },
  };
}
