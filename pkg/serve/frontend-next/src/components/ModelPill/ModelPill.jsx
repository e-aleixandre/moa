import { ThinkingMeter } from "../../primitives/index.js";
import "./ModelPill.css";

// ModelPill — pastilla de modelo con el ThinkingMeter (variant "bars" por
// defecto) incrustado. `accent` tiñe el nombre del modelo (sol=lavender,
// fable=peach, terra=teal… cualquier token de color válido).
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
