import { useEffect, useRef, useState } from "preact/hooks";
import { formatElapsed } from "../../data/util/activity.js";
import { useElapsed } from "../../data/util/use-elapsed.js";
import { usePresence } from "../../hooks/usePresence.js";
import { MOTION, prefersReducedMotion } from "../../hooks/motion.js";
import { useArrivals } from "./arrivals.js";
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

// ICONS — one glyph per tool, 16px, stroke 1.4–1.5, currentColor.
//
// The map used to hold ten drawings for twenty tools: `read` and `ls` were the
// same page, `grep` and `find` the same lens, `edit` and `multiedit` the same
// pencil, and eight more tools shared one generic wrench. Reading a row meant
// reading its name, which is exactly the work an icon is there to save.
//
// The families are drawn rather than coloured, which is the only kind of
// grouping an icon may assert on its own: what a tool DOES to a file shows in
// what is marked on the page (lines = read, plus = created, ±  = patched,
// pencil = edited), and what leaves the machine carries a boundary (globe,
// plug, person, bolt). No colour, no weight, no second channel — the palette
// stays what it is, a status vocabulary.
const STROKE = { fill: "none", stroke: "currentColor", "stroke-width": "1.4", "stroke-linejoin": "round" };
const STROKE_CAP = { fill: "none", stroke: "currentColor", "stroke-width": "1.5", "stroke-linecap": "round", "stroke-linejoin": "round" };

// The page the file family is built on: read marks it with lines, write with a
// plus, apply_patch with a plus and a minus. Same silhouette, different verb.
const PAGE = "M3.6 2h5.1l3.3 3.3V14H3.6z M8.7 2v3.3H12";

const ICONS = {
  // ── reading: a surface and what is being looked for on it ──────────────
  read: <><path d={PAGE} {...STROKE} /><path d="M5.8 8.6h4.4M5.8 11.1h2.9" {...STROKE_CAP} /></>,
  // A list, not a page: rows with markers is what `ls` returns.
  ls: <><path d="M6.2 4h6.3M6.2 8h6.3M6.2 12h6.3" {...STROKE_CAP} /><circle cx="3.5" cy="4" r="0.95" fill="currentColor" /><circle cx="3.5" cy="8" r="0.95" fill="currentColor" /><circle cx="3.5" cy="12" r="0.95" fill="currentColor" /></>,
  // grep searches CONTENT: the lens sits beside lines of text.
  grep: <><path d="M2.4 3.9h6.1M2.4 6.5h3.5" {...STROKE_CAP} /><circle cx="9.3" cy="9" r="3.4" fill="none" stroke="currentColor" stroke-width="1.5" /><path d="M11.8 11.5l2.2 2.2" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" /></>,
  // find searches for FILES: the same lens against a folder.
  find: <><path d="M2.2 11.4V3.6h3.4l1.2 1.6h5v2.3" {...STROKE} /><circle cx="10" cy="10.4" r="3.1" fill="none" stroke="currentColor" stroke-width="1.5" /><path d="M12.3 12.7l1.8 1.8" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" /></>,
  // A book, not a document: docs are read, not produced.
  moa_docs: <><path d="M8 4.4C6.9 3.4 5.3 2.9 3 2.9v8.6c2.3 0 3.9.5 5 1.5 1.1-1 2.7-1.5 5-1.5V2.9c-2.3 0-4.1.5-5 1.5z" {...STROKE} /><path d="M8 4.4v8.6" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" /></>,
  // A store, not a page: memory is where facts were put, and read back.
  memory: <><path d="M3.6 4c0-1.1 2-2 4.4-2s4.4.9 4.4 2v8c0 1.1-2 2-4.4 2s-4.4-.9-4.4-2z" {...STROKE} /><path d="M3.6 4c0 1.1 2 2 4.4 2s4.4-.9 4.4-2M3.6 8.2c0 1.1 2 2 4.4 2s4.4-.9 4.4-2" {...STROKE} /></>,

  // ── mutating: a mark applied to something ──────────────────────────────
  edit: <path d="M11.6 2.3l2.1 2.1L6 12.1H3.9V9.9z" {...STROKE} />,
  // The same pencil, and the several lines it is being run over.
  multiedit: <><path d="M2.2 5.4h3.1M2.2 8.7h3.1M2.2 12h3.1" {...STROKE_CAP} /><path d="M12.3 2.3l1.5 1.5-5.5 5.5-2 .5.5-2z" {...STROKE} /></>,
  write: <><path d={PAGE} {...STROKE} /><path d="M8 7.9v4.2M5.9 10h4.2" {...STROKE_CAP} /></>,
  // A page carrying an added and a removed line: that is what a patch is.
  apply_patch: <><path d={PAGE} {...STROKE} /><path d="M5.5 8.4h2.1M6.55 7.35v2.1M5.5 11.4h4.6" {...STROKE_CAP} /></>,
  // A flag: the point in the work you can come back to.
  checkpoint: <><path d="M4 2.1v11.8" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" /><path d="M4 3.2h7.8L10.2 5.7l1.6 2.5H4z" {...STROKE} /></>,
  // A shield with the check inside it: checked AND vouched for, which is what
  // separates `verify` from the plain ticks of `tasks`.
  verify: <><path d="M8 2.2l4.5 1.7v3.9c0 3-1.8 5.1-4.5 6.1-2.7-1-4.5-3.1-4.5-6.1V3.9z" {...STROKE} /><path d="M5.9 7.9l1.5 1.5 2.8-3.1" {...STROKE_CAP} /></>,
  tasks: <path d="M3 4.5l1.5 1.5 3-3M3 10.5l1.5 1.5 3-3M9.5 5h3.5M9.5 11H13" {...STROKE_CAP} />,
  task: <path d="M3 4.5l1.5 1.5 3-3M3 10.5l1.5 1.5 3-3M9.5 5h3.5M9.5 11H13" {...STROKE_CAP} />,

  // ── outward: something that crosses the machine's edge ──────────────────
  bash: <path d="M3 4l4 4-4 4M8.5 12H13" {...STROKE_CAP} />,
  fetch: <><circle cx="8" cy="8" r="5.5" fill="none" stroke="currentColor" stroke-width="1.4" /><path d="M2.5 8h11M8 2.5c1.6 1.8 2.4 3.6 2.4 5.5S9.6 12.2 8 13.5C6.4 12.2 5.6 10 5.6 8S6.4 4.3 8 2.5z" {...STROKE} /></>,
  fetch_content: <><circle cx="8" cy="8" r="5.5" fill="none" stroke="currentColor" stroke-width="1.4" /><path d="M2.5 8h11M8 2.5c1.6 1.8 2.4 3.6 2.4 5.5S9.6 12.2 8 13.5C6.4 12.2 5.6 10 5.6 8S6.4 4.3 8 2.5z" {...STROKE} /></>,
  // The globe again, with a lens handle on it: searching the same outside.
  web_search: <><circle cx="6.8" cy="6.8" r="4.3" fill="none" stroke="currentColor" stroke-width="1.4" /><path d="M2.5 6.8h8.6M6.8 2.5c1.25 1.4 1.9 2.9 1.9 4.3s-.65 2.9-1.9 4.3C5.55 9.7 4.9 8.2 4.9 6.8s.65-2.9 1.9-4.3z" {...STROKE} /><path d="M10 10l3.6 3.6" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" /></>,
  mcp: <path d="M5 2v3M11 2v3M3.5 5h9v3a4.5 4.5 0 0 1-9 0zM8 12.5V15" {...STROKE_CAP} />,
  ask: <><circle cx="8" cy="8" r="6" fill="none" stroke="currentColor" stroke-width="1.4" /><path d="M6.4 6.2a1.7 1.7 0 1 1 2 2v1.1" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" /><circle cx="8.4" cy="11.4" r="0.75" fill="currentColor" /></>,
  ask_user: <><circle cx="8" cy="8" r="6" fill="none" stroke="currentColor" stroke-width="1.4" /><path d="M6.4 6.2a1.7 1.7 0 1 1 2 2v1.1" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" /><circle cx="8.4" cy="11.4" r="0.75" fill="currentColor" /></>,
  agent: <><circle cx="8" cy="5.5" r="2.5" fill="none" stroke="currentColor" stroke-width="1.4" /><path d="M3 13.5c.6-2.6 2.5-4 5-4s4.4 1.4 5 4" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" /></>,
  subagent: <><circle cx="8" cy="5.5" r="2.5" fill="none" stroke="currentColor" stroke-width="1.4" /><path d="M3 13.5c.6-2.6 2.5-4 5-4s4.4 1.4 5 4" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" /></>,
  // A capability arriving: the bolt is what the skill adds, not a file.
  load_skill: <path d="M9.4 1.9L4 8.9h3.4l-.7 5.3 5.4-7.2H8.7z" {...STROKE} />,

  // The genuine unknown. It keeps the wrench precisely because it is the one
  // case where the ledger has nothing to say about what the call does.
  tool: <path d="M10.8 2.6a3.4 3.4 0 0 0-4 4.4L3 10.8a1.5 1.5 0 0 0 2.1 2.1L8.9 9.2a3.4 3.4 0 0 0 4.4-4l-2 2-1.6-1.6z" {...STROKE} />,
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
const STATUS_MARK_STROKE_WIDTH = "1.7";

function StatusMark({ status, live }) {
  const kind = live ? "live" : status === "err" ? "err" : status === "warn" ? "warn" : "ok";
  return (
    <span class={`zl-lg-mark is-${kind}`} aria-hidden="true">
      {kind === "ok" && (
        <svg viewBox="0 0 12 12" stroke-width={STATUS_MARK_STROKE_WIDTH}><path d="M2.5 6.5l2.5 2.5 4.5-5" fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round" /></svg>
      )}
      {kind === "err" && (
        <svg viewBox="0 0 12 12" stroke-width={STATUS_MARK_STROKE_WIDTH}><path d="M3 3l6 6M9 3l-6 6" stroke="currentColor" stroke-linecap="round" /></svg>
      )}
      {kind === "warn" && (
        <svg viewBox="0 0 12 12" stroke-width={STATUS_MARK_STROKE_WIDTH}><path d="M6 2.5v4" stroke="currentColor" stroke-linecap="round" /><circle cx="6" cy="9" r="0.9" fill="currentColor" /></svg>
      )}
    </span>
  );
}

function DoneRow({ row, arrive }) {
  const [open, setOpen] = useState(false);
  const { mounted, leaving } = usePresence(open, MOTION.exitFast);
  const { text, detail: argDetail } = argParts(row.arg);
  const dim = argDetail || row.dim;
  const detail = detailNode(row.detail);
  const hasDetail = detail != null;
  const Tag = hasDetail ? "button" : "div";

  return (
    <>
      <Tag
        type={hasDetail ? "button" : undefined}
        class={`zl-lg-row${open ? " is-open" : ""}${row._folding ? " is-folding" : ""}${arrive != null ? " is-arriving" : ""}`}
        style={arrive ? `--zl-lg-delay: calc(${arrive} * var(--motion-stagger))` : undefined}
        onClick={hasDetail ? () => setOpen((v) => !v) : undefined}
        aria-expanded={hasDetail ? open : undefined}
      >
        <ToolIcon tool={row.tool} />
        <span class="zl-lg-txt" title={fullLabel(row, text)}>
          <span class="zl-lg-tool">{row.tool}</span>
          <span class={`zl-lg-arg zl-data${row.argTail ? " is-tail" : ""}`}>{text}</span>
          {dim && <span class="zl-lg-dim"> · {dim}</span>}
        </span>
        {row.echo && (
          <span class={`zl-lg-echo zl-data is-${row.status || "ok"}`} title={row.echo}>{row.echo}</span>
        )}
        {row.out && <span class="zl-lg-out zl-data">{row.out}</span>}
        <StatusMark status={row.status} />
        {hasDetail && <Chevron open={open} />}
        <span class="sr-only">{SR[row.status] || SR.ok}</span>
      </Tag>
      {hasDetail && mounted && (
        <div class={`zl-lg-detail is-opening${leaving ? " is-leaving" : ""}`}>
          <div class="zl-lg-detail-in">{detail}</div>
        </div>
      )}
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

function LiveCommand({ command, leaving }) {
  return (
    <div class={`zl-lg-detail is-opening${leaving ? " is-leaving" : ""}`}>
      <div class="zl-lg-detail-in">
        <div class="doc-mono zl-lg-cmd">
          <span class="zl-lg-prompt" aria-hidden="true">$ </span>
          {command}
        </div>
      </div>
    </div>
  );
}

function LiveRow({ row, arrive }) {
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
  const commandOpen = expandable && expanded;
  const command = usePresence(commandOpen, MOTION.exitFast);
  const Tag = expandable ? "button" : "div";
  return (
    <>
      <Tag
        type={expandable ? "button" : undefined}
        class={`zl-lg-row is-live${expanded && expandable ? " is-open" : ""}${arrive != null ? " is-arriving" : ""}`}
        style={arrive ? `--zl-lg-delay: calc(${arrive} * var(--motion-stagger))` : undefined}
        role={expandable ? undefined : "status"}
        aria-live={expandable ? undefined : "off"}
        onClick={expandable ? () => setExpanded((value) => !value) : undefined}
        aria-expanded={expandable ? expanded : undefined}
      >
        <ToolIcon tool={row.tool} />
        <span class="zl-lg-txt" title={fullLabel(row, text)}>
          <span class="zl-lg-tool">{row.tool}</span>
          <span class={`zl-lg-arg zl-data${row.argTail ? " is-tail" : ""}`}>{text}</span>
          {dim && <span class="zl-lg-dim"> · {dim}</span>}
        </span>
        {elapsed && <span class="zl-lg-out zl-data">{elapsed}</span>}
        <StatusMark live />
        {expandable && <Chevron open={expanded} />}
        <span class="sr-only">{SR.live}</span>
      </Tag>
      {expandable && command.mounted && (
        <LiveCommand command={row.command} leaving={command.leaving} />
      )}
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
  return order.map((k) => `${counts[k]} ${pluralizeTool(k, counts[k])}`).join(" · ");
}

// countedSummary is summarizeRows with its total in front, which is what the
// EXPANDED header says. Folded, the total is already the row's first word, so
// repeating it there would say the number twice.
function countedSummary(rows) {
  const total = rows.length;
  return `${total} action${total === 1 ? "" : "s"} · ${summarizeRows(rows)}`;
}

function FoldHeader({ expanded, earlier = [], failed, summary, absorbed, onToggle }) {
  return (
    <button type="button" class="zl-lg-head" onClick={onToggle} aria-expanded={expanded}>
      <Chevron open={expanded} />
      <span class="zl-lg-head-t">
        {expanded
          ? summary
          : (
            // Folded, the header used to say "11 earlier actions" and nothing
            // else: a number that tells you work happened and refuses to say
            // what. The composition was already being computed for the
            // EXPANDED header, where it is least needed — you can see the rows
            // there. Same function, moved to the state that hides them; the
            // fold itself is untouched.
            <>
              <span class={`zl-data${absorbed ? " is-absorbing" : ""}`}>{earlier.length}</span> earlier
              {earlier.length > 0 && <> · {summarizeRows(earlier)}</>}
            </>
          )}
      </span>
      {failed > 0 && (
        <span class="zl-lg-head-fail"><span class="zl-data">{failed}</span> failed</span>
      )}
    </button>
  );
}

// useCounterBump — true for one beat after `value` changes, so a number that
// has just grown can acknowledge it. Returns false when `value` is null (the
// counter is not on screen) and under reduced motion, where the number itself
// is the whole message and does not need a second one.
const BUMP_MS = 240;

function useCounterBump(value) {
  const previous = useRef(value);
  const [, force] = useState(0);
  const until = useRef(0);

  const changed = previous.current !== value && previous.current != null && value != null;
  previous.current = value;
  if (changed && !prefersReducedMotion()) until.current = Date.now() + BUMP_MS;

  const on = Date.now() < until.current;
  useEffect(() => {
    if (!on) return undefined;
    const t = setTimeout(() => force((n) => n + 1), Math.max(0, until.current - Date.now()));
    return () => clearTimeout(t);
  }, [on]);

  return on;
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

  // Which rows are allowed to move, computed over everything rendered (the
  // tail window plus the live row) so the hook sees one stable id list and a
  // row does not "arrive" twice by crossing between the two lists.
  const slotOf = useArrivals(visible.concat(liveRow ? [liveRow] : []));

  // The header ABSORBING a row is its own moment: the count on it changes at
  // the instant a row leaves the window and starts folding, so the two read as
  // one movement — this row went into that number. `earlier.length` is what
  // the header says, so it is what marks it.
  const absorbed = useCounterBump(folded ? earlier.length : null);

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
          earlier={earlier}
          failed={failed}
          absorbed={absorbed}
          onToggle={() => setExpanded(true)}
        />
      )}
      {expanded && foldable && (
        <FoldHeader
          expanded
          summary={countedSummary(doneRows.concat(liveRow ? [liveRow] : []))}
          failed={failed}
          onToggle={() => setExpanded(false)}
        />
      )}
      {visible.map((row, i) => (
        <DoneRow key={rowKey(row, i)} row={row} arrive={slotOf(row.id)} />
      ))}
      {liveRow && <LiveRow key={rowKey(liveRow, doneRows.length)} row={liveRow} arrive={slotOf(liveRow.id)} />}
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
