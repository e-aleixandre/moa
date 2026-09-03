import { useEffect, useState } from "preact/hooks";
import { api } from "../../data/api.js";
import { addToast } from "../../data/notifications.js";
import { Segmented } from "../Segmented/Segmented.jsx";
import {
  SESSION,
  isCustom,
  normalizeChoices,
  specForMode,
  summaryLabel,
} from "./compact-model-model.js";

const MODE_OPTIONS = [
  { value: SESSION, label: "Session model" },
  { value: "custom", label: "Custom" },
];

// CompactModel — which model writes compaction summaries.
//
// Summarizing is extraction over a flattened transcript under its own system
// prompt: it shares no cached prefix with the conversation, so the session's
// (often pricier) model is rarely the right tool for it. This is the control
// for that choice, and it is global — subagents summarize with it too.
//
// The list only offers models whose provider has a credential right now. A
// model that cannot be reached would degrade to the session's model on some
// later compaction, long after this sheet was closed.
export function CompactModel() {
  const [spec, setSpec] = useState(SESSION);
  const [choices, setChoices] = useState([]);
  const [loaded, setLoaded] = useState(false);

  useEffect(() => {
    let live = true;
    api("GET", "/api/compact-model")
      .catch(() => null)
      .then((policy) => {
        if (!live) return;
        setSpec(policy?.compact_model || SESSION);
        setChoices(normalizeChoices(policy?.choices));
        setLoaded(true);
      });
    return () => { live = false; };
  }, []);

  const save = (next) => {
    if (!next || next === spec) return;
    const previous = spec;
    setSpec(next);
    api("PATCH", "/api/compact-model", { compact_model: next })
      .then((policy) => {
        setSpec(policy?.compact_model || next);
        if (policy?.choices) setChoices(normalizeChoices(policy.choices));
      })
      .catch((error) => {
        addToast({
          title: "Could not set the compaction model",
          detail: String(error.message || error),
          type: "error",
        });
        setSpec(previous);
      });
  };

  const mode = isCustom(spec) ? "custom" : SESSION;
  const noChoices = choices.length === 0;

  return (
    <div class="compact-at">
      <p class="compact-at-hint" id="compact-model-hint">
        {summaryLabel(spec, choices)}
      </p>
      <Segmented
        options={MODE_OPTIONS}
        value={mode}
        onChange={(next) => save(specForMode(next, spec, choices))}
        // "Custom" with nothing to choose from would be a dead end, so the
        // whole control stays put until the catalog arrives.
        disabled={!loaded || noChoices}
        aria-label="Compaction model"
        aria-describedby="compact-model-hint"
      />
      {mode === "custom" && !noChoices && (
        <select
          class="compact-model-select"
          value={spec}
          aria-label="Model that writes compaction summaries"
          onChange={(e) => save(e.currentTarget.value)}
        >
          {choices.map((c) => (
            <option key={c.spec} value={c.spec}>
              {c.name}
            </option>
          ))}
        </select>
      )}
    </div>
  );
}
