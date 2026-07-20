import { Segmented } from "../Segmented/Segmented.jsx";
import "./ModelSelector.css";

// THINKING_OPTIONS — same 5-level vocabulary as ThinkingMeter/Segmented
// elsewhere ("off"|"low"|"medium"|"high"|"xhigh"). `bars` is how many of the
// 4 mini-glyph bars are filled for that level (0 = the "off" dash glyph).
const THINKING_OPTIONS = [
  { value: "off", label: "off", bars: 0 },
  { value: "low", label: "low", bars: 1 },
  { value: "medium", label: "med", bars: 2 },
  { value: "high", label: "high", bars: 3 },
  { value: "xhigh", label: "xhigh", bars: 4 },
];

// ThinkingStepper — MODEL-SELECTOR-ALT-SPEC-FABLE §1a: 5 equal cells (off ·
// low · med · high · xhigh), each with a mini bars glyph (same metaphor as
// ThinkingMeter's variant="bars") over a mono label. Built on top of Segmented
// so the radiogroup / roving-tabindex / arrow-key a11y isn't duplicated —
// this only supplies per-option content (`renderOption`) and the "xhigh is
// peach (hot)" styling hook (`itemClassName`).
function ThinkingStepper({ value, onChange }) {
  const hot = value === "xhigh";
  return (
    <div class="think-block">
      <div class="think-lbl" id="model-selector-thinking-label">
        Thinking <b class={hot ? "hot" : ""}>{value.toUpperCase()}</b>
      </div>
      <Segmented
        options={THINKING_OPTIONS}
        value={value}
        onChange={onChange}
        aria-labelledby="model-selector-thinking-label"
        className="think-steps"
        itemClassName={(opt) => (opt.id === "xhigh" ? "think-step hot" : "think-step")}
        renderOption={(opt, on) => (
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

// ModelGrid — MODEL-SELECTOR-ALT-SPEC-FABLE §1b: chips grouped by provider,
// 2 columns. Each chip shows the codename (Opus/Sonnet/Sol/Terra…) plus a
// mono subline ("version · context"). Selected chip gets the mauve wash +
// border + check; the codename otherwise carries the model's accent color.
function ModelGrid({ models, selected, onSelect }) {
  const groups = [];
  const seen = new Map();
  for (const m of models) {
    const key = m.provider || "";
    if (!seen.has(key)) {
      seen.set(key, { provider: key, items: [] });
      groups.push(seen.get(key));
    }
    seen.get(key).items.push(m);
  }
  return (
    <div class="model-block">
      {groups.map((g) => (
        <div key={g.provider}>
          <div class="prov-lbl">{g.provider}</div>
          <div class="chip-grid">
            {g.items.map((m) => {
              const on = m.id === selected;
              return (
                <button
                  key={m.id}
                  type="button"
                  class={`mchip${on ? " on" : ""}`}
                  onClick={() => onSelect?.(m.id)}
                  aria-pressed={on}
                >
                  <span class="cn" style={on ? undefined : { color: `var(--${m.accent})` }}>
                    {m.codename}
                  </span>
                  {m.sub && <span class="cv">{m.sub}</span>}
                  {on && <span class="check" aria-hidden="true">✓</span>}
                </button>
              );
            })}
          </div>
        </div>
      ))}
    </div>
  );
}

// ModelSelector — panel for model + thinking level ("Model & thinking",
// MODEL-SELECTOR-ALT-SPEC-FABLE). Thinking comes first (it's changed more
// often than the model), the model grid follows. `models`: [{ id, name,
// provider, codename, sub, accent }] (see deriveModelSpecs). `thinking` is
// the canonical value ("off" | "low" | "medium" | "high" | "xhigh"), the same
// vocabulary consumed by ThinkingMeter. Same component for both densities:
// standalone popover on desktop (anchored to the ChatHead ModelPill),
// `embedded` inside a Sheet on mobile.
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
      {!embedded && <div class="sel-head">Model &amp; thinking</div>}
      <ThinkingStepper value={thinking} onChange={onThinkingChange} />
      <ModelGrid models={models} selected={selected} onSelect={onSelect} />
    </div>
  );
}
