import { ThinkingMeter } from "../../primitives/index.js";
import "./ModelPill.css";

// ModelPill — model pill with the ThinkingMeter (variant "bars" by
// default) embedded. `accent` tints the model name (sol=lavender,
// fable=peach, terra=teal… any valid color token).
//
// Desktop-only shortcut (MODEL-SELECTOR-ALT-SPEC-FABLE §2, §6.2): when
// `onMeterClick` is passed, the pill splits into two click targets — the name
// opens the Model & thinking popover (`onClick`), the meter zone cycles the
// thinking level in place (off→low→medium→high→xhigh→off) without opening
// anything. Mobile never passes `onMeterClick` (that half would be <44px, and
// the split gesture isn't offered there per the spec) so it keeps the single
// whole-pill button that opens the sheet.
export function ModelPill({
  model,
  level = "off",
  variant = "bars",
  accent = "lavender",
  hot = false,
  onClick,
  onMeterClick,
  ...rest
}) {
  // xhigh always renders "hot" (peach) on the persistent pill meter, even when
  // the caller doesn't pass `hot` — the spec requires the pill to reflect xhigh
  // as hot everywhere (desktop and mobile).
  const isHot = hot || level === "xhigh";
  if (onMeterClick) {
    return (
      <span class="model-pill model-pill--split" {...rest}>
        <button type="button" class="m-name-btn" onClick={onClick}>
          <span class="m-name" style={{ color: `var(--${accent})` }}>
            {model}
          </span>
        </button>
        <button
          type="button"
          class="m-meter-btn"
          onClick={onMeterClick}
          title={`Thinking: ${level} — click to cycle`}
        >
          <ThinkingMeter variant={variant} level={level} hot={isHot} label={`Thinking: ${level}`} />
        </button>
      </span>
    );
  }
  return (
    <button
      type="button"
      class="model-pill"
      onClick={onClick}
      {...rest}
    >
      <span class="m-name" style={{ color: `var(--${accent})` }}>
        {model}
      </span>
      <ThinkingMeter variant={variant} level={level} hot={isHot} label={`Thinking: ${level}`} />
    </button>
  );
}
