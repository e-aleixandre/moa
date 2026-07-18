import { useState } from "preact/hooks";
import { Copy, Check } from "lucide-preact";
import "./DiffBlock.css";

// parseUnifiedDiff — parser simple y robusto de un diff unificado (formato
// `git diff`/`diff -u`): entiende cabeceras `@@ -a,b +c,d @@`, líneas de
// contexto, `+` (add) y `-` (del). Ignora metadatos (`diff --git`, `index `,
// `--- `/`+++ `) y la marca `\ No newline at end of file` — ninguno de ellos
// se numera ni se muestra como línea de contexto. Las líneas de contenido
// solo se procesan tras haber visto una cabecera de hunk `@@`; cualquier
// cosa antes de eso (o metadatos no reconocidos) se descarta.
// Devuelve un array de {oldNo, newNo, type, text} listo para <DiffBlock>.
export function parseUnifiedDiff(diffText) {
  const lines = diffText.split("\n");
  const out = [];
  let oldNo = 0;
  let newNo = 0;
  let inHunk = false;
  for (const raw of lines) {
    if (raw === "") continue;
    if (raw.startsWith("\\ No newline at end of file")) continue;
    if (raw.startsWith("diff --git") || raw.startsWith("index ")) continue;
    if (raw.startsWith("--- ") || raw.startsWith("+++ ")) continue;
    const hunk = raw.match(/^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/);
    if (hunk) {
      oldNo = parseInt(hunk[1], 10);
      newNo = parseInt(hunk[2], 10);
      inHunk = true;
      continue;
    }
    if (!inHunk) continue;
    if (raw.startsWith("+")) {
      out.push({ newNo: newNo, type: "add", text: raw.slice(1) });
      newNo++;
    } else if (raw.startsWith("-")) {
      out.push({ oldNo: oldNo, type: "del", text: raw.slice(1) });
      oldNo++;
    } else {
      const text = raw.startsWith(" ") ? raw.slice(1) : raw;
      out.push({ oldNo: oldNo, newNo: newNo, type: "ctx", text });
      oldNo++;
      newNo++;
    }
  }
  return out;
}


// DiffBlock — variante diff del CodeBlock: cabecera "diff" en teal + fichero
// + copy, cuerpo con líneas numeradas (add/del/ctx). Acepta `lines` ya
// estructuradas ({oldNo?, newNo?, type, text}) o `diffText` (unified diff
// crudo, parseado con parseUnifiedDiff).
export function DiffBlock({ lines, diffText, filename, className = "", ...rest }) {
  const [copied, setCopied] = useState(false);
  const rows = lines ?? (diffText ? parseUnifiedDiff(diffText) : []);

  async function copy() {
    const text =
      diffText ??
      rows
        .map((l) => (l.type === "add" ? "+" : l.type === "del" ? "-" : " ") + l.text)
        .join("\n");
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      // clipboard no disponible: no-op visual.
    }
  }

  return (
    <div class={`code diff ${className}`.trim()} {...rest}>
      <div class="code-head">
        <span class="lang">diff</span>
        {filename && <span class="filename">{filename}</span>}
        <button type="button" class="copy" onClick={copy} aria-label="Copy diff">
          {copied ? <Check size={12} /> : <Copy size={12} />}
          {copied ? "copied" : "copy"}
        </button>
      </div>
      <pre>
        <code>
          {rows.map((l, i) => (
            <span key={i} class={`dl ${l.type}`}>
              <span class="no">{l.type === "add" ? l.newNo : l.oldNo}</span>
              <span class="txt">{(l.type === "add" ? "+" : l.type === "del" ? "-" : "")}{l.text}</span>
            </span>
          ))}
        </code>
      </pre>
    </div>
  );
}
