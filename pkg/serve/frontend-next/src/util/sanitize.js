import DOMPurify from "dompurify";

// sanitizeHtml — allowlist for conversation content rendered from markdown
// (user waypoints, assistant documents). DOMPurify already strips <script>
// and on* attributes by default; here we further restrict the set of allowed
// tags/attributes to what the markdown pipeline generates, so SVG/MathML and
// exotic attributes can't slip through.
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
    // Links always with a safe rel: without this an assistant <a target="_blank">
    // could abuse window.opener against the origin document.
    ADD_ATTR: ["target", "rel"],
  });
}

// Forces safe target/rel on any <a> after sanitizing (DOMPurify does not
// rewrite existing attributes, it only allows/denies, so we do it as a hook).
DOMPurify.addHook("afterSanitizeAttributes", (node) => {
  if (node.tagName === "A") {
    node.setAttribute("target", "_blank");
    node.setAttribute("rel", "noopener noreferrer");
  }
});
