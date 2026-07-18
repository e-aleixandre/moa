import { useState } from "preact/hooks";
import { Copy, Check } from "lucide-preact";
import "./DiffBlock.css";

// parseUnifiedDiff — simple, robust parser for a unified diff (`git diff`/
// `diff -u` format): understands `@@ -a,b +c,d @@` headers, context
// lines, `+` (add) and `-` (del). Ignores metadata (`diff --git`, `index `,
// `--- `/`+++ `) and the `\ No newline at end of file` marker — none of them
// is numbered or shown as a context line. Content lines
// are only processed after seeing a `@@` hunk header; anything
// before that (or unrecognized metadata) is discarded.
// Returns an array of {oldNo, newNo, type, text} ready for <DiffBlock>.
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
