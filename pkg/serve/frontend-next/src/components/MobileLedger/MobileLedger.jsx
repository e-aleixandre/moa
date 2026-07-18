import { useState } from "preact/hooks";
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

export function MobileLedger({ summary, icons, rows = [], defaultOpen = false, defaultOpenRowIds = [] }) {
  const [open, setOpen] = useState(defaultOpen);
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
