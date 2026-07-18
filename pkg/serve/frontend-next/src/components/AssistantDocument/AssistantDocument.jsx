import { sanitizeHtml } from "../../util/sanitize.js";
import "./AssistantDocument.css";

// AssistantDocument — flujo de "documento" del trabajo del agente: párrafos,
// listas, títulos de sección, code inline. No impone estructura interna: el
// consumidor compone los bloques hijos (párrafos, ActivityLedger, CodeBlock,
// DiffBlock…) como children, o pasa `html` ya renderizado (markdown).
// `streaming` pinta el cursor parpadeante al final del documento, como
// hermano posterior al contenido — igual en modo `html` y modo children.
export function AssistantDocument({
  children,
  html,
  streaming = false,
  className = "",
  ...rest
}) {
  const streamClass = streaming ? " is-streaming" : "";
  if (html != null) {
    return (
      <div class={`doc ${streamClass} ${className}`.trim()} {...rest}>
        <div
          class="doc-html"
          dangerouslySetInnerHTML={{ __html: sanitizeHtml(html) }}
        />
        {streaming && <span class="doc-cursor" aria-hidden="true" />}
      </div>
    );
  }
  return (
    <div class={`doc ${streamClass} ${className}`.trim()} {...rest}>
      {children}
      {streaming && (
        <span class="doc-cursor" aria-hidden="true" />
      )}
    </div>
  );
}

