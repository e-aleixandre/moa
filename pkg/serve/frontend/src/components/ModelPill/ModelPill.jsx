import { ThinkingMeter } from "../../primitives/index.js";
import "./ModelPill.css";

// ModelPill — model pill with the ThinkingMeter (variant "bars" by
// default) embedded. `accent` tints the model name (sol=lavender,
// fable=peach, terra=teal… any valid color token).
//
// The whole pill is a single button that opens the Model & thinking selector,
// on desktop and mobile alike: a button nested inside another button reads as
// an accident, so the meter is a status indicator, never its own click target.
export function ModelPill({
  model,
  level = "off",
  variant = "bars",
  accent = "lavender",
  hot = false,
  readOnly = false,
  onClick,
  ...rest
}) {
  // The highest effort always renders "hot" (peach) on the persistent pill meter, even when
  // the caller doesn't pass `hot` — the spec requires the pill to reflect xhigh
  // as hot everywhere (desktop and mobile). Astra calls that effort "max".
  const isHot = hot || level === "xhigh" || level === "max";
  // Read-only: the same pill as a plain span. Used inside a subagent, where the
  // model is the CHILD's and can't be changed from there — a disabled button
  // would promise an action that doesn't exist anywhere, rather than stating a
  // fact.
  if (readOnly) {
    return (
      <span class="model-pill" {...rest}>
        <span class="m-name" style={{ color: `var(--${accent})` }}>
          {model}
        </span>
        <ThinkingMeter variant={variant} level={level} hot={isHot} label={`Thinking: ${level}`} />
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
