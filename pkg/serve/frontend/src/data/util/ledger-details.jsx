import { useEffect, useState } from "preact/hooks";
import { DiffBlock } from "../../components/DiffBlock/DiffBlock.jsx";
import { CodeBlock } from "../../components/CodeBlock/CodeBlock.jsx";
import { AskUserDetail } from "../../components/AskUserCard/AskUserDetail.jsx";
import { api } from "../api.js";
import { toolPreview } from "../util/format.js";
import { mapToolToKind } from "../util/tool-kind.js";

export function projectedToolDetailNode(tool, path, detail) {
  const preview = toolPreview(tool, detail?.args, detail?.output, 'done');
  if (!preview?.text) return <div className="doc-mono tg-input">No output</div>;
  if (preview.kind === 'diff') {
    return <DiffBlock className="flush" diffText={preview.text} filename={path || detail?.args?.path || ''} />;
  }
  return <CodeBlock className="flush" code={preview.text} lang="bash" showHeader={false} />;
}

export function ProjectedToolDetail({ url, tool, path }) {
  const [detail, setDetail] = useState(null);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    let active = true;
    api('GET', url).then((value) => {
      if (active) setDetail(value || {});
    }).catch(() => {
      if (active) setFailed(true);
    });
    return () => { active = false; };
  }, [url]);
  if (failed) return <div className="doc-mono tg-input">Could not load output</div>;
  if (!detail) return <div className="doc-mono tg-input">Loading…</div>;
  return projectedToolDetailNode(tool, path, detail);
}

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
// rows so they open INSIDE the unified tool-group card (no nested card).
// Consecutive `diff` siblings fuse onto edit rows in order (one diff → last
// edit, for older single-sibling callers). Supported tool input lines precede
// their output; other rows carrying a text `body` get an output detail. Bash
// commands precede their output in the same panel. Diffs/outputs
// render BORDERLESS (className="flush") since the .tg-detail panel is the only
// surface. Shared by the desktop Stream and mobile MobileStream so both fuse
// identically (parity). Returns rows, each possibly with a `detail:{node}`.
export function fuseLedgerDetails(rows, siblingDiff) {
  const diffs = Array.isArray(siblingDiff) ? siblingDiff.filter(Boolean) : (siblingDiff ? [siblingDiff] : []);
  const editIndexes = [];
  for (let i = 0; i < rows.length; i++) {
    if (mapToolToKind(rows[i].tool) === "edit") editIndexes.push(i);
  }
  const diffByRow = new Map();
  if (diffs.length === 1 && editIndexes.length > 0) {
    diffByRow.set(editIndexes[editIndexes.length - 1], diffs[0]);
  } else {
    const start = Math.max(0, editIndexes.length - diffs.length);
    diffs.forEach((diff, k) => {
      const idx = editIndexes[start + k];
      if (idx != null) diffByRow.set(idx, diff);
    });
  }
  return rows.map((row, i) => {
    if (row.live) return row; // the live row never carries a static detail
    const fused = diffByRow.get(i);
    const lazyOutput = row.detailUrl
      ? <ProjectedToolDetail url={row.detailUrl} tool={row.tool} path={row.arg?.text || ''} />
      : null;
    const output = fused
      ? <DiffBlock className="flush" diffText={fused.diffText} filename={fused.filename} />
      : row.body
        ? <CodeBlock className="flush" code={row.body} lang="bash" showHeader={false} />
        : lazyOutput;
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
