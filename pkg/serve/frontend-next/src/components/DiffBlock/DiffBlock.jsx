import { useState } from "preact/hooks";
import { Copy, Check } from "lucide-preact";
export { parseUnifiedDiff } from "../../data/util/unified-diff.js";
import { parseUnifiedDiff } from "../../data/util/unified-diff.js";
import "./DiffBlock.css";


// DiffBlock — diff variant of CodeBlock: "diff" header in teal + filename
// + copy, body with numbered lines (add/del/ctx). Accepts already-
// structured `lines` ({oldNo?, newNo?, type, text}) or raw `diffText`
// (unified diff, parsed with parseUnifiedDiff).
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
      // clipboard not available: visual no-op.
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
