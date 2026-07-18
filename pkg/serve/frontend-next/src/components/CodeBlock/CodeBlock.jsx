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

// Selective language registration (core + chosen languages/*) — avoids pulling
// in the full highlight.js build (~190 languages, >1MB minified). See bundle
// note in this block's README.
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

// escapeHtml — escapes plain text before injecting it as HTML. Pure
// function (doesn't touch the DOM), used only by highlight()'s fallback when
// the language isn't registered — hljs.highlight with a known language already
// escapes the code on its own.
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
  // No recognized language: escaped plain text (no auto-detect, which is
  // more expensive and less predictable for short blocks).
  return escapeHtml(code);
}


// CodeBlock — code block with a header (language + optional filename +
// copy) and a body highlighted with highlight.js (core + languages registered
// above). `code` is the plain text; `lang` is a highlight.js id.
// `showHeader={false}` hides the header (lang/filename/copy) for
// dense uses where only the body matters (e.g. a pane's mini-code).
export function CodeBlock({ code = "", lang, filename, showHeader = true, className = "", ...rest }) {
  const [copied, setCopied] = useState(false);
  const html = highlight(code, lang);

  async function copy() {
    try {
      await navigator.clipboard.writeText(code);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      // clipboard not available (permissions/insecure context): visual no-op.
    }
  }

  return (
    <div class={`code ${className}`.trim()} {...rest}>
      {showHeader && (
        <div class="code-head">
          {lang && <span class="lang">{lang}</span>}
          {filename && <span class="filename">{filename}</span>}
          <button type="button" class="copy" onClick={copy} aria-label="Copy code">
            {copied ? <Check size={12} /> : <Copy size={12} />}
            {copied ? "copied" : "copy"}
          </button>
        </div>
      )}
      <pre>
        <code dangerouslySetInnerHTML={{ __html: html }} />
      </pre>
    </div>
  );
}
