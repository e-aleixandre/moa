import { useEffect, useLayoutEffect, useRef, useState } from "preact/hooks";
import { createPortal } from "preact/compat";
import { ChevronDown } from "lucide-preact";
import { api } from "../../data/api.js";
import { addToast } from "../../data/notifications.js";
import { Segmented } from "../Segmented/Segmented.jsx";
import { ModelSelector } from "../ModelSelector/ModelSelector.jsx";
import {
  SESSION,
  isCustom,
  normalizeChoices,
  selectorModels,
  specForMode,
  summaryLabel,
} from "./compact-model-model.js";

const MODE_OPTIONS = [
  { value: SESSION, label: "Session model" },
  { value: "custom", label: "Custom" },
];

function CompactModelPicker({ choices, selected, onSelect }) {
  const [open, setOpen] = useState(false);
  const triggerRef = useRef(null);
  const pickerRef = useRef(null);
  const triggerBoundsRef = useRef(null);
  const [position, setPosition] = useState(null);
  const selectedChoice = choices.find((choice) => choice.spec === selected);
  const close = () => { setOpen(false); requestAnimationFrame(() => triggerRef.current?.focus()); };
  const show = () => {
    const rect = triggerRef.current?.getBoundingClientRect();
    triggerBoundsRef.current = rect || null;
    if (rect) setPosition({ left: Math.max(16, Math.min(rect.left, window.innerWidth - 376)), top: rect.bottom + 8 });
    setOpen(true);
  };
  useLayoutEffect(() => {
    if (!open || !triggerBoundsRef.current || window.innerWidth <= 600) return undefined;
    const rect = triggerBoundsRef.current;
    const height = pickerRef.current?.offsetHeight || 0;
    const below = rect.bottom + 8;
    setPosition({
      left: Math.max(16, Math.min(rect.left, window.innerWidth - 376)),
      top: below + height <= window.innerHeight - 16 ? below : Math.max(16, rect.top - 8 - height),
    });
    return undefined;
  }, [open]);
  useEffect(() => {
    if (!open) return undefined;
    const frame = requestAnimationFrame(() => pickerRef.current?.querySelector("input")?.focus());
    return () => cancelAnimationFrame(frame);
  }, [open]);
  return <>
    <button ref={triggerRef} type="button" class="compact-model-picker-trigger" aria-label="Model that writes compaction summaries" aria-describedby="compact-model-hint" aria-haspopup="dialog" aria-expanded={open} onClick={show}>
      <span class="compact-model-picker-choice"><span>{selectedChoice?.name || selected}</span>{selectedChoice?.provider && <small>{selectedChoice.provider}</small>}</span><ChevronDown size={16} aria-hidden="true" />
    </button>
    {open && typeof document !== "undefined" && document.body && createPortal(<div class="compact-model-picker-layer" onClick={close}><div class="compact-model-picker-popover" ref={pickerRef} role="dialog" aria-label="Choose compaction model" style={position || undefined} onClick={(event) => event.stopPropagation()} onKeyDown={(event) => { if (event.key === "Escape") { event.stopPropagation(); if (!event.defaultPrevented) close(); } }}><ModelSelector models={selectorModels(choices)} selected={selected} modelOnly onSelect={(spec) => { onSelect(spec); close(); }} /></div></div>, document.body)}
  </>;
}

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
        <CompactModelPicker choices={choices} selected={spec} onSelect={save} />
      )}
    </div>
  );
}
