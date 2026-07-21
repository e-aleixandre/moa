import { useState } from "preact/hooks";
import {
  FileText,
  Search,
  Terminal,
  Pencil,
  FilePlus,
  Globe,
  Database,
  ListTodo,
  Wrench,
  ChevronRight,
  Check,
  X,
  AlertTriangle,
} from "lucide-preact";
import { liveVerb, formatElapsed } from "../../data/util/activity.js";
import { StateDot } from "../../primitives/index.js";
import { DiffBlock } from "../DiffBlock/DiffBlock.jsx";
import { useElapsed } from "../../data/util/use-elapsed.js";
import { useTailWindow } from "./tail-dwell.js";
import "./ActivityLedger.css";

// TOOL_ICONS — tool → lucide icon (Variant B: the left column is the KIND of
// action). `Wrench` is the fallback for unmapped tools.
const TOOL_ICONS = {
  read: FileText,
  ls: FileText,
  grep: Search,
  find: Search,
  bash: Terminal,
  edit: Pencil,
  multiedit: Pencil,
  write: FilePlus,
  fetch: Globe,
  fetch_content: Globe,
  db: Database,
  task: ListTodo,
  tasks: ListTodo,
};

function toolIcon(tool) {
  return TOOL_ICONS[(tool || "").toLowerCase()] || Wrench;
}

// argParts splits a row's `arg` into its display text + optional dim detail.
function argParts(arg) {
  if (arg && typeof arg === "object") return { text: arg.text, detail: arg.detail };
  return { text: arg, detail: null };
}

// StatusMark — the small outcome mark on the RIGHT of a done row (Variant B):
// ✓ ok (green) / ✗ error (red) / ! rejected (yellow). Running rows have none.
// The glyph is decorative; the outcome is named for screen readers via an
// SR-only label (an "232 lines"/"exit 1" result doesn't always convey it).
function StatusMark({ status }) {
  const kind = status === "err" ? "err" : status === "warn" ? "warn" : "ok";
  const Icon = kind === "err" ? X : kind === "warn" ? AlertTriangle : Check;
  const label = kind === "err" ? "failed" : kind === "warn" ? "rejected" : "completed";
  return (
    <span class={`mark ${kind}`}>
      <Icon size={11} aria-hidden="true" />
      <span class="sr-only">{label}</span>
    </span>
  );
}

// DoneRow — a terminated tool call. The SAME atom as the live/header rows,
// just frozen: tool icon, bold tool + object, short result, outcome mark. If it
// carries a `detail` (diff / output body) it's a <button> that toggles a
// recessed `.tg-detail` panel INSIDE the card (no nested card); otherwise it's
// a plain inert <div> (nothing to open — not a disabled button, which SRs would
// announce as an unavailable action).
function DoneRow({ row }) {
  const [open, setOpen] = useState(false);
  const { text, detail: argDetail } = argParts(row.arg);
  const Icon = toolIcon(row.tool);
  const hasDetail = row.detail != null;
  const Tag = hasDetail ? "button" : "div";

  return (
    <>
      <Tag
        type={hasDetail ? "button" : undefined}
        class={`tg-row${open ? " open" : ""}${row._folding ? " folding" : ""}`}
        onClick={hasDetail ? () => setOpen((v) => !v) : undefined}
        aria-expanded={hasDetail ? open : undefined}
      >
        <span class="ic" aria-hidden="true">
          <Icon size={14} />
        </span>
        <span class="txt">
          <b>{row.tool}</b> {text}
          {argDetail && <span class="dim"> · {argDetail}</span>}
        </span>
        {row.out && (
          <span class={`res ${row.status === "err" ? "err" : row.status === "ok" ? "ok" : ""}`.trim()}>
            {row.out}
          </span>
        )}
        <StatusMark status={row.status} />
        {hasDetail && (
          <span class="chev" aria-hidden="true">
            <ChevronRight size={12} />
          </span>
        )}
      </Tag>
      {hasDetail && open && <RowDetail detail={row.detail} />}
    </>
  );
}

// RowDetail — the recessed panel a row opens INSIDE the card. Diffs/outputs
// render BORDERLESS (className="flush") so the .tg-detail panel is the only
// surface — the fix for the "card inside a card" ugliness.
function RowDetail({ detail }) {
  return (
    <div class="tg-detail">
      {detail.node}
    </div>
  );
}

// LiveRow — the running tool call: the SAME atom, tinted blue. Breathing dot in
// the icon column, blue verb + bright object, elapsed (from 3s), a 1px progress
// sweep. A running bash streams its last lines in a `.tg-log` panel below.
function LiveRow({ row }) {
  const elapsed = useElapsed(row.startedAt);
  const verb = liveVerb(row.tool);
  const { text, detail: argDetail } = argParts(row.arg);
  const tailLines = row.liveTail ? row.liveTail.split("\n") : [];
  const tailStart = row.liveTailStart || 0;
  const livePreview = row.livePreview;
  return (
    <>
      <div class="tg-row live" role="status" aria-live="off">
        <span class="ic" aria-hidden="true">
          <StateDot state="running" size={6} />
        </span>
        <span class="txt">
          <span class="verb">{verb}</span> {text}
          {argDetail && <span class="dim"> {argDetail}</span>}
        </span>
        {elapsed >= 3000 && <span class="res">{formatElapsed(elapsed)}</span>}
        <span class="hair" aria-hidden="true" />
      </div>
      {livePreview && (
        <div class={`tg-live-preview ${livePreview.kind === "diff" ? "diff" : "input"}`}>
          {livePreview.kind === "diff" ? (
            <DiffBlock className="flush" diffText={livePreview.text} filename={text} />
          ) : (
            <pre>{livePreview.text}</pre>
          )}
          <span class="tg-live-cursor" aria-hidden="true" />
        </div>
      )}
      {tailLines.length > 0 && (
        <div class={`tg-log${tailStart > 0 ? " fade" : ""}`} role="log" aria-live="off">
          {tailLines.map((line, i) => (
            <div key={tailStart + i} class="ln">
              {line}
              {i === tailLines.length - 1 && <span class="ln-cursor" aria-hidden="true" />}
            </div>
          ))}
        </div>
      )}
    </>
  );
}

// FoldHeader — the dim header row that appears when the card hides rows. The
// SAME atom. Collapsed: "· N earlier actions" (+ "· K errors" red). Expanded:
// the textual summary ("7 actions · 3 reads · 2 greps · 1 bash"). Tapping it
// toggles the card between collapsed and expanded.
function FoldHeader({ expanded, earlierCount, earlierErrors, summary, onToggle }) {
  return (
    <button
      type="button"
      class={`tg-row tg-fold${expanded ? " open" : ""}`}
      aria-expanded={expanded}
      onClick={onToggle}
    >
      <span class="chev" aria-hidden="true">
        <ChevronRight size={12} />
      </span>
      <span class="txt">
        {expanded ? summary : `· ${earlierCount} earlier action${earlierCount === 1 ? "" : "s"}`}
      </span>
      {!expanded && earlierErrors > 0 && (
        <span class="err-n">· {earlierErrors} error{earlierErrors === 1 ? "" : "s"}</span>
      )}
    </button>
  );
}

// pluralizeTool renders "N <tool>" with the right plural. Most tools read fine
// with a trailing "s" ("3 reads", "2 greps", "1 edit"), matching the mockup;
// a few don't take one as a countable noun ("3 bash", "2 ls") or are already
// plural ("2 tasks"), so they stay invariant.
const INVARIANT_TOOLS = new Set(["bash", "ls", "tasks"]);

function pluralizeTool(tool, n) {
  if (n === 1 || INVARIANT_TOOLS.has(tool)) return tool;
  return `${tool}s`;
}

// summarizeRows builds the expanded header's textual summary, grouped by tool
// kind in first-appearance order: "7 actions · 3 reads · 2 greps · 1 bash".
function summarizeRows(rows) {
  const order = [];
  const counts = {};
  for (const r of rows) {
    const k = (r.tool || "tool").toLowerCase();
    if (!(k in counts)) { counts[k] = 0; order.push(k); }
    counts[k]++;
  }
  const parts = order.map((k) => `${counts[k]} ${pluralizeTool(k, counts[k])}`);
  const total = rows.length;
  return `${total} action${total === 1 ? "" : "s"} · ${parts.join(" · ")}`;
}

// rowKey derives a stable Preact key for a row (consumer SHOULD pass row.id).
function rowKey(row, i) {
  if (row.id != null) return row.id;
  const { text } = argParts(row.arg);
  return `${row.tool ?? "row"}:${text ?? ""}:${i}`;
}

// FOLD_THRESHOLD — a batch folds (oldest rows collapse into the "N earlier
// actions" header) only when it has MORE than this many rows total, live or
// not (mockup: "above ~3 rows the oldest fold"). `visibleDone` is a separate
// knob: how many terminated rows the ALREADY-collapsed tail keeps visible
// (desktop 2 / mobile 1) — it does NOT decide when to fold.
const FOLD_THRESHOLD = 3;

// ActivityLedger — the unified tool-group card (.tg). ONE shape across every
// phase (TOOLCALLS-UNIFIED-IMPL-SPEC): running/collapsed/expanded/finished are
// the same card and the same row atom, differing only by which rows show and a
// `.live` modifier. `rows` is the projectStream ledger's rows (each
// `{ tool, arg, out, status, id, body?, live?, startedAt?, livePreview?, liveTail?, liveTailStart?, detail? }`,
// `detail` a fused diff/output node attached by the caller).
//
// FOLD: a batch of more than FOLD_THRESHOLD rows collapses its oldest done rows
// into a dim header ("N earlier actions"); tapping it expands to the full list.
// A short batch (≤3 rows) renders as the plain list — no header — whether it's
// live or finished. The card is a SINGLE component (never swapped): the dwell
// hook (useTailWindow) is always mounted and simply gets a smaller `target`
// when the batch crosses the fold threshold, so the row that folds away
// animates out instead of being dropped in one frame.
export function ActivityLedger({ rows = [], children, visibleDone = 2, className = "", ...rest }) {
  const [expanded, setExpanded] = useState(false);

  const isLive = rows.length > 0 && rows[rows.length - 1].live === true;
  const liveRow = isLive ? rows[rows.length - 1] : null;
  const doneRows = isLive ? rows.slice(0, -1) : rows;

  const foldable = rows.length > FOLD_THRESHOLD;
  const folded = foldable && !expanded;

  // The dwell hook is ALWAYS mounted (Rules of Hooks + no remount at the fold
  // threshold): its `target` is the last `visibleDone` done rows when folded,
  // or all of them otherwise. Shrinking the target on the 3→4 crossing makes
  // the newly-hidden row fold out with animation rather than vanish.
  const target = folded ? doneRows.slice(-visibleDone) : doneRows;
  const visible = useTailWindow(target);
  // The header count reflects the LOGICAL fold (rows not in `target`), NOT the
  // dwell-expanded `visible` set — during the fold-out animation a row still
  // lingers in `visible`, and counting off that would keep the header at 0 and
  // then pop it in abruptly once the animation ends. `target` is exact.
  const targetIds = new Set(target.map((r) => r.id));
  const earlier = folded ? doneRows.filter((r) => !targetIds.has(r.id)) : [];
  const earlierErrors = earlier.filter((r) => r.status === "err").length;

  // Empty ledger: a bare card wrapping arbitrary children (used by specimens).
  if (rows.length === 0) {
    return (
      <div class={`tg ${className}`.trim()} {...rest}>
        {children}
      </div>
    );
  }

  return (
    <div class={`tg ${className}`.trim()} {...rest}>
      {folded && earlier.length > 0 && (
        <FoldHeader
          expanded={false}
          earlierCount={earlier.length}
          earlierErrors={earlierErrors}
          onToggle={() => setExpanded(true)}
        />
      )}
      {expanded && foldable && (
        <FoldHeader
          expanded
          summary={summarizeRows(doneRows.concat(liveRow ? [liveRow] : []))}
          onToggle={() => setExpanded(false)}
        />
      )}
      {visible.map((row, i) => (
        <DoneRow key={rowKey(row, i)} row={row} />
      ))}
      {liveRow && <LiveRow key={rowKey(liveRow, doneRows.length)} row={liveRow} />}
    </div>
  );
}
