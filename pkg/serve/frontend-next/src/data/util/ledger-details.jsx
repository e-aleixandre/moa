import { DiffBlock } from "../../components/DiffBlock/DiffBlock.jsx";
import { CodeBlock } from "../../components/CodeBlock/CodeBlock.jsx";
import { mapToolToKind } from "../util/tool-kind.js";

// fuseLedgerDetails — attach inline detail nodes to a projectStream ledger's
// rows so they open INSIDE the unified tool-group card (no nested card): the
// edit's unified `diff` sibling (projectStream emits it right after the ledger)
// fuses into that ledger's LAST edit row, and any other row carrying a text
// `body` (bash output / result preview) gets an output detail. Diffs/outputs
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
    if (i === diffRowIndex) {
      return {
        ...row,
        detail: {
          node: (
            <DiffBlock
              className="flush"
              diffText={siblingDiff.diffText}
              filename={siblingDiff.filename}
            />
          ),
        },
      };
    }
    if (row.body) {
      return {
        ...row,
        detail: { node: <CodeBlock className="flush" code={row.body} lang="bash" showHeader={false} /> },
      };
    }
    return row;
  });
}
