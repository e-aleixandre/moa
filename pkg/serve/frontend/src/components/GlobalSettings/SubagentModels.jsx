import { useEffect, useRef, useState } from "preact/hooks";
import { api } from "../../data/api.js";
import { addToast } from "../../data/notifications.js";
import { Segmented } from "../Segmented/Segmented.jsx";
import { nextAllowedModels, scopeForAllowed } from "./subagent-models-model.js";

const SCOPE_OPTIONS = [
  { value: "all", label: "All models" },
  { value: "selected", label: "Selected only" },
];

// SubagentModels — global policy for which models the agent may delegate to.
//
// The stored list IS the whole policy: an empty list means "no restriction",
// so the control has no third state. Switching to "Selected only" therefore
// starts from every model checked (same effect, now explicit) and the user
// narrows down from there; the last checked model cannot be unchecked, because
// an empty list would silently mean the opposite of what the UI shows.
//
// The catalog is fetched here instead of being threaded through the three
// GlobalSettings call sites: the sheet mounts on demand, so this costs one
// request the moment the user actually opens Settings.
export function SubagentModels() {
  const [models, setModels] = useState([]);
  const [allowed, setAllowed] = useState([]);
  const [scope, setScope] = useState("all");
  const [loaded, setLoaded] = useState(false);
  // Mirrors ModelSelector's pinning: keep the last confirmed value to restore
  // on failure, serialize writes, and ignore answers to superseded requests.
  const allowedRef = useRef([]);
  const revisionRef = useRef(0);
  const queueRef = useRef(Promise.resolve());

  const applyAllowed = (ids) => {
    allowedRef.current = ids;
    setAllowed(ids);
  };

  useEffect(() => {
    let live = true;
    Promise.all([
      api("GET", "/api/models").catch(() => []),
      api("GET", "/api/subagent-models").catch(() => null),
    ]).then(([list, policy]) => {
      if (!live) return;
      setModels(list || []);
      const ids = policy?.allowed_models || [];
      applyAllowed(ids);
      setScope(scopeForAllowed(ids));
      setLoaded(true);
    });
    return () => { live = false; };
  }, []);

  const save = (ids) => {
    const before = allowedRef.current;
    const revision = ++revisionRef.current;
    applyAllowed(ids);

    const request = queueRef.current
      .catch(() => {})
      .then(() => api("PATCH", "/api/subagent-models", { allowed_models: ids }));
    queueRef.current = request;
    request
      .then((policy) => {
        if (revision === revisionRef.current) applyAllowed(policy?.allowed_models || ids);
      })
      .catch((error) => {
        if (revision !== revisionRef.current) return;
        applyAllowed(before);
        setScope(scopeForAllowed(before));
        addToast({ title: "Could not update subagent models", detail: error.message, type: "error" });
      });
  };

  const onScope = (next) => {
    if (next === scope) return;
    setScope(next);
    save(next === "all" ? [] : models.map((model) => model.id));
  };

  // Rebuilt from the catalog order so the saved list reads the same way it is
  // shown, instead of in click order.
  const toggle = (id, checked) => {
    const ids = nextAllowedModels(models, allowed, id, checked);
    if (!ids.length) return; // the last remaining checkbox is disabled; belt and braces
    save(ids);
  };

  const limited = scope === "selected";
  const lastRemaining = limited && allowed.length === 1;

  return (
    <div class="subagent-models">
      <p class="subagent-models-hint" id="subagent-models-hint">
        Models the agent may delegate to. Anything else is refused.
      </p>
      <Segmented
        options={SCOPE_OPTIONS}
        value={scope}
        onChange={onScope}
        disabled={!loaded}
        aria-label="Subagent models"
        aria-describedby="subagent-models-hint"
      />
      {limited && (
        <div class="subagent-models-list" role="group" aria-label="Allowed subagent models">
          {models.map((model) => {
            const checked = allowed.includes(model.id);
            return (
              <label key={model.id} class={`subagent-models-row${checked ? " on" : ""}`}>
                <input
                  type="checkbox"
                  class="subagent-models-box"
                  checked={checked}
                  disabled={checked && lastRemaining}
                  onChange={(event) => toggle(model.id, event.currentTarget.checked)}
                />
                <span class="subagent-models-name">{model.name}</span>
                {model.alias && <span class="subagent-models-alias">{model.alias}</span>}
              </label>
            );
          })}
        </div>
      )}
    </div>
  );
}
