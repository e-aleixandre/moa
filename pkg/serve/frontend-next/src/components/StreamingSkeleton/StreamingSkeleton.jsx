import "./StreamingSkeleton.css";

// StreamingSkeleton — bloque de "texto que llega" con barras shimmer, para el
// contenido que aún no ha sido emitido por el modelo. Vive en components/
// porque es una pieza de contenido dentro del stream (como AssistantDocument
// con streaming=true, del que es el complemento visual: el párrafo que ya
// llegó lleva el cursor parpadeante de AssistantDocument, y debajo va este
// skeleton para lo que falta por llegar).
//
// `widths` por defecto reproduce el mockup (92%/78%/45%, decreciente).
const DEFAULT_WIDTHS = ["92%", "78%", "45%"];

export function StreamingSkeleton({ widths = DEFAULT_WIDTHS, className = "", ...rest }) {
  return (
    <div class={`skel-block ${className}`.trim()} {...rest}>
      {widths.map((w, i) => (
        <div key={i} class="skel-line" style={{ width: w }} />
      ))}
    </div>
  );
}

// TypingDots — tres puntos mauve que rebotan, para marcar que el asistente
// sigue "pensando/escribiendo" al final de un párrafo (p.ej. "…I'll draft
// the notes against it •••"). Componente pequeño e independiente porque se
// usa inline, a diferencia del bloque de StreamingSkeleton.
export function TypingDots({ label = "typing", className = "", ...rest }) {
  return (
    <span class={`typing-dots ${className}`.trim()} role="img" aria-label={label} {...rest}>
      <i />
      <i />
      <i />
    </span>
  );
}
