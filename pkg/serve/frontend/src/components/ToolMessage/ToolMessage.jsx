// ToolMessage — the opened detail of a ledger row that sent text to another
// agent: where it went, the whole message as the markdown it was written in,
// and (below a divider, like any other output) what the tool answered.
import { renderMarkdown } from "../../data/util/markdown.js";
import { CodeBlock } from "../CodeBlock/CodeBlock.jsx";
import "./ToolMessage.css";

export function ToolMessage({ target, text, result }) {
  return (
    <>
      <div class="tmsg-to doc-mono">→ {target}</div>
      <div class="tmsg-body zl-prose" dangerouslySetInnerHTML={{ __html: renderMarkdown(text) }} />
      {result && <div class="zl-lg-divider" />}
      {result && <CodeBlock className="flush" code={result} lang="bash" showHeader={false} />}
    </>
  );
}
