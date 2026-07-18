import "./StateDot.css";

// StateDot — punto de estado de sesión. Átomo base reutilizado por chips,
// tiras móviles, headers, etc.
const STATES = ["idle", "running", "permission", "error", "saved"];

// Default 8px = --space-2. Se pasa como número para permitir tamaños arbitrarios
// en contexto, pero el valor por defecto sale de la escala de espaciado.
const DEFAULT_SIZE = 8;

export function StateDot({ state = "idle", size = DEFAULT_SIZE, label, ...rest }) {
  const safe = STATES.includes(state) ? state : "saved";
  // Aislado, un punto solo-color no es accesible: si el consumidor da label lo
  // exponemos; si no, es decorativo (el estado suele ir ya en texto al lado).
  const a11y = label
    ? { role: "img", "aria-label": label }
    : { "aria-hidden": "true" };
  return (
    <span
      class={`state-dot ${safe}`}
      style={{ width: `${size}px`, height: `${size}px` }}
      {...a11y}
      {...rest}
    />
  );
}
