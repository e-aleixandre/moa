import { useState } from "preact/hooks";
import { Copy, Check } from "lucide-preact";
import hljs from "highlight.js/lib/core";
import javascript from "highlight.js/lib/languages/javascript";
import typescript from "highlight.js/lib/languages/typescript";
import python from "highlight.js/lib/languages/python";
import go from "highlight.js/lib/languages/go";
import bash from "highlight.js/lib/languages/bash";
import json from "highlight.js/lib/languages/json";
import xml from "highlight.js/lib/languages/xml";
import css from "highlight.js/lib/languages/css";
import markdown from "highlight.js/lib/languages/markdown";
import rust from "highlight.js/lib/languages/rust";
import yaml from "highlight.js/lib/languages/yaml";
import sql from "highlight.js/lib/languages/sql";
import diff from "highlight.js/lib/languages/diff";
import dockerfile from "highlight.js/lib/languages/dockerfile";
import "./CodeBlock.css";

// Registro selectivo de lenguajes (core + languages/* elegidos) — evita tirar
// del build completo de highlight.js (~190 lenguajes, >1MB minif). Ver nota
// de bundle en el README de este bloque.
hljs.registerLanguage("javascript", javascript);
hljs.registerLanguage("typescript", typescript);
hljs.registerLanguage("python", python);
hljs.registerLanguage("go", go);
hljs.registerLanguage("bash", bash);
hljs.registerLanguage("json", json);
hljs.registerLanguage("xml", xml);
hljs.registerLanguage("html", xml);
hljs.registerLanguage("css", css);
hljs.registerLanguage("markdown", markdown);
hljs.registerLanguage("rust", rust);
hljs.registerLanguage("yaml", yaml);
hljs.registerLanguage("sql", sql);
hljs.registerLanguage("diff", diff);
hljs.registerLanguage("dockerfile", dockerfile);

// escapeHtml — escapa el texto plano antes de inyectarlo como HTML. Función
// pura (sin tocar el DOM), usada solo por el fallback de highlight() cuando
// el lenguaje no está registrado — hljs.highlight con lenguaje conocido ya
// escapa el código por su cuenta.
function escapeHtml(s) {
  return s.replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[c]));
}

function highlight(code, lang) {
  if (lang && hljs.getLanguage(lang)) {
    try {
      return hljs.highlight(code, { language: lang }).value;
    } catch {
      // fall through to plain text below
    }
  }
  // Sin lenguaje reconocido: texto plano escapado (sin auto-detect, que es
  // más caro y menos predecible para bloques cortos).
  return escapeHtml(code);
}


// CodeBlock — bloque de código con cabecera (lenguaje + fichero opcional +
// copiar) y cuerpo resaltado con highlight.js (core + lenguajes registrados
// arriba). `code` es el texto plano; `lang` es un id de highlight.js.
export function CodeBlock({ code = "", lang, filename, className = "", ...rest }) {
  const [copied, setCopied] = useState(false);
  const html = highlight(code, lang);

  async function copy() {
    try {
      await navigator.clipboard.writeText(code);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      // clipboard no disponible (permisos/contexto no seguro): no-op visual.
    }
  }

  return (
    <div class={`code ${className}`.trim()} {...rest}>
      <div class="code-head">
        {lang && <span class="lang">{lang}</span>}
        {filename && <span class="filename">{filename}</span>}
        <button type="button" class="copy" onClick={copy} aria-label="Copy code">
          {copied ? <Check size={12} /> : <Copy size={12} />}
          {copied ? "copied" : "copy"}
        </button>
      </div>
      <pre>
        <code dangerouslySetInnerHTML={{ __html: html }} />
      </pre>
    </div>
  );
}
