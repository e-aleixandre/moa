import { ThinkingMeter } from "../../primitives/index.js";
import "./ModelPill.css";

// ModelPill — model pill with the ThinkingMeter (variant "bars" by
// default) embedded. `accent` tints the model name (sol=lavender,
// fable=peach, terra=teal… any valid color token).
export function ModelPill({
  model,
  level = "off",
  variant = "bars",
  accent = "lavender",
  hot = false,
  onClick,
  ...rest
}) {
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
      <ThinkingMeter variant={variant} level={level} hot={hot} label={`Thinking: ${level}`} />
    </button>
  );
}
