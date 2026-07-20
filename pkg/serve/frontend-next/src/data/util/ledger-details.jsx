import { DiffBlock } from "../../components/DiffBlock/DiffBlock.jsx";
import { CodeBlock } from "../../components/CodeBlock/CodeBlock.jsx";
import { mapToolToKind } from "../util/tool-kind.js";

// fuseLedgerDetails — attach inline detail nodes to a projectStream ledger's
// rows so they open INSIDE the unified tool-group card (no nested card): the
// edit's unified `diff` sibling (projectStream emits it right after the ledger)
// fuses into that ledger's LAST edit row, and any other row carrying a text
// `body` (bash output / result preview) gets an output detail. Bash commands
// precede their output in the same panel. Diffs/outputs
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
    if (row.command) {
      // A dedicated block preserves the shell prompt and input/output contrast
      // that a syntax-highlighted CodeBlock cannot provide.
      return {
        ...row,
        detail: {
          node: (
            <>
              <div className="doc-mono tg-cmd">
                <span className="tg-cmd-prompt" aria-hidden="true">$ </span>
                {row.command}
              </div>
              {output && <div className="tg-detail-divider" />}
              {output}
            </>
          ),
        },
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
