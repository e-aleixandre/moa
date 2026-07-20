import { useState, useEffect, useRef } from "preact/hooks";
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
  ChevronDown,
} from "lucide-preact";
import { liveVerb, formatElapsed } from "../../data/util/activity.js";
import { StateDot } from "../../primitives/index.js";
import { useTailWindow } from "./tail-dwell.js";
import "./ActivityLedger.css";

// TOOL_ICONS — tool → lucide icon map, used by LedgerRow. `Wrench` is the
// fallback for tools not explicitly mapped.
const TOOL_ICONS = {
  read: FileText,
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
  return TOOL_ICONS[tool] || Wrench;
}

// LedgerRow — a single activity ledger row. Clickable to expand/collapse
// a mono `body` with the tool's output. Internal state by default
// (`defaultOpen`); can be controlled by passing `open`/`onToggle`.
export function LedgerRow({
  tool,
  arg,
  out,
  status = "ok",
  body,
  defaultOpen = false,
  open,
  onToggle,
}) {
  const [innerOpen, setInnerOpen] = useState(defaultOpen);
  const isControlled = open !== undefined;
  const isOpen = isControlled ? open : innerOpen;
  const hasBody = body != null && body !== "";

  function toggle() {
    if (!hasBody) return;
    if (onToggle) onToggle(!isOpen);
    if (!isControlled) setInnerOpen((v) => !v);
  }

  const Icon = toolIcon(tool);
  const argText = typeof arg === "object" && arg !== null ? arg.text : arg;
  const argDetail = typeof arg === "object" && arg !== null ? arg.detail : null;

  return (
    <>
      <button
        type="button"
        class={`ledger-row${isOpen ? " open" : ""}`}
        onClick={toggle}
        aria-expanded={hasBody ? isOpen : undefined}
        disabled={!hasBody}
      >
        <span class="t-ic" aria-hidden="true">
          <Icon size={14} />
        </span>
        <span class="t-name">{tool}</span>
        <span class="t-arg">
          {argText}
          {argDetail && <b> · {argDetail}</b>}
        </span>
        {out && <span class={`t-out ${status}`}>{out}</span>}
      </button>
      {hasBody && isOpen && (
        <div class="ledger-body">{body}</div>
      )}
    </>
  );
}

// useElapsed re-renders every second while `startedAt` is set, returning the
// elapsed ms since then. Used by TailLiveLine's timer — the timer only
// shows once elapsed >= 3000ms (RUNNING-TOOL-SPEC-FABLE.md §1).
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

// TailLiveLine — the live line of the "B·Tail" view: ▸ + verb + object +
// breathing dot + elapsed timer (from 3s). The dot (blue, breathing at a
// slower tempo than the assistant caret's blink) marks the row as alive
// without cloning the text caret. When the running tool streams output
// (`liveTail` — bash only, in practice) a mini-logtail of the last lines
// unfolds below, matching the async BackgroundJob's live tail.
function TailLiveLine({ row }) {
  const elapsed = useElapsed(row.startedAt);
  const verb = liveVerb(row.tool);
  const argText = typeof row.arg === "object" && row.arg !== null ? row.arg.text : row.arg;
  const argDetail = typeof row.arg === "object" && row.arg !== null ? row.arg.detail : null;
  const tailLines = row.liveTail ? row.liveTail.split("\n") : [];
  return (
    <div class="tail-live">
      <div class="tail-line live" role="status" aria-live="off">
        <span class="mk" aria-hidden="true">▸</span>
        <span class="txt">
          <span class="verb">{verb}</span> {argText}
          {argDetail && <span class="dim"> {argDetail}</span>}
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

// TailDoneLine — one of the last terminated rows shown above the live line:
// ✓ ok (green) / ! rejected (yellow) / ✗ error (red) + tool + object (gray) +
// short result on the right.
function TailDoneLine({ row }) {
  const argText = typeof row.arg === "object" && row.arg !== null ? row.arg.text : row.arg;
  const kind = row.status === "err" ? "err" : row.status === "warn" ? "warn" : "ok";
  const glyph = kind === "err" ? "✗" : kind === "warn" ? "!" : "✓";
  return (
    <div class={`tail-line${row._folding ? " folding" : ""}`}>
      <span class={`mk ${kind}`} aria-hidden="true">{glyph}</span>
      <span class="txt"><b>{row.tool}</b> {argText}</span>
      {row.out && <span class="res">{row.out}</span>}
    </div>
  );
}

// TailView — the "B·Tail" compact rendering of a ledger batch (console tail):
// a folded "· N earlier actions" header (only when there are more than the
// visible slots), the last `visibleDone` terminated rows, and the live row (if
// any). Reused by both ActivityLedger (desktop, visibleDone=2) and
// MobileLedger (visibleDone=1). Tapping the header hands control back to the
// caller via `onExpand`, which should render the full row list instead.
export function TailView({ rows, visibleDone = 2, onExpand, className = "", ...rest }) {
  const liveRow = rows.length > 0 && rows[rows.length - 1].live ? rows[rows.length - 1] : null;
  const doneRows = liveRow ? rows.slice(0, -1) : rows;
  const target = doneRows.slice(-visibleDone);
  // Hold just-superseded lines for a minimum dwell so a burst of fast tools
  // can't flash a line away before it can be read.
  const visible = useTailWindow(target);
  const visibleIds = new Set(visible.map((r) => r.id));
  const earlier = doneRows.filter((r) => !visibleIds.has(r.id));
  const earlierErrors = earlier.filter((r) => r.status === "err").length;

  return (
    <div class={`tail ${className}`.trim()} {...rest}>
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
      {liveRow && <TailLiveLine key={liveRow.id ?? "live"} row={liveRow} />}
    </div>
  );
}

// rowKey derives a stable Preact key for a ledger row. See the header note
// below: the consumer SHOULD pass a stable `row.key`/`row.id`.
function rowKey(row, i) {
  if (row.key != null) return row.key;
  if (row.id != null) return row.id;
  const argText = typeof row.arg === "object" && row.arg !== null ? row.arg.text : row.arg;
  return `${row.tool ?? "row"}:${argText ?? ""}:${i}`;
}

// ActivityLedger — container for collapsible tool-call rows.
// `rows` is an array of props for LedgerRow. Each row's open/closed state
// lives in LedgerRow (useState) and is tied to the row via `key`;
// using the index as key would make inserting/reordering rows reassign
// the expansion state to the wrong row. The consumer SHOULD pass a
// stable `row.key`/`row.id` (e.g. the tool call's id). If it doesn't,
// we derive a best-effort key from the content — not as robust as a real
// id, but better than a plain index.
//
// TAIL VIEW: when the batch is still live (its last row has `live:true`) OR
// has more than 3 rows, it renders as the compact "B·Tail" console-tail view
// (TailView above: folded "N earlier actions" header + last 2 terminated rows
// + the live row) instead of the full row list. Tapping the header expands to
// the full list (this component's own `expanded` state) — the everyday
// LedgerRow list, unchanged. A short/finished batch (≤3 rows, nothing live)
// renders as the plain list directly: no header needed.
export function ActivityLedger({ rows = [], children, className = "", ...rest }) {
  const [expanded, setExpanded] = useState(false);
  if (rows.length === 0) {
    return (
      <div class={`ledger ${className}`.trim()} {...rest}>
        {children}
      </div>
    );
  }

  const isLive = rows[rows.length - 1].live === true;
  const showTail = !expanded && (isLive || rows.length > 3);

  if (showTail) {
    // The tail view is its own self-contained block (border/background per
    // mockup, `.tail` in ActivityLedger.css) — it does NOT nest inside the
    // `.ledger` list container (which has its own border), avoiding a
    // double-border box.
    return <TailView rows={rows} visibleDone={2} onExpand={() => setExpanded(true)} {...rest} />;
  }

  return (
    <div class={`ledger ${className}`.trim()} {...rest}>
      {rows.map((row, i) =>
        row.live ? (
          <TailLiveLine key={rowKey(row, i)} row={row} />
        ) : (
          <LedgerRow key={rowKey(row, i)} {...row} />
        ),
      )}
    </div>
  );
}
