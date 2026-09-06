import { useEffect, useMemo, useRef, useState } from "preact/hooks";
import { Check, ChevronLeft, ChevronRight, Search, Star, X } from "lucide-preact";
import { api } from "../../data/api.js";
import { addToast } from "../../data/notifications.js";
import { thinkingOptionsFor, thinkingPositionFor } from "../../data/selectors.js";
import { Segmented } from "../Segmented/Segmented.jsx";
import {
  groupByProvider,
  pinnedModelSpecs,
  specMatches,
  visiblePinnedSpecs,
} from "./model-selector-model.js";
import "./ModelSelector.css";

const THINKING_OPTIONS = [
  { value: "off", label: "off", bars: 0 },
  { value: "low", label: "low", bars: 1 },
  { value: "medium", label: "med", bars: 2 },
  { value: "high", label: "high", bars: 3 },
  { value: "xhigh", label: "xhigh", bars: 4 },
];

const PINNED_COLLAPSE_THRESHOLD = 4;

function ThinkingStepper({ value, onChange, options = THINKING_OPTIONS }) {
  const selected = options.find((option) => option.value === value) || options[0];
  const hot = selected?.value === "xhigh";
  return (
    <div class="think-block">
      <div class="think-lbl" id="model-selector-thinking-label">
        Thinking <b class={hot ? "hot" : ""}>{selected?.label.toUpperCase()}</b>
      </div>
      <Segmented
        options={options}
        value={value}
        onChange={onChange}
        aria-labelledby="model-selector-thinking-label"
        className="think-steps"
        itemClassName={(opt) => (opt.id === "xhigh" ? "think-step hot" : "think-step")}
        renderOption={(opt) => (
          <>
            <span class="tks" aria-hidden="true">
              {opt.bars === 0 ? (
                <span class="off" />
              ) : (
                [0, 1, 2, 3].map((i) => <i key={i} class={i < opt.bars ? "f" : ""} />)
              )}
            </span>
            {opt.label}
          </>
        )}
      />
    </div>
  );
}

function CurrentModelRow({ spec, sessionModel, onOpenProvider }) {
  const content = spec ? (
    <>
      <span class="cur-lbl">Current</span>
      <span class="cur-name" style={{ color: `var(--${spec.accent})` }}>
        {spec.codename}
      </span>
      {spec.sub && <span class="cur-sub">{spec.sub}</span>}
      {onOpenProvider && <ChevronRight size={13} aria-hidden="true" />}
    </>
  ) : (
    <>
      <span class="cur-lbl">Current</span>
      <span class="cur-name">{sessionModel}</span>
      <span class="cur-sub">custom · not in catalog</span>
    </>
  );

  if (!onOpenProvider) return <div class="cur-row">{content}</div>;
  return (
    <button type="button" class="cur-row cur-row--button" onClick={onOpenProvider}>
      {content}
    </button>
  );
}

function SectionHeading({ children, trailing }) {
  return (
    <div class="model-section-heading">
      <span>{children}</span>
      <i aria-hidden="true" />
      {trailing && <span class="model-section-count">{trailing}</span>}
    </div>
  );
}

function PinButton({ model, pinned, onToggle }) {
  return (
    <button
      type="button"
      class={`model-pin${pinned ? " on" : ""}`}
      aria-label={`${pinned ? "Unpin" : "Pin"} ${model.codename}${model.sub ? ` ${model.sub}` : ""}`}
      aria-pressed={pinned}
      onClick={() => onToggle?.(model, !pinned)}
    >
      <Star size={15} fill={pinned ? "currentColor" : "none"} aria-hidden="true" />
    </button>
  );
}

function ModelChip({ model, selected, pinned, onSelect, onTogglePin, pinnable = true }) {
  const on = model.id === selected;
  return (
    <div class={`mchip-wrap${on ? " on" : ""}`}>
      <button
        type="button"
        class="mchip"
        onClick={() => onSelect?.(model.id)}
        aria-pressed={on}
      >
        <span class="cn" style={on ? undefined : { color: `var(--${model.accent})` }}>
          {model.codename}
          {on && <Check class="check" size={12} aria-hidden="true" />}
        </span>
        {model.sub && <span class="cv">{model.sub}</span>}
      </button>
      {pinnable && <PinButton model={model} pinned={pinned} onToggle={onTogglePin} />}
    </div>
  );
}

function ModelGrid({ models, selected, pinnedIDs, onSelect, onTogglePin, pinnable = true }) {
  return (
    <div class="chip-grid">
      {models.map((model) => (
        <ModelChip
          key={model.id}
          model={model}
          selected={selected}
          pinned={pinnedIDs.includes(model.catalogId)}
          onSelect={onSelect}
          onTogglePin={onTogglePin}
          pinnable={pinnable}
        />
      ))}
    </div>
  );
}

function SearchResults({ models, selected, pinnedIDs, onSelect, onTogglePin, pinnable = true }) {
  return (
    <div class="model-results">
      {models.map((model) => {
        const on = model.id === selected;
        const pinned = pinnedIDs.includes(model.catalogId);
        return (
          <div key={model.id} class={`model-result${on ? " on" : ""}`}>
            <button
              type="button"
              class="model-result-select"
              aria-pressed={on}
              onClick={() => onSelect?.(model.id)}
            >
              <span class="model-result-dot" style={{ background: `var(--${model.accent})` }} />
              <span class="model-result-copy">
                <span style={{ color: `var(--${model.accent})` }}>{model.codename}</span>
                {model.sub && <small>{model.sub}</small>}
              </span>
              <span class="model-provider-badge">{model.provider}</span>
              {on && <Check size={12} aria-hidden="true" />}
            </button>
            {pinnable && <PinButton model={model} pinned={pinned} onToggle={onTogglePin} />}
          </div>
        );
      })}
    </div>
  );
}

function ProviderList({ groups, selected, onOpen }) {
  return (
    <div class="provider-list">
      {groups.map((group) => {
        const current = group.items.some((model) => model.id === selected);
        const preview = group.items.slice(0, 3).map((model) => model.codename).join(", ");
        const accent = group.items[0]?.accent || "overlay1";
        const initial = (group.provider || "?").slice(0, 1).toUpperCase();
        return (
          <button
            key={group.provider}
            type="button"
            class="provider-row"
            onClick={() => onOpen(group.provider)}
          >
            <span
              class="provider-mark"
              style={{ color: `var(--${accent})`, background: `color-mix(in srgb, var(--${accent}) 14%, transparent)` }}
              aria-hidden="true"
            >
              {initial}
            </span>
            <span class="provider-copy">
              <span class="provider-name">
                {group.provider}
                {current && <i class="provider-current" aria-label="Contains current model" />}
              </span>
              <small>{preview}</small>
            </span>
            <span class="provider-count">{group.items.length}</span>
            <ChevronRight size={15} aria-hidden="true" />
          </button>
        );
      })}
    </div>
  );
}

// FastRow — the speed switch, sibling to the thinking stepper: one buys depth,
// the other buys time. A two-state toggle rather than a segmented control,
// because the options are not peers — standard is the default and fast is an
// opt-in that costs more. On a model that can't serve it the row stays visible
// but disabled: hiding it would make the option seem to come and go, leaving
// you to memorise which models have it.
function FastRow({ value, supported, note, onChange }) {
  const on = value && supported;
  return (
    <div class="fast-block">
      <div class="fast-lbl" id="model-selector-fast-label">
        Fast <b class={on ? "on" : ""}>{on ? "ON" : "OFF"}</b>
      </div>
      <button
        type="button"
        class={`fast-toggle${on ? " is-on" : ""}`}
        role="switch"
        aria-checked={on}
        aria-labelledby="model-selector-fast-label"
        aria-describedby="model-selector-fast-note"
        disabled={!supported || !onChange}
        onClick={onChange ? () => onChange(!on) : undefined}
      >
        <span class="fast-track" aria-hidden="true">
          <span class="fast-knob" />
        </span>
        <span class="fast-text">{supported ? "Same model, less waiting" : "Not available on this model"}</span>
      </button>
      {supported && note && (
        <p class="fast-note" id="model-selector-fast-note">
          {note}
        </p>
      )}
    </div>
  );
}

export function ModelSelector({
  models = [],
  selected,
  thinking = "off",
  fast = false,
  fastSupported = false,
  fastNote = "",
  onSelect,
  onThinkingChange,
  onFastChange,
  embedded = false,
  modelOnly = false,
  sessionModel,
  sessionProvider,
  ...rest
}) {
  const [query, setQuery] = useState("");
  const [provider, setProvider] = useState(null);
  const [pinnedIDs, setPinnedIDs] = useState([]);
  const pinnedIDsRef = useRef([]);
  const preferenceRevisionRef = useRef(0);
  const preferenceQueueRef = useRef(Promise.resolve());
  const [pinsExpanded, setPinsExpanded] = useState(false);
  const groups = useMemo(() => groupByProvider(models), [models]);
  const selectedSpec = useMemo(
    () => models.find((model) => model.id === selected),
    [models, selected]
  );
  const pinned = useMemo(
    () => pinnedModelSpecs(models, pinnedIDs),
    [models, pinnedIDs]
  );
  const visiblePins = visiblePinnedSpecs(pinned, pinsExpanded, PINNED_COLLAPSE_THRESHOLD);
  const q = query.trim().toLowerCase();
  const filtered = useMemo(
    () => (q ? models.filter((model) => specMatches(model, q)) : []),
    [models, q]
  );
  const providerGroup = groups.find((group) => group.provider === provider);

  const applyPinnedIDs = (ids) => {
    pinnedIDsRef.current = ids;
    setPinnedIDs(ids);
  };

  useEffect(() => {
    if (modelOnly) return undefined;
    let live = true;
    const revision = preferenceRevisionRef.current;
    api("GET", "/api/model-preferences")
      .then((preferences) => {
        if (live && revision === preferenceRevisionRef.current) {
          applyPinnedIDs(preferences?.pinned_models || []);
        }
      })
      .catch(() => {
        if (live && revision === preferenceRevisionRef.current) applyPinnedIDs([]);
      });
    return () => { live = false; };
  }, [modelOnly]);

  const updatePin = (model, shouldPin, { offerUndo = true } = {}) => {
    const before = pinnedIDsRef.current;
    const optimistic = shouldPin
      ? [...before.filter((id) => id !== model.catalogId), model.catalogId]
      : before.filter((id) => id !== model.catalogId);
    const revision = ++preferenceRevisionRef.current;
    applyPinnedIDs(optimistic);

    const request = preferenceQueueRef.current
      .catch(() => {})
      .then(() => api("PATCH", "/api/model-preferences", {
        model_id: model.catalogId,
        pinned: shouldPin,
      }));
    preferenceQueueRef.current = request;
    request.then((preferences) => {
      if (revision === preferenceRevisionRef.current) {
        applyPinnedIDs(preferences?.pinned_models || optimistic);
      }
      if (!shouldPin && offerUndo) {
        addToast({
          title: `Unpinned ${model.codename}`,
          type: "info",
          action: {
            label: "Undo",
            onClick: () => updatePin(model, true, { offerUndo: false }),
          },
        });
      }
    }).catch((error) => {
      if (revision === preferenceRevisionRef.current) applyPinnedIDs(before);
      addToast({ title: "Could not update pinned models", detail: error.message, type: "error" });
    });
  };

  const openProvider = (nextProvider) => {
    setQuery("");
    setProvider(nextProvider);
  };

  const showRoot = () => {
    setQuery("");
    setProvider(null);
    setPinsExpanded(false);
  };

  return (
    <div class={`model-selector${embedded ? " model-selector--embedded" : ""}${modelOnly ? " model-selector--model-only" : ""}`} {...rest}>
      {!embedded && !modelOnly && <div class="sel-head">Model &amp; thinking</div>}
      {!modelOnly && <ThinkingStepper
        value={thinkingPositionFor(thinking, selectedSpec, sessionProvider)}
        onChange={onThinkingChange}
        options={thinkingOptionsFor(selectedSpec, sessionProvider)}
      />}
      {!modelOnly && <FastRow value={fast} supported={fastSupported} note={fastNote} onChange={onFastChange} />}
      {!modelOnly && !!sessionModel && (
        <CurrentModelRow
          spec={selectedSpec}
          sessionModel={sessionModel}
          onOpenProvider={selectedSpec ? () => openProvider(selectedSpec.provider) : undefined}
        />
      )}
      <div class="model-filter">
        <Search size={15} aria-hidden="true" />
        <input
          type="search"
          value={query}
          onInput={(event) => setQuery(event.currentTarget.value)}
          placeholder="Filter models…"
          aria-label="Filter models"
          autocomplete="off"
          autocorrect="off"
          autocapitalize="off"
          spellcheck={false}
        />
        {query && (
          <button type="button" class="model-filter-clear" aria-label="Clear filter" onClick={showRoot}>
            <X size={14} aria-hidden="true" />
          </button>
        )}
      </div>

      {q ? (
        <div class="model-selector-view">
          <SectionHeading trailing={`${filtered.length}`}>Results</SectionHeading>
          {filtered.length > 0 ? (
            <SearchResults
              models={filtered}
              selected={selected}
              pinnedIDs={pinnedIDs}
              onSelect={onSelect}
              onTogglePin={updatePin}
              pinnable={!modelOnly}
            />
          ) : (
            <div class="model-empty">No models match “{query.trim()}”</div>
          )}
        </div>
      ) : providerGroup ? (
        <div class="model-selector-view model-provider-view">
          <div class="provider-view-head">
            <button type="button" class="provider-back" onClick={showRoot}>
              <ChevronLeft size={14} aria-hidden="true" /> All models
            </button>
            <span>{providerGroup.provider} · {providerGroup.items.length}</span>
          </div>
          <ModelGrid
            models={providerGroup.items}
            selected={selected}
            pinnedIDs={pinnedIDs}
            onSelect={onSelect}
            onTogglePin={updatePin}
            pinnable={!modelOnly}
          />
        </div>
      ) : (
        <div class="model-selector-view">
          {!modelOnly && <SectionHeading trailing={`★ ${pinned.length}`}>Pinned</SectionHeading>}
          {!modelOnly && (pinned.length > 0 ? (
            <>
              <ModelGrid
                models={visiblePins}
                selected={selected}
                pinnedIDs={pinnedIDs}
                onSelect={onSelect}
                onTogglePin={updatePin}
                pinnable={!modelOnly}
              />
              {pinned.length > PINNED_COLLAPSE_THRESHOLD && (
                <button
                  type="button"
                  class="pinned-fold"
                  aria-expanded={pinsExpanded}
                  onClick={() => setPinsExpanded((expanded) => !expanded)}
                >
                  {pinsExpanded
                    ? "Show less"
                    : `Show ${pinned.length - PINNED_COLLAPSE_THRESHOLD} more`}
                </button>
              )}
            </>
          ) : (
            <div class="pinned-empty">
              <Star size={14} aria-hidden="true" />
              Pin your go-to models — tap <Star size={12} aria-hidden="true" /> on any model
            </div>
          ))}
          <SectionHeading>All models</SectionHeading>
          <ProviderList groups={groups} selected={selected} onOpen={openProvider} />
        </div>
      )}
    </div>
  );
}
