import { Rewind, Maximize2, X } from "lucide-preact";
import { StateDot, IconButton, ThinkingMeter } from "../../primitives/index.js";
import "./Pane.css";

// Pane — un panel individual del grid. Reutiliza StateDot/IconButton/
// ThinkingMeter de las primitivas; el header reproduce el patrón compacto de
// ChatHead pero más denso (esto vive en una rejilla, no en la columna
// principal).
//
// El badge de modelo mono ("sol ▰▰▱▱" en el mockup) se construye con
// ThinkingMeter variant="glyph" en vez de texto literal: así el nivel de
// pensamiento queda accesible/real en vez de un string estático, y no se
// duplica la lógica de niveles que ya vive en la primitiva.
export function Pane({
  title,
  path,
  state = "idle",
  model,
  modelAccent = "lavender",
  thinkingLevel = "off",
  focused = false,
  variant = "normal",
  titleTone,
  onTitleClick,
  onRewind,
  onMaximize,
  onClose,
  children,
}) {
  const classes = [
    "pane",
    variant === "tall" ? "p-tall" : "",
    focused ? "focused" : "",
  ]
    .filter(Boolean)
    .join(" ");

  // El estado del pane (running/permission/error) va codificado por color y
  // animación en el StateDot; para lectores de pantalla lo exponemos como
  // texto en el nombre accesible del panel y del propio punto.
  const STATE_LABEL = {
    running: "running",
    permission: "requires permission",
    error: "error",
    saved: "saved",
    idle: "idle",
  };
  const stateText = STATE_LABEL[state] ?? state;

  return (
    <section class={classes} aria-label={`${title}, ${stateText}`}>
      {focused && <span class="focus-tag">FOCUS</span>}

      <div class="p-head">
        <StateDot state={state} size={9} label={stateText} />
        <button
          type="button"
          class="p-title"
          style={titleTone ? { color: `var(--${titleTone})` } : undefined}
          onClick={onTitleClick}
        >
          {title}
        </button>
        {path && <span class="p-path">{path}</span>}
        {model && (
          <span class="p-model">
            {model} <ThinkingMeter variant="glyph" level={thinkingLevel} label={`Thinking: ${thinkingLevel}`} />
          </span>
        )}

        <div class="p-tools">
          <IconButton label="Rewind" onClick={onRewind}>
            <Rewind size={13} />
          </IconButton>
          <div class="p-max-wrap">
            <IconButton label="Maximize into conversation view" onClick={onMaximize}>
              <Maximize2 size={12} />
            </IconButton>
            <span class="p-max-tip" aria-hidden="true">→ conversation view</span>
          </div>
          <IconButton label="Close pane" variant="ghost" className="p-close" onClick={onClose}>
            <X size={12} />
          </IconButton>
        </div>
      </div>

      <div class="p-body">{children}</div>

      <div class="p-input">
        <span class="p-input-text">Message moa…</span>
        <span class="send" aria-hidden="true">↑</span>
      </div>
    </section>
  );
}
