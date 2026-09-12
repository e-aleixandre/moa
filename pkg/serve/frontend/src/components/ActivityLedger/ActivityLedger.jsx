import { useEffect, useRef, useState } from "preact/hooks";
import { formatElapsed } from "../../data/util/activity.js";
import { useElapsed } from "../../data/util/use-elapsed.js";
import { useTailWindow } from "./tail-dwell.js";
import "./ActivityLedger.css";

// ActivityLedger — a batch of tool calls. Markup and CSS are the catalogue's
// (catalog/zones-lab.jsx `Ledger` / `LedgerRow`, zones-lab.css `.zl-ledger` /
// `.zl-lg-*`), MOVED here rather than imitated. The class names travelled
// with the rules, so the sheet IS the accepted design instead of a translation
// of it. The catalogue imports this component now, which is what makes one
// definition rather than two.
//
// What is NOT the catalogue's is everything the prototype never had, grafted
// on top: the live output window, the full-command panel on a running bash,
// the untruncated tooltip, the dwell-fold animation, rejected/warn marks,
// lazy-loaded bodies, and the elapsed timer from a real start timestamp.

const ICONS = {
  read: <path d="M3.5 2.5h6l3 3v8h-9z M9.5 2.5v3h3" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round" />,
  ls: <path d="M3.5 2.5h6l3 3v8h-9z M9.5 2.5v3h3" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round" />,
  grep: <><circle cx="7" cy="7" r="4" fill="none" stroke="currentColor" stroke-width="1.5" /><path d="M10 10l3.5 3.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" /></>,
  find: <><circle cx="7" cy="7" r="4" fill="none" stroke="currentColor" stroke-width="1.5" /><path d="M10 10l3.5 3.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" /></>,
  bash: <path d="M3 4l4 4-4 4M8.5 12H13" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />,
  edit: <path d="M11.5 2.5l2 2L6 12H4v-2z" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round" />,
  multiedit: <path d="M11.5 2.5l2 2L6 12H4v-2z" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round" />,
  write: <path d="M3.5 2.5h6l3 3v8h-9z M8 7v4M6 9h4" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round" />,
  fetch: <><circle cx="8" cy="8" r="5.5" fill="none" stroke="currentColor" stroke-width="1.4" /><path d="M2.5 8h11M8 2.5c1.6 1.8 2.4 3.6 2.4 5.5S9.6 12.2 8 13.5C6.4 12.2 5.6 10 5.6 8S6.4 4.3 8 2.5z" fill="none" stroke="currentColor" stroke-width="1.4" /></>,
  fetch_content: <><circle cx="8" cy="8" r="5.5" fill="none" stroke="currentColor" stroke-width="1.4" /><path d="M2.5 8h11M8 2.5c1.6 1.8 2.4 3.6 2.4 5.5S9.6 12.2 8 13.5C6.4 12.2 5.6 10 5.6 8S6.4 4.3 8 2.5z" fill="none" stroke="currentColor" stroke-width="1.4" /></>,
  tasks: <path d="M3 4.5l1.5 1.5 3-3M3 10.5l1.5 1.5 3-3M9.5 5h3.5M9.5 11H13" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />,
  task: <path d="M3 4.5l1.5 1.5 3-3M3 10.5l1.5 1.5 3-3M9.5 5h3.5M9.5 11H13" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />,
  mcp: <path d="M5 2v3M11 2v3M3.5 5h9v3a4.5 4.5 0 0 1-9 0zM8 12.5V15" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />,
  ask: <><circle cx="8" cy="8" r="6" fill="none" stroke="currentColor" stroke-width="1.4" /><path d="M6.4 6.2a1.7 1.7 0 1 1 2 2v1.1" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" /><circle cx="8.4" cy="11.4" r="0.75" fill="currentColor" /></>,
  ask_user: <><circle cx="8" cy="8" r="6" fill="none" stroke="currentColor" stroke-width="1.4" /><path d="M6.4 6.2a1.7 1.7 0 1 1 2 2v1.1" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" /><circle cx="8.4" cy="11.4" r="0.75" fill="currentColor" /></>,
  agent: <><circle cx="8" cy="5.5" r="2.5" fill="none" stroke="currentColor" stroke-width="1.4" /><path d="M3 13.5c.6-2.6 2.5-4 5-4s4.4 1.4 5 4" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" /></>,
  subagent: <><circle cx="8" cy="5.5" r="2.5" fill="none" stroke="currentColor" stroke-width="1.4" /><path d="M3 13.5c.6-2.6 2.5-4 5-4s4.4 1.4 5 4" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" /></>,
  tool: <path d="M10.8 2.6a3.4 3.4 0 0 0-4 4.4L3 10.8a1.5 1.5 0 0 0 2.1 2.1L8.9 9.2a3.4 3.4 0 0 0 4.4-4l-2 2-1.6-1.6z" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round" />,
};

function iconKind(tool) {
  const name = (tool || "").toLowerCase();
  if (ICONS[name]) return name;
  if (name.startsWith("mcp__")) return "mcp";
  return "tool";
}

function ToolIcon({ tool }) {
  return <svg class="zl-tool-ico" viewBox="0 0 16 16" aria-hidden="true">{ICONS[iconKind(tool)]}</svg>;
}

function Chevron({ open }) {
  return (
    <svg class={`zl-lg-chev${open ? " is-open" : ""}`} viewBox="0 0 12 12" aria-hidden="true">
      <path d="M4 2.5L7.5 6 4 9.5" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" />
    </svg>
  );
}

function argParts(arg) {
  if (arg && typeof arg === "object") return { text: arg.text, detail: arg.detail };
  return { text: arg, detail: null };
}

export function fullLabel(row, text) {
  const value = row.command || (typeof text === "string" ? text : "");
  return value || undefined;
}

function detailNode(detail) {
  if (detail == null) return null;
  if (typeof detail === "object" && detail.node != null) return detail.node;
  return detail;
}

const SR = { ok: "completed", err: "failed", warn: "rejected", live: "running" };

function StatusMark({ status, live }) {
  const kind = live ? "live" : status === "err" ? "err" : status === "warn" ? "warn" : "ok";
  return (
    <span class={`zl-lg-mark is-${kind}`} aria-hidden="true">
      {kind === "ok" && (
        <svg viewBox="0 0 12 12"><path d="M2.5 6.5l2.5 2.5 4.5-5" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" /></svg>
      )}
      {kind === "err" && (
        <svg viewBox="0 0 12 12"><path d="M3 3l6 6M9 3l-6 6" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" /></svg>
      )}
      {kind === "warn" && (
        <svg viewBox="0 0 12 12"><path d="M6 2.5v4" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" /><circle cx="6" cy="9" r="0.9" fill="currentColor" /></svg>
      )}
    </span>
  );
}

function DoneRow({ row }) {
  const [open, setOpen] = useState(false);
  const { text, detail: argDetail } = argParts(row.arg);
  const dim = argDetail || row.dim;
  const detail = detailNode(row.detail);
  const hasDetail = detail != null;
  const Tag = hasDetail ? "button" : "div";

  return (
    <>
      <Tag
        type={hasDetail ? "button" : undefined}
        class={`zl-lg-row${open ? " is-open" : ""}${row._folding ? " is-folding" : ""}`}
        onClick={hasDetail ? () => setOpen((v) => !v) : undefined}
        aria-expanded={hasDetail ? open : undefined}
      >
        <ToolIcon tool={row.tool} />
        <span class="zl-lg-txt" title={fullLabel(row, text)}>
          <span class="zl-lg-tool">{row.tool}</span>
          <span class="zl-lg-arg zl-data">{text}</span>
          {dim && <span class="zl-lg-dim"> · {dim}</span>}
        </span>
        {row.out && (
          <span class={`zl-lg-out zl-data${row.status === "err" ? " is-err" : ""}`}>{row.out}</span>
        )}
        <StatusMark status={row.status} />
        {hasDetail && <Chevron open={open} />}
        <span class="sr-only">{SR[row.status] || SR.ok}</span>
      </Tag>
      {hasDetail && open && <div class="zl-lg-detail">{detail}</div>}
    </>
  );
}

function LiveWindow({ lines, start = 0, diff = false, expanded, onToggle }) {
  const logRef = useRef(null);
  const stickToBottom = useRef(true);

  useEffect(() => {
    if (!expanded) {
      stickToBottom.current = true;
      return;
    }
    const log = logRef.current;
    if (log && stickToBottom.current) log.scrollTop = log.scrollHeight;
  }, [expanded, lines]);

  if (!lines || lines.length === 0) return null;
  return (
    <button
      ref={logRef}
      type="button"
      class={`zl-lg-log${diff ? " is-diff" : ""}${start > 0 && !expanded ? " is-fade" : ""}${expanded ? " is-expanded" : ""}`}
      aria-expanded={expanded}
      aria-label={expanded ? "Collapse live output" : "Show all live output"}
      onClick={onToggle}
      onScroll={(event) => {
        const log = event.currentTarget;
        stickToBottom.current = log.scrollHeight - log.scrollTop - log.clientHeight <= 40;
      }}
    >
      {lines.map((line, i) => {
        const type = diff ? line.type : "";
        const text = diff
          ? `${type === "add" ? "+" : type === "del" ? "-" : ""}${line.text}`
          : line;
        return (
          <span key={start + i} class={`zl-lg-ln${type ? ` is-${type}` : ""}`}>
            {text}
            {i === lines.length - 1 && <span class="zl-lg-cursor" aria-hidden="true" />}
          </span>
        );
      })}
      <span class="zl-lg-log-affordance" aria-hidden="true">
        <Chevron open={expanded} />
      </span>
    </button>
  );
}

function LiveCommand({ command }) {
  return (
    <div class="zl-lg-detail">
      <div class="doc-mono zl-lg-cmd">
        <span class="zl-lg-prompt" aria-hidden="true">$ </span>
        {command}
      </div>
    </div>
  );
}

function LiveRow({ row }) {
  const [expanded, setExpanded] = useState(false);
  const elapsedMs = useElapsed(row.startedAt);
  const elapsed = row.elapsed || (elapsedMs >= 3000 ? formatElapsed(elapsedMs) : null);
  const { text, detail: argDetail } = argParts(row.arg);
  const dim = argDetail || row.dim;
  const livePreview = row.livePreview;
  const liveWindow = livePreview
    ? { lines: livePreview.lines, start: livePreview.start, diff: livePreview.kind === "diff" }
    : row.liveTail
      ? { lines: row.liveTail.split("\n"), start: row.liveTailStart || 0 }
      : null;
  const fullWindow = row.liveFull
    ? { lines: row.liveFull.lines, start: row.liveFull.start || 0, diff: row.liveFull.kind === "diff" }
    : null;
  const displayedWindow = expanded && fullWindow ? fullWindow : liveWindow;
  const expandable = !!row.command;
  const Tag = expandable ? "button" : "div";
  return (
    <>
      <Tag
        type={expandable ? "button" : undefined}
        class={`zl-lg-row is-live${expanded && expandable ? " is-open" : ""}`}
        role={expandable ? undefined : "status"}
        aria-live={expandable ? undefined : "off"}
        onClick={expandable ? () => setExpanded((value) => !value) : undefined}
        aria-expanded={expandable ? expanded : undefined}
      >
        <ToolIcon tool={row.tool} />
        <span class="zl-lg-txt" title={fullLabel(row, text)}>
          <span class="zl-lg-tool">{row.tool}</span>
          <span class="zl-lg-arg zl-data">{text}</span>
          {dim && <span class="zl-lg-dim"> · {dim}</span>}
        </span>
        {elapsed && <span class="zl-lg-out zl-data">{elapsed}</span>}
        <StatusMark live />
        {expandable && <Chevron open={expanded} />}
        <span class="sr-only">{SR.live}</span>
      </Tag>
      {expandable && expanded && <LiveCommand command={row.command} />}
      {displayedWindow && (
        <LiveWindow
          {...displayedWindow}
          expanded={expanded && !!fullWindow}
          onToggle={() => setExpanded((value) => !value)}
        />
      )}
    </>
  );
}

const INVARIANT_TOOLS = new Set(["bash", "ls", "tasks", "grep", "write", "fetch_content", "ask_user"]);

function pluralizeTool(tool, n) {
  if (n === 1 || INVARIANT_TOOLS.has(tool)) return tool;
  return `${tool}s`;
}

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

function FoldHeader({ expanded, earlierCount, failed, summary, onToggle }) {
  return (
    <button type="button" class="zl-lg-head" onClick={onToggle} aria-expanded={expanded}>
      <Chevron open={expanded} />
      <span class="zl-lg-head-t">
        {expanded
          ? summary
          : <><span class="zl-data">{earlierCount}</span> earlier action{earlierCount === 1 ? "" : "s"}</>}
      </span>
      {failed > 0 && (
        <span class="zl-lg-head-fail"><span class="zl-data">{failed}</span> failed</span>
      )}
    </button>
  );
}

function rowKey(row, i) {
  if (row.id != null) return row.id;
  const { text } = argParts(row.arg);
  return `${row.tool ?? "row"}:${text ?? ""}:${i}`;
}

const FOLD_THRESHOLD = 3;

export function ActivityLedger({
  rows = [],
  children,
  visibleDone = 2,
  dense,
  folded: foldedInit,
  className = "",
  ...rest
}) {
  const [expanded, setExpanded] = useState(foldedInit === false);

  const isLive = rows.length > 0 && rows[rows.length - 1].live === true;
  const liveRow = isLive ? rows[rows.length - 1] : null;
  const doneRows = isLive ? rows.slice(0, -1) : rows;

  const foldable = rows.length > FOLD_THRESHOLD;
  const folded = foldable && !expanded;

  const target = folded ? doneRows.slice(-visibleDone) : doneRows;
  const visible = useTailWindow(target);
  const targetIds = new Set(target.map((r) => r.id));
  const earlier = folded ? doneRows.filter((r) => !targetIds.has(r.id)) : [];
  const failed = rows.filter((r) => r.status === "err").length;

  if (rows.length === 0) {
    return (
      <div class={`zl-ledger${dense ? " is-dense" : ""}${className ? ` ${className}` : ""}`.trim()} {...rest}>
        {children}
      </div>
    );
  }

  return (
    <div class={`zl-ledger${isLive ? " is-live" : ""}${dense ? " is-dense" : ""}${className ? ` ${className}` : ""}`.trim()} {...rest}>
      {folded && earlier.length > 0 && (
        <FoldHeader
          expanded={false}
          earlierCount={earlier.length}
          failed={failed}
          onToggle={() => setExpanded(true)}
        />
      )}
      {expanded && foldable && (
        <FoldHeader
          expanded
          summary={summarizeRows(doneRows.concat(liveRow ? [liveRow] : []))}
          failed={failed}
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

// LedgerDiff — the recessed in-ledger diff the catalogue designed (zones-lab
// `Diff`). Standalone diffs still use DiffBlock; this is the one that opens
// INSIDE a row, one step further down, not a new card.
export function LedgerDiff({ lines = [] }) {
  return (
    <pre class="zl-diff zl-data">
      {lines.map(([t, n, s], i) => (
        <span class={`zl-dl is-${t}`} key={i}>
          <span class="zl-dl-no">{n}</span>
          <span class="zl-dl-sign">{t === "add" ? "+" : t === "del" ? "−" : " "}</span>
          <span class="zl-dl-txt">{s}</span>
        </span>
      ))}
    </pre>
  );
}
