import "./Spinner.css";

// Spinner — anillo giratorio pequeño para indicar trabajo en curso. Extrae y
// tokeniza el patrón que ya vivía duplicado en AgentTray (agent-spinner):
// aquí gana una variante de color (blue/sky/teal/mauve) para poder distinguir
// varios subagentes en paralelo sin inventar un nuevo patrón por cada sitio
// que lo necesite (FanoutBlock, AgentTray, tool tickers, etc.).
const COLORS = ["blue", "sky", "teal", "mauve"];

const DEFAULT_SIZE = 11;

export function Spinner({ color = "blue", size = DEFAULT_SIZE, label, ...rest }) {
  const safe = COLORS.includes(color) ? color : "blue";
  const a11y = label
    ? { role: "img", "aria-label": label }
    : { "aria-hidden": "true" };
  return (
    <span
      class={`spinner c-${safe}`}
      style={{ width: `${size}px`, height: `${size}px` }}
      {...a11y}
      {...rest}
    />
  );
}
