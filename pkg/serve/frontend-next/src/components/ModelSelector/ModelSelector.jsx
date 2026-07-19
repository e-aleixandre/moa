import { Check } from "lucide-preact";
import { Segmented } from "../Segmented/Segmented.jsx";
import "./ModelSelector.css";

const THINKING_OPTIONS = [
  { value: "off", label: "off" },
  { value: "low", label: "low" },
  { value: "medium", label: "med" },
  { value: "high", label: "high" },
  { value: "xhigh", label: "xhigh" },
];

// ModelSelector — popover for model selection + thinking level.
// `models`: [{ id, name, desc, sigil, accent }]. `accent` is a color
// token name (e.g. "peach"), used to tint the sigil. `thinking` is
// the canonical value ("off" | "low" | "medium" | "high" | "xhigh"), the same
// vocabulary consumed by ThinkingMeter.
export function ModelSelector({
  models,
  selected,
  thinking = "off",
  onSelect,
  onThinkingChange,
  embedded = false,
  ...rest
}) {
  return (
    <div class={`model-selector${embedded ? " model-selector--embedded" : ""}`} {...rest}>
      {!embedded && <div class="sel-head">Model</div>}
      {models.map((m) => {
        const on = m.id === selected;
        return (
          <button
            key={m.id}
            type="button"
            class={`sel-row${on ? " on" : ""}`}
            onClick={() => onSelect?.(m.id)}
            aria-pressed={on}
          >
            <span class="sig" style={{ background: `color-mix(in srgb, var(--${m.accent}) 18%, transparent)`, color: `var(--${m.accent})` }}>
              {m.sigil}
            </span>
            <span class="sel-row-text">
              <span class="nm">{m.name}</span>
              <span class="desc">{m.desc}</span>
            </span>
            {on && (
              <span class="check" aria-hidden="true">
                <Check size={13} />
              </span>
            )}
          </button>
        );
      })}
      <div class="sel-think">
        <div class="lbl" id="model-selector-thinking-label">
          Thinking <b>{thinking.toUpperCase()}</b>
        </div>
        <Segmented
          options={THINKING_OPTIONS}
          value={thinking}
          onChange={onThinkingChange}
          aria-labelledby="model-selector-thinking-label"
        />
      </div>
    </div>
  );
}
