import { X } from "lucide-preact";
import { StateDot } from "../../primitives/index.js";
import "./SessionRow.css";

// SessionRow — la pieza de sesión, un único componente con 3 variantes
// conmutables (pill | tab | card) para comparar en vivo las direcciones
// A/B/C, igual que ThinkingMeter con `variant`.
//
// `state` "permission" y "error" tiñen toda la fila ("needs you"), no solo el
// punto: permission usa amarillo (como el mockup), error usa la misma pauta
// en rojo para mantener la convención de semáforo del sistema.
const NEEDS_TONE = {
  permission: "yellow",
  error: "red",
};

// Sufijo añadido al nombre accesible del botón cuando el estado es relevante
// para quien usa lector de pantalla (no solo color/icono).
const STATE_LABEL_SUFFIX = {
  permission: ", requires permission",
  error: ", error",
};

export function SessionRow({
  title,
  state = "idle",
  variant = "card",
  active = false,
  unseen = false,
  meta,
  age,
  pane,
  onClick,
  onClose,
  ...rest
}) {
  const needs = NEEDS_TONE[state];
  const classes = [
    "session-row",
    `variant-${variant}`,
    active ? "on" : "",
    needs ? `needs-${needs}` : "",
  ]
    .filter(Boolean)
    .join(" ");

  const handleClose = (event) => {
    event.stopPropagation();
    onClose?.(event);
  };

  const hitLabel = `${title}${pane ? `, pane ${pane}` : ""}${STATE_LABEL_SUFFIX[state] ?? ""}`;

  return (
    <span class={classes} {...rest}>
      <button
        type="button"
        class="session-row-hit"
        onClick={onClick}
        aria-current={active ? "true" : undefined}
        aria-label={hitLabel}
      >
        {variant === "card" ? (
          <>
            <span class="r1">
              <StateDot state={state} size={8} />
              <span class="title" aria-hidden="true">{title}</span>
              {pane && <span class="pane" aria-hidden="true">{pane}</span>}
              {unseen && <span class="unseen" aria-hidden="true" />}
            </span>
            {meta && <span class="r2" aria-hidden="true">{meta}</span>}
          </>
        ) : (
          <>
            <StateDot state={state} size={8} />
            <span class="title" aria-hidden="true">{title}</span>
            {unseen && <span class="unseen" aria-hidden="true" />}
            {variant === "tab" && age && <span class="n" aria-hidden="true">{age}</span>}
          </>
        )}
      </button>
      {onClose && (
        <button
          type="button"
          class="x"
          aria-label={`Close ${title}`}
          onClick={handleClose}
        >
          <X size={11} />
        </button>
      )}
    </span>
  );
}
