import { DiffBlock } from "../../components/DiffBlock/DiffBlock.jsx";
import { CodeBlock } from "../../components/CodeBlock/CodeBlock.jsx";
import { AskUserDetail } from "../../components/AskUserCard/AskUserDetail.jsx";
import { mapToolToKind } from "../util/tool-kind.js";

function inputDetailNode(inputText, output, prompt = null) {
  return (
    <>
      <div className={`doc-mono ${prompt ? "tg-cmd" : "tg-input"}`}>
        {prompt ? <span className="tg-cmd-prompt" aria-hidden="true">{prompt}</span> : null}
        {inputText}
      </div>
      {output && <div className="tg-detail-divider" />}
      {output}
    </>
  );
}

// fuseLedgerDetails — attach inline detail nodes to a projectStream ledger's
// rows so they open INSIDE the unified tool-group card (no nested card): the
// edit's unified `diff` sibling (projectStream emits it right after the ledger)
// fuses into that ledger's LAST edit row. Supported tool input lines precede
// their output; other rows carrying a text `body` get an output detail. Bash
// commands precede their output in the same panel. Diffs/outputs
// render BORDERLESS (className="flush") since the .tg-detail panel is the only
// surface. Shared by the desktop Stream and mobile MobileStream so both fuse
// identically (parity). Returns rows, each possibly with a `detail:{node}`.
export function fuseLedgerDetails(rows, siblingDiff) {
  let diffRowIndex = -1;
  if (siblingDiff) {
    for (let i = rows.length - 1; i >= 0; i--) {
      if (mapToolToKind(rows[i].tool) === "edit") { diffRowIndex = i; break; }
    }
  }
  return rows.map((row, i) => {
    if (row.live) return row; // the live row never carries a static detail
    const output = i === diffRowIndex
      ? <DiffBlock className="flush" diffText={siblingDiff.diffText} filename={siblingDiff.filename} />
      : row.body
        ? <CodeBlock className="flush" code={row.body} lang="bash" showHeader={false} />
        : null;
    if (row.askUser) {
      return {
        ...row,
        detail: { node: <AskUserDetail questions={row.askUser.questions} result={row.askUser.result} /> },
      };
    }
    if (row.command) {
      return {
        ...row,
        detail: { node: inputDetailNode(row.command, output, "$ ") },
      };
    }
    if (row.inputLine) {
      return {
        ...row,
        detail: { node: inputDetailNode(row.inputLine, output) },
      };
    }
    if (output) {
      return {
        ...row,
        detail: { node: output },
      };
    }
    return row;
  });
}
