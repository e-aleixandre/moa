import DOMPurify from "dompurify";

// sanitizeHtml — allowlist para contenido de conversación renderizado desde
// markdown (waypoints de usuario, documentos del asistente). DOMPurify ya
// quita <script> y atributos on* por defecto; aquí además restringimos el
// conjunto de tags/atributos permitidos a lo que el pipeline markdown genera,
// para no dejar colarse SVG/MathML ni atributos exóticos.
const ALLOWED_TAGS = [
  "p", "strong", "em", "b", "i", "code", "pre",
  "ul", "ol", "li", "a",
  "h1", "h2", "h3", "h4",
  "blockquote", "br", "span", "del", "hr",
  "table", "thead", "tbody", "tr", "th", "td",
];

const ALLOWED_ATTR = ["href", "class"];

export function sanitizeHtml(html) {
  return DOMPurify.sanitize(html, {
    ALLOWED_TAGS,
    ALLOWED_ATTR,
    // Enlaces siempre con rel seguro: sin esto un <a target="_blank"> del
    // asistente podría abusar de window.opener sobre el documento origen.
    ADD_ATTR: ["target", "rel"],
  });
}

// Fuerza target/rel seguros en cualquier <a> tras sanitizar (DOMPurify no
// reescribe atributos existentes, solo permite/deniega, así que lo hacemos
// como hook).
DOMPurify.addHook("afterSanitizeAttributes", (node) => {
  if (node.tagName === "A") {
    node.setAttribute("target", "_blank");
    node.setAttribute("rel", "noopener noreferrer");
  }
});
