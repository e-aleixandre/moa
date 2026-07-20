import { useState, useEffect } from "preact/hooks";
import {
  ChevronDown,
  ChevronRight,
  FileText,
  Search,
  Pencil,
  Terminal,
  Check,
} from "lucide-preact";
import { CodeBlock, DiffBlock } from "../index.js";
import { liveVerb, formatElapsed } from "../../data/util/activity.js";
import { StateDot } from "../../primitives/index.js";
import { useTailWindow } from "../ActivityLedger/tail-dwell.js";
import "../ActivityLedger/ActivityLedger.css";
import "./MobileLedger.css";

// MobileLedger — 3-level touch ledger for the mobile stream:
//   L1 (summary)  → one line with icons + summary + chevron; tap to open.
//   L2 (rows)     → concrete read/edit/bash rows with a result; tap a row.
//   L3 (detail)   → inline panel per detail.type (files | diff | bash) +
//                   actions row. Diffs/bash delegate to DiffBlock/CodeBlock.
//
// Props:
//   summary  string   — summary text for the L1 line.
//   icons    node[]?  — lucide icons to the left of the summary.
//   defaultOpen        boolean? — L1 starts expanded (still toggleable).
//   defaultOpenRowIds  string[]? — ids of L2 rows that start expanded.
//   rows     [{ id, kind:"read"|"edit"|"bash", name, action, result, detail }]
//     detail:
//       { type:"files", files:[{ id?, name, lines }], actions?:[{id?,label}]|string[] }
//       { type:"diff",  diffText, filename?, actions?:... }
//       { type:"bash",  output, actions?:... }
//   liveRow  { id, tool, arg, startedAt }? — the tool call currently in
//     flight (from mobile-ledger-adapter.js#adaptLedger). When present, the
//     batch renders as the compact "B·Tail" view (1 terminated + live line)
//     instead of the normal L1 summary, regardless of `defaultOpen`.

// useElapsed re-renders every second while `startedAt` is set. Mirrors
// ActivityLedger.jsx's timer so both frontends share the same 3s threshold.
function useElapsed(startedAt) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!startedAt) return;
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, [startedAt]);
  if (!startedAt) return 0;
  return Math.max(0, now - startedAt);
}

// TailLiveLine — the live line of the "B·Tail" view (shares .tail-line.live
// styling with ActivityLedger.css, imported alongside MobileLedger.css). A
// breathing dot marks it alive; when the tool streams output (`liveTail`) a
// mini-logtail of the last lines unfolds below.
function TailLiveLine({ liveRow }) {
  const elapsed = useElapsed(liveRow.startedAt);
  const verb = liveVerb(liveRow.tool);
  const argText = typeof liveRow.arg === "object" && liveRow.arg !== null ? liveRow.arg.text : liveRow.arg;
  const tailLines = liveRow.liveTail ? liveRow.liveTail.split("\n") : [];
  return (
    <div class="tail-live">
      <div class="tail-line live" role="status" aria-live="off">
        <span class="mk" aria-hidden="true">▸</span>
        <span class="txt">
          <span class="verb">{verb}</span> {argText}
        </span>
        <StateDot state="running" size={6} />
        {elapsed >= 3000 && <span class="res">{formatElapsed(elapsed)}</span>}
      </div>
      {tailLines.length > 0 && (
        <div class="tail-logtail" role="log" aria-live="off">
          {tailLines.map((line, i) => (
            <div key={i} class="ln">
              {line}
              {i === tailLines.length - 1 && <span class="ln-cursor" aria-hidden="true" />}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

// TailDoneLine — the last terminated row shown above the live line.
function TailDoneLine({ row }) {
  const kind = row.status === "err" ? "err" : row.status === "warn" ? "warn" : "ok";
  const glyph = kind === "err" ? "✗" : kind === "warn" ? "!" : "✓";
  return (
    <div class={`tail-line${row._folding ? " folding" : ""}`}>
      <span class={`mk ${kind}`} aria-hidden="true">{glyph}</span>
      <span class="txt"><b>{row.name}</b> {row.action}</span>
      <span class="res">{row.result}</span>
    </div>
  );
}

function LedgerRow({ row, defaultOpen = false }) {
  const [open, setOpen] = useState(defaultOpen);
  const okIsCheck = row.result === "ok";
  return (
    <>
      <button
        type="button"
        class={`mledger-row${open ? " open" : ""}`}
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
      >
        <span class="n">{row.name}</span>
        <span class="a">{row.action}</span>
        <span class="ok">
          {okIsCheck ? (
            <>
              <Check size={11} aria-hidden="true" /> ok
            </>
          ) : (
            row.result
          )}
        </span>
        <span class="chev" aria-hidden="true">
          <ChevronRight size={11} />
        </span>
      </button>
      {open && <RowDetail detail={row.detail} />}
    </>
  );
}

function RowDetail({ detail }) {
  if (!detail) return null;
  return (
    <div class="mledger-detail">
      {detail.type === "files" && (
        <div class="mledger-files">
          {detail.files.map((f, i) => (
            <div key={f.id ?? f.name ?? i} class="mledger-file">
              <FileText size={12} aria-hidden="true" />
              <span class="fname">{f.name}</span>
              <span class="fln">{f.lines}</span>
            </div>
          ))}
        </div>
      )}
      {detail.type === "diff" && (
        <DiffBlock diffText={detail.diffText} filename={detail.filename} />
      )}
      {detail.type === "bash" && (
        <CodeBlock code={detail.output} lang="bash" showHeader={false} />
      )}
      {detail.actions && detail.actions.length > 0 && (
        <div class="mledger-actions">
          {detail.actions.map((a, i) => {
            const label = typeof a === "string" ? a : a.label;
            const key = typeof a === "string" ? a : a.id ?? a.label ?? i;
            return (
              <button key={key} type="button" class="mledger-action">
                {label}
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}

// MobileTailView — the B·Tail console-tail view for mobile. Extracted so the
// dwell hook (useTailWindow) runs unconditionally (Rules of Hooks): MobileLedger
// mounts/unmounts this whole component when switching to/from the tail view.
function MobileTailView({ rows, liveRow, onExpand }) {
  // Below the >3-calls threshold, show up to 2 terminated lines with no header
  // (nothing hidden). At/above it, compress to the single last terminated line
  // and fold the rest into the "· N earlier actions" header — keeps the mobile
  // batch to ~72px without ever dropping an action silently. (Spec: header only
  // with >3 calls; mobile note: 1 terminated + live when folded.)
  const total = rows.length + (liveRow ? 1 : 0);
  const folded = total > 3;
  const visibleDone = folded ? 1 : 2;
  const target = rows.slice(-visibleDone);
  // Hold just-superseded lines for a minimum dwell (shared with desktop) so
  // fast tool bursts can't flash a line away before it can be read.
  const visible = useTailWindow(target);
  const visibleIds = new Set(visible.map((r) => r.id));
  const earlier = rows.filter((r) => !visibleIds.has(r.id));
  const earlierErrors = earlier.filter((r) => r.status === "err").length;
  return (
    <div class="tail mledger-tail">
      {earlier.length > 0 && (
        <button type="button" class="tail-earlier" onClick={onExpand}>
          <ChevronDown size={11} aria-hidden="true" />
          <span>· {earlier.length} earlier action{earlier.length === 1 ? "" : "s"}</span>
          {earlierErrors > 0 && (
            <span class="tail-earlier-err">
              {" "}· {earlierErrors} error{earlierErrors === 1 ? "" : "s"}
            </span>
          )}
        </button>
      )}
      {visible.map((row, i) => (
        <TailDoneLine key={row.id ?? i} row={row} />
      ))}
      {liveRow && <TailLiveLine key={liveRow.id} liveRow={liveRow} />}
    </div>
  );
}

export function MobileLedger({
  summary,
  icons,
  rows = [],
  defaultOpen = false,
  defaultOpenRowIds = [],
  liveRow = null,
}) {
  const [open, setOpen] = useState(defaultOpen);
  const [expanded, setExpanded] = useState(false);

  // B·Tail: while a tool is live (or the batch has more rows than the
  // compact view shows), render the console-tail view — 1-2 terminated
  // rows + the live row — instead of the normal L1 summary. Tapping it opens
  // the full L1/L2 ledger below (RUNNING-TOOL-SPEC-FABLE.md §5, mobile note:
  // "1 terminated + live (~72px)").
  const showTail = !expanded && (liveRow != null || rows.length > 3);

  if (showTail) {
    return <MobileTailView rows={rows} liveRow={liveRow} onExpand={() => setExpanded(true)} />;
  }

  return (
    <div class={`mledger${open ? " open" : ""}`}>
      <button
        type="button"
        class="mledger-sum"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
      >
        {icons && (
          <span class="mledger-icons" aria-hidden="true">
            {icons}
          </span>
        )}
        <span class="cnt">{summary}</span>
        <span class="mledger-chev" aria-hidden="true">
          <ChevronDown size={14} />
        </span>
      </button>
      {open && (
        <div class="mledger-rows">
          {rows.map((row, i) => (
            <LedgerRow
              key={row.id ?? i}
              row={row}
              defaultOpen={row.id != null && defaultOpenRowIds.includes(row.id)}
            />
          ))}
        </div>
      )}
    </div>
  );
}

// Conventional icons re-exported so the consumer can assemble the L1
// icon set without importing lucide directly (read-phase = FileText + Search, etc.).
export const LedgerIcons = { FileText, Search, Pencil, Terminal };
