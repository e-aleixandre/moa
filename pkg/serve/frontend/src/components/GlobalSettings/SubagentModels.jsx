import { useEffect, useMemo, useRef, useState } from "preact/hooks";
import { Check, ChevronRight, Search, X } from "lucide-preact";
import { api } from "../../data/api.js";
import { addToast } from "../../data/notifications.js";
import { deriveModelSpecs } from "../../data/selectors.js";
import { Segmented } from "../Segmented/Segmented.jsx";
import { groupByProvider, specMatches } from "../ModelSelector/model-selector-model.js";
import {
  allowedCount,
  createAllowedModelsWriter,
  nextAllowedModels,
  scopeForAllowed,
} from "./subagent-models-model.js";

const SCOPE_OPTIONS = [
  { value: "all", label: "All models" },
  { value: "selected", label: "Selected only" },
];

// AllowToggle — the "may delegate here" control for one model.
//
// Deliberately NOT the star: the star already means "pinned" everywhere else
// in this app, and reusing it for a permission would make two different ideas
// share one glyph. A check in a green box reads as granted permission, and
// green is otherwise unused in the model vocabulary (mauve = current
// selection, yellow = pinned), so the three states never blur together.
function AllowToggle({ model, allowed, locked, onToggle }) {
  const label = `${model.codename}${model.sub ? ` ${model.sub}` : ""}`;
  return (
    <button
      type="button"
      class={`subagent-allow${allowed ? " on" : ""}`}
      aria-pressed={allowed}
      aria-label={`${allowed ? "Disallow" : "Allow"} ${label} for subagents`}
      title={locked ? "At least one model must stay allowed" : undefined}
      disabled={locked}
      onClick={() => onToggle(model.catalogId, !allowed)}
    >
      <span class="subagent-allow-box" aria-hidden="true">
        <Check size={12} />
      </span>
    </button>
  );
}

function ModelRow({ model, allowed, locked, onToggle, showProvider = false }) {
  return (
    <div class={`subagent-model-row${allowed ? " on" : ""}`}>
      <span class="subagent-model-dot" style={{ background: `var(--${model.accent})` }} aria-hidden="true" />
      <span class="subagent-model-copy">
        <span style={{ color: `var(--${model.accent})` }}>{model.codename}</span>
        {model.sub && <small>{model.sub}</small>}
      </span>
      {showProvider && <span class="subagent-provider-badge">{model.provider}</span>}
      <AllowToggle model={model} allowed={allowed} locked={locked} onToggle={onToggle} />
    </div>
  );
}

// ProviderSection — one collapsed row per provider, opened in place.
//
// Folding by provider is what keeps this section short: today's 14 models fit
// in a handful of rows, and a 50-model catalog would still be the same handful
// of rows, so the sheet never grows a half-cut scrolling list.
function ProviderSection({ group, allowed, expanded, locked, onOpen, onToggle }) {
  const accent = group.items[0]?.accent || "overlay1";
  const initial = (group.provider || "?").slice(0, 1).toUpperCase();
  const on = allowedCount(group.items, allowed);
  return (
    <div class={`subagent-provider${expanded ? " open" : ""}`}>
      <button
        type="button"
        class="subagent-provider-row"
        aria-expanded={expanded}
        onClick={() => onOpen(expanded ? null : group.provider)}
      >
        <span
          class="subagent-provider-mark"
          style={{ color: `var(--${accent})`, background: `color-mix(in srgb, var(--${accent}) 14%, transparent)` }}
          aria-hidden="true"
        >
          {initial}
        </span>
        <span class="subagent-provider-copy">
          <span class="subagent-provider-name">{group.provider}</span>
          <small>{group.items.map((model) => model.codename).slice(0, 3).join(", ")}</small>
        </span>
        <span class={`subagent-provider-count${on ? " on" : ""}`}>
          {on}/{group.items.length}
        </span>
        <ChevronRight class="subagent-provider-chevron" size={15} aria-hidden="true" />
      </button>
      {expanded && (
        <div class="subagent-provider-body">
          {group.items.map((model) => (
            <ModelRow
              key={model.id}
              model={model}
              allowed={allowed.includes(model.catalogId)}
              locked={locked && allowed.includes(model.catalogId)}
              onToggle={onToggle}
            />
          ))}
        </div>
      )}
    </div>
  );
}

// SubagentModels — global policy for which models the agent may delegate to.
//
// The stored list IS the whole policy: an empty list means "no restriction",
// so the control has no third state. Switching to "Selected only" therefore
// starts from every model allowed (same effect, now explicit) and the user
// narrows down from there; the last allowed model cannot be revoked, because
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
  const [query, setQuery] = useState("");
  const [provider, setProvider] = useState(null);
  // One serialized writer: the policy is saved whole, so overlapping PATCHes
  // would let a stale payload win. See createAllowedModelsWriter.
  const writerRef = useRef(null);
  if (!writerRef.current) {
    writerRef.current = createAllowedModelsWriter({
      send: (ids) => api("PATCH", "/api/subagent-models", { allowed_models: ids }),
      apply: (ids) => {
        setAllowed(ids);
        setScope(scopeForAllowed(ids));
      },
      onError: (error) =>
        addToast({ title: "Could not update subagent models", detail: error.message, type: "error" }),
    });
  }
  const writer = writerRef.current;

  useEffect(() => {
    let live = true;
    Promise.all([
      api("GET", "/api/models").catch(() => []),
      api("GET", "/api/subagent-models").catch(() => null),
    ]).then(([list, policy]) => {
      if (!live) return;
      setModels(deriveModelSpecs(list || []));
      const ids = policy?.allowed_models || [];
      writer.reset(ids);
      setAllowed(ids);
      setScope(scopeForAllowed(ids));
      setLoaded(true);
    });
    return () => { live = false; };
  }, []);

  const groups = useMemo(() => groupByProvider(models), [models]);
  const q = query.trim().toLowerCase();
  const filtered = useMemo(
    () => (q ? models.filter((model) => specMatches(model, q)) : []),
    [models, q]
  );

  const onScope = (next) => {
    if (next === scope) return;
    setScope(next);
    writer.update(() => (next === "all" ? [] : models.map((model) => model.catalogId)));
  };

  // Every send starts from the writer's latest list rather than from `allowed`
  // captured in this render, so a burst of taps composes instead of each one
  // overwriting the previous with an older snapshot.
  const toggle = (id, checked) => {
    writer.update((currentAllowed) => {
      const ids = nextAllowedModels(models, currentAllowed, id, checked);
      return ids.length ? ids : null; // never persist "empty" — that means unrestricted
    });
  };

  const limited = scope === "selected";
  const locked = limited && allowed.length === 1;

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
        <div class="subagent-models-picker">
          <div class="subagent-filter">
            <Search size={15} aria-hidden="true" />
            <input
              type="search"
              value={query}
              onInput={(event) => setQuery(event.currentTarget.value)}
              placeholder="Filter models…"
              aria-label="Filter subagent models"
              autocomplete="off"
              autocorrect="off"
              autocapitalize="off"
              spellcheck={false}
            />
            {query && (
              <button
                type="button"
                class="subagent-filter-clear"
                aria-label="Clear filter"
                onClick={() => setQuery("")}
              >
                <X size={14} aria-hidden="true" />
              </button>
            )}
          </div>
          <div class="subagent-models-tally">
            <span>
              {allowed.length} of {models.length} allowed
            </span>
            {locked && <span class="subagent-models-lock">last one — keep at least one</span>}
          </div>
          {q ? (
            filtered.length ? (
              <div class="subagent-models-results" role="group" aria-label="Allowed subagent models">
                {filtered.map((model) => (
                  <ModelRow
                    key={model.id}
                    model={model}
                    allowed={allowed.includes(model.catalogId)}
                    locked={locked && allowed.includes(model.catalogId)}
                    onToggle={toggle}
                    showProvider
                  />
                ))}
              </div>
            ) : (
              <div class="subagent-models-empty">No models match “{query.trim()}”</div>
            )
          ) : (
            <div class="subagent-models-providers" role="group" aria-label="Allowed subagent models">
              {groups.map((group) => (
                <ProviderSection
                  key={group.provider}
                  group={group}
                  allowed={allowed}
                  expanded={provider === group.provider}
                  locked={locked}
                  onOpen={setProvider}
                  onToggle={toggle}
                />
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  );
}
