import "./ToolTicker.css";

// ToolTicker — líneas mono que rotan verticalmente para transmitir actividad
// sin que haya que leerlas con atención (usado dentro de un Pane cuando hay
// subagentes trabajando pero no hay espacio/necesidad de un ActivityLedger
// completo). Es su propio componente en components/ (no una extensión de
// Pane) porque la animación de ciclo vertical es una pieza de contenido
// autocontenida, reutilizable fuera del grid si hiciera falta.
//
// `lines`: array de { id?, tool, text } — como máximo 3 conviven en pantalla a
// la vez en el mockup (delays escalonados de 2s sobre un ciclo de 6s). El
// delay es posicional (depende del índice); `id` es solo la key estable.
export function ToolTicker({ lines = [], className = "", ...rest }) {
  return (
    <div class={`tool-ticker ${className}`.trim()} aria-hidden="true" {...rest}>
      {lines.slice(0, 3).map((l, i) => (
        <div class="tk" key={l.id ?? `${l.tool}:${l.text}`} style={{ animationDelay: `${i * 2}s` }}>
          <b>{l.tool}</b> {l.text}
        </div>
      ))}
    </div>
  );
}
