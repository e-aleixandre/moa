import { useEffect, useRef, useState } from "preact/hooks";
/* The token layer and the lab chrome are imported, never edited: this file
   owns nothing of zones-lab, it only speaks its language (tonal ladder,
   Outfit for words / IBM Plex Mono for data, 3 radii, 4 sizes, 3 weights,
   mauve for accent, green/yellow/red for state, peach only on the user's
   message). Everything new here is prefixed zw-. */
import "./zones-lab.css";
import "./zones-work.css";

/* ═══════════════════════════════════════════════════════════════════════════
   WORK: the tool calls, and the two screens async work runs away to.

   Three things are designed here, in this order, because that is the order
   in which the product loses you:

     1  TOOL CALLS      what the agent did, in line, inside the turn.
     2  SUBAGENT        a conversation you delegated: its own transcript.
     3  ASYNC BASH      a command that outlives the turn: its own output.

   The architecture is the one already decided: the line is the controls for
   the next turn, the panel is the dossier of the session, the CENTRE is the
   result. A subagent and a background command are results in progress, so
   they take the centre; the live bar over the composer lists them and a row
   is the door. Opening one never opens a modal: it is a place, it has a
   back, and the back always says where it goes.

   Four holes in today's product are answered here, each argued at its
   component: cancelled has no state in the ledger; send_file is a card when
   it works and a row when it fails; bash prints no cost (right) and no facts
   at all (wrong); and nobody has designed "waiting for you" for either
   screen, which is the one state where looking matters most.
   ═══════════════════════════════════════════════════════════════════════ */

/* ── Shared atoms ────────────────────────────────────────────────────── */

const HUES = [210, 265, 170, 320, 40, 190];
function hueOf(name) {
  let h = 0;
  for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) >>> 0;
  return HUES[h % HUES.length];
}

function useNow(active) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!active) return;
    setNow(Date.now());
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, [active]);
  return now;
}

function fmtElapsed(ms) {
  const s = Math.max(0, Math.floor(ms / 1000));
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  const r = s % 60;
  if (m < 60) return `${m}m${String(r).padStart(2, "0")}s`;
  return `${Math.floor(m / 60)}h${String(m % 60).padStart(2, "0")}m`;
}

function BackChevron() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true">
      <path d="M10 3.5L5.5 8l4.5 4.5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" />
    </svg>
  );
}
function CopyIcon() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true">
      <rect x="5.5" y="5.5" width="8" height="8" rx="1.6" fill="none" stroke="currentColor" stroke-width="1.4" />
      <path d="M10.5 5.5v-2a1 1 0 0 0-1-1h-6a1 1 0 0 0-1 1v6a1 1 0 0 0 1 1h2" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round" />
    </svg>
  );
}

/* ── 1 · TOOL CALLS ──────────────────────────────────────────────────────
   Icons follow production's own map (ActivityLedger.jsx:4-31) plus two the
   inventory found missing: ask_user, which today is a wrench like everything
   unknown, and mcp__*, which is not "unknown" at all -- it is a named server
   and the name is the useful half. `mcp__playwright__browser_click` is
   printed as the server (playwright) plus the tool (browser_click), because
   the raw token is 30 characters of prefix nobody reads. */
const ICONS = {
  read: <path d="M3.5 2.5h6l3 3v8h-9z M9.5 2.5v3h3" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round" />,
  grep: <><circle cx="7" cy="7" r="4" fill="none" stroke="currentColor" stroke-width="1.5" /><path d="M10 10l3.5 3.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" /></>,
  bash: <path d="M3 4l4 4-4 4M8.5 12H13" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />,
  edit: <path d="M11.5 2.5l2 2L6 12H4v-2z" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round" />,
  write: <path d="M3.5 2.5h6l3 3v8h-9z M8 7v4M6 9h4" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round" />,
  fetch: <><circle cx="8" cy="8" r="5.5" fill="none" stroke="currentColor" stroke-width="1.4" /><path d="M2.5 8h11M8 2.5c1.6 1.8 2.4 3.6 2.4 5.5S9.6 12.2 8 13.5C6.4 12.2 5.6 10 5.6 8S6.4 4.3 8 2.5z" fill="none" stroke="currentColor" stroke-width="1.4" /></>,
  tasks: <path d="M3 4.5l1.5 1.5 3-3M3 10.5l1.5 1.5 3-3M9.5 5h3.5M9.5 11H13" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />,
  mcp: <path d="M5 2v3M11 2v3M3.5 5h9v3a4.5 4.5 0 0 1-9 0zM8 12.5V15" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />,
  ask: <><circle cx="8" cy="8" r="6" fill="none" stroke="currentColor" stroke-width="1.4" /><path d="M6.4 6.2a1.7 1.7 0 1 1 2 2v1.1" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" /><circle cx="8.4" cy="11.4" r="0.75" fill="currentColor" /></>,
  agent: <><circle cx="8" cy="5.5" r="2.5" fill="none" stroke="currentColor" stroke-width="1.4" /><path d="M3 13.5c.6-2.6 2.5-4 5-4s4.4 1.4 5 4" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" /></>,
  tool: <path d="M10.8 2.6a3.4 3.4 0 0 0-4 4.4L3 10.8a1.5 1.5 0 0 0 2.1 2.1L8.9 9.2a3.4 3.4 0 0 0 4.4-4l-2 2-1.6-1.6z" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round" />,
};
function ToolIcon({ kind }) {
  return <svg class="zw-ico" viewBox="0 0 16 16" aria-hidden="true">{ICONS[kind] || ICONS.tool}</svg>;
}

/* The mark. Four terminal shapes, and they are four because a run can end in
   four genuinely different ways -- and the inventory found the fourth one
   missing (stream-model.js:720 folds anything that is not error/rejected
   into ok, so a cancelled call today shows a green tick).

     ✓ green   it did what it said
     ✗ red     it failed: something is broken and you may have to act
     ! yellow  rejected: the permission mode said no; you are the reason
     ▪ grey    cancelled: there is no result, and that is not a problem

   Cancelled is deliberately NOT a state colour. Green/yellow/red are the
   three semantic colours in this product and each means "look at me" in a
   different key; a call you stopped on purpose has nothing to report. It
   keeps the neutral tone, the row dims, and the summary says the word. No
   strikethrough: struck-through text reads as "deleted", and the call did
   happen. */
function Mark({ status }) {
  if (status === "live") {
    return <span class="zw-mark is-live" aria-hidden="true"><span class="zw-mark-dot" /></span>;
  }
  return (
    <span class={`zw-mark is-${status}`} aria-hidden="true">
      {status === "ok" && <svg viewBox="0 0 12 12"><path d="M2.5 6.5l2.5 2.5 4.5-5" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" /></svg>}
      {status === "err" && <svg viewBox="0 0 12 12"><path d="M3 3l6 6M9 3l-6 6" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" /></svg>}
      {status === "warn" && <svg viewBox="0 0 12 12"><path d="M6 2.5v4" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" /><circle cx="6" cy="9" r="0.9" fill="currentColor" /></svg>}
      {status === "cancel" && <svg viewBox="0 0 12 12"><rect x="3.2" y="3.2" width="5.6" height="5.6" rx="1.2" fill="currentColor" /></svg>}
    </span>
  );
}
const SR = { ok: "completed", err: "failed", warn: "rejected", cancel: "cancelled", live: "running" };

/* Output block. One rule for every kind of captured text: a recessed slab in
   mono, anchored to its END, because the line that matters (the failure, the
   summary, the last thing the process said) is the last one. */
function Log({ text, tall, follow = true }) {
  const ref = useRef(null);
  useEffect(() => { if (ref.current && follow) ref.current.scrollTop = ref.current.scrollHeight; }, [text, follow]);
  return <pre class={`zw-log zl-data${tall ? " is-tall" : ""}`} ref={ref}>{text}</pre>;
}

function Diff({ lines }) {
  return (
    <pre class="zw-diff zl-data">
      {lines.map(([t, n, s], i) => (
        <span class={`zw-dl is-${t}`} key={i}>
          <span class="zw-dl-no">{n}</span>
          <span class="zw-dl-sign">{t === "add" ? "+" : t === "del" ? "−" : " "}</span>
          <span class="zw-dl-txt">{s}</span>
        </span>
      ))}
    </pre>
  );
}

/* Very long output. DECISION: scroll, capped, with a door -- not truncation
   and not an inline box that grows to a thousand lines.

   The reasons, in order:
   · An inline block taller than the viewport steals the transcript's scroll.
     You lose the turn you were reading to look at a log you did not ask to
     read in full.
   · Truncating with no way out is worse: today the ledger keeps 400 lines
     and 20 000 characters (stream-model.js:826) and the bash screen keeps
     1 000 (bash-job-view-model.js:15). Two silent caps, neither reachable.
   · So: ONE ladder, and each step says its own size.
         5 lines     the live tail, while it runs
         ~12 lines   the inline evidence, scrollable, last 200 kept
         1 000       the full output screen -- the same screen an async bash
                     opens, reached from the row's footer
     The header states what is missing (`N earlier lines not shown`) and the
     footer states the whole size plus the door. Nothing is hidden silently. */
function BigOut({ total, kept, text, onOpen }) {
  return (
    <div class="zw-big">
      <div class="zw-big-head zl-data">… {(total - kept).toLocaleString("en-US")} earlier lines not shown</div>
      <Log text={text} tall />
      <div class="zw-big-foot">
        <span class="zw-big-n zl-data">{total.toLocaleString("en-US")} lines · last {kept} kept here</span>
        <button type="button" class="zw-linkbtn" onClick={onOpen}>
          Open full output
          <svg viewBox="0 0 12 12" aria-hidden="true"><path d="M4 8l4-4M4.6 3.7H8.3V7.4" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" /></svg>
        </button>
      </div>
    </div>
  );
}

/* ask_user, finished: the question and what you answered. It is the one tool
   whose result is a sentence of yours, so it is printed as a two-line record
   and not as a log. */
function AskDetail({ q, a }) {
  return (
    <div class="zw-askrec">
      <div class="zw-askrec-l"><span class="zw-askrec-k">asked</span><span class="zw-askrec-v">{q}</span></div>
      <div class="zw-askrec-l"><span class="zw-askrec-k">you</span><span class="zw-askrec-v is-you">{a}</span></div>
    </div>
  );
}

function Detail({ d, onOpenOutput }) {
  if (!d) return null;
  if (d.kind === "log") return <Log text={d.text} />;
  if (d.kind === "cmd") return (<><div class="zw-cmd zl-data"><span class="zw-cmd-sig">$</span>{d.cmd}</div>{d.text && <Log text={d.text} />}</>);
  if (d.kind === "diff") return <Diff lines={d.lines} />;
  if (d.kind === "ask") return <AskDetail q={d.q} a={d.a} />;
  if (d.kind === "big") return <BigOut {...d} onOpen={onOpenOutput} />;
  return null;
}

/* A ledger row. The chevron rule, already decided, is enforced structurally:
   a row with no detail is a div -- not a button with a disabled look -- so it
   cannot be tabbed to, cannot be pressed, and shows no chevron. */
function LedgerRow({ r, open, onToggle, onOpenOutput, elapsed }) {
  const expandable = !!r.detail;
  const Tag = expandable ? "button" : "div";
  const live = r.status === "live";
  return (
    <>
      <Tag
        type={expandable ? "button" : undefined}
        class={`zw-row is-${r.status}${open ? " is-open" : ""}`}
        onClick={expandable ? onToggle : undefined}
        aria-expanded={expandable ? open : undefined}
      >
        <ToolIcon kind={r.icon || r.tool} />
        <span class="zw-row-txt">
          <span class="zw-tool">{r.tool}</span>
          <span class="zw-arg zl-data">{r.arg}</span>
          {r.dim && <span class="zw-dim zl-data">{r.dim}</span>}
        </span>
        <span class={`zw-out zl-data is-${r.status}`}>{live ? elapsed : r.out}</span>
        <Mark status={r.status} />
        {expandable && (
          <span class="zw-chev" aria-hidden="true">
            <svg viewBox="0 0 12 12"><path d="M4 2.5L7.5 6 4 9.5" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" /></svg>
          </span>
        )}
        <span class="zw-sr">{SR[r.status]}</span>
      </Tag>
      {live && r.tail && <div class="zw-tail"><Log text={r.tail} /></div>}
      {expandable && open && <div class="zw-detail"><Detail d={r.detail} onOpenOutput={onOpenOutput} /></div>}
    </>
  );
}

function Ledger({ rows, folded: foldedInit = false, dense, t0, onOpenOutput, openInit = [] }) {
  const [open, setOpen] = useState(() => new Set(openInit));
  const [folded, setFolded] = useState(foldedInit && rows.length > 3);
  const live = rows.some((r) => r.status === "live");
  const now = useNow(live);
  const toggle = (k) => setOpen((v) => { const n = new Set(v); n.has(k) ? n.delete(k) : n.add(k); return n; });
  const hidden = folded ? rows.slice(0, rows.length - 2) : [];
  const shown = folded ? rows.slice(rows.length - 2) : rows;
  const failed = rows.filter((r) => r.status === "err").length;
  const cancelled = rows.filter((r) => r.status === "cancel").length;
  /* The folded header is a census, and a census that hides an error is a lie:
     failures and cancellations are counted on the closed header, in their own
     ink, so folding never buries the reason you would unfold. */
  return (
    <div class={`zw-ledger${dense ? " is-dense" : ""}${live ? " is-live" : ""}`}>
      {rows.length > 3 && (
        <button type="button" class="zw-head" onClick={() => setFolded((v) => !v)} aria-expanded={!folded}>
          <span class={`zw-chev is-head${folded ? "" : " is-open"}`} aria-hidden="true">
            <svg viewBox="0 0 12 12"><path d="M4 2.5L7.5 6 4 9.5" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" /></svg>
          </span>
          <span class="zw-head-t">
            {folded
              ? <><span class="zl-data">{hidden.length}</span> earlier actions</>
              : <><span class="zl-data">{rows.length}</span> actions · {census(rows)}</>}
          </span>
          {failed > 0 && <span class="zw-head-n is-err"><span class="zl-data">{failed}</span> failed</span>}
          {cancelled > 0 && <span class="zw-head-n is-cancel"><span class="zl-data">{cancelled}</span> cancelled</span>}
        </button>
      )}
      {shown.map((r, i) => (
        <LedgerRow
          key={`${r.tool}:${r.arg}:${i}`}
          r={r}
          open={open.has(`${r.tool}:${r.arg}`)}
          onToggle={() => toggle(`${r.tool}:${r.arg}`)}
          onOpenOutput={onOpenOutput}
          elapsed={r.status === "live" ? fmtElapsed(now - (t0 - (r.ago || 0) * 1000)) : null}
        />
      ))}
    </div>
  );
}
/* `3 bashs` is what a naive plural gives, and it is the kind of detail that
   makes an interface look machine-written. A tool name is a command, not a
   noun: it is pluralised only where English allows it. */
const NO_PLURAL = new Set(["bash", "grep", "ls", "write", "fetch_content", "ask_user", "playwright"]);
function census(rows) {
  const by = {};
  for (const r of rows) by[r.tool] = (by[r.tool] || 0) + 1;
  return Object.entries(by)
    .map(([k, n]) => `${n} ${k}${n > 1 && !NO_PLURAL.has(k) ? "s" : ""}`)
    .join(" · ");
}

/* send_file. DECISION on the incoherence the inventory found (a delivered
   file leaves the ledger and becomes a card, a failed one stays a row):
   send_file is never a ledger row. Both outcomes are the card.

   Why this way round and not "always a row": what you asked for is the FILE.
   A delivery is a result, and results live in the prose as objects you can
   take away -- that is the same rule that puts the artifact card there in
   the first place. Keeping the row for the failure only means the failure
   looks like a different kind of event from the success, when it is the same
   event with the opposite outcome.

   So the failed card has the same footprint, the same glyph, the same name;
   what changes is that it is not openable, the rim goes red, and the second
   line is the reason instead of the size. You can still see WHAT was not
   delivered, which the ledger row never told you. */
function Deliverable({ name, kind, size, error }) {
  const Tag = error ? "div" : "button";
  return (
    <Tag type={error ? undefined : "button"} class={`zw-file${error ? " is-err" : ""}`} aria-label={error ? undefined : `Open ${name}`}>
      <span class="zw-file-ico" aria-hidden="true">
        <svg viewBox="0 0 20 24">
          <path d="M2.75 1h8.5L17.25 7v15.25a.75.75 0 0 1-.75.75h-13a.75.75 0 0 1-.75-.75V1.75A.75.75 0 0 1 2.75 1z" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round" />
          <path d="M11.25 1v5.25a.75.75 0 0 0 .75.75h5.25" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round" />
        </svg>
      </span>
      <span class="zw-file-main">
        <span class="zw-file-name">{name}</span>
        <span class={`zw-file-meta zl-data${error ? " is-err" : ""}`}>{error ? `not delivered · ${error}` : `${kind} · ${size}`}</span>
      </span>
      {error
        ? <Mark status="err" />
        : (
          <span class="zw-file-act" aria-hidden="true">
            <svg viewBox="0 0 16 16"><path d="M8 2.5v8M4.5 7L8 10.5 11.5 7M3 13h10" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" /></svg>
          </span>
        )}
    </Tag>
  );
}

/* ── Specimen data ───────────────────────────────────────────────────── */

const FAIL_LOG = `--- FAIL: TestDeleteMissing (0.00s)
    store_test.go:91: expected ErrNotFound, got <nil>
FAIL
FAIL    moa/pkg/attach  0.014s`;

const DIFF_LINES = [
  ["ctx", 14, "func (s *Store) Delete(id string) error {"],
  ["ctx", 15, "\ts.mu.Lock()"],
  ["del", 16, "\tdelete(s.index, id)"],
  ["add", 16, "\tif _, ok := s.index[id]; !ok {"],
  ["add", 17, "\t\ts.mu.Unlock()"],
  ["add", 18, "\t\treturn ErrNotFound"],
  ["add", 19, "\t}"],
  ["add", 20, "\tdelete(s.index, id)"],
  ["ctx", 21, "\ts.mu.Unlock()"],
];

const BIG_TEXT = Array.from({ length: 34 }, (_, i) => {
  const n = 1051 + i;
  if (i === 30) return `pkg/serve/frontend/src/data/stream-model.js:${n}  warning: unused binding 'kind'`;
  if (i === 33) return "webpack 5.94.0 compiled with 1 warning in 12043 ms";
  return `  asset  static/build/chunk-${String(n).padStart(4, "0")}.js   ${(12 + (i % 9) * 3.4).toFixed(1)} KiB  [emitted]`;
}).join("\n");

/* The types that genuinely look different, per INV-TOOLCALLS: a plain row
   with nothing to open; a row with captured output; bash (command + output);
   an edit (diff); ask_user (a record of your answer); an MCP call (server +
   tool, not a wrench); a rejected call; a cancelled call; and a live one
   with its tail. */
const ROWS_TYPES = (t0) => [
  { tool: "read", arg: "pkg/serve/frontend/src/data/stream-model.js", out: "918 lines", status: "ok" },
  { tool: "grep", arg: "mapStatus", dim: "src/data", out: "4 hits", status: "ok",
    detail: { kind: "log", text: `data/stream-model.js:720  function mapStatus(s) {\ndata/stream-model.js:734    const st = mapStatus(m.status);\ndata/stream-model.js:775    const st = mapStatus(m.status);\ndata/subagent-view-model.js:26  const TERMINAL = new Set([...])` } },
  { tool: "bash", arg: "go test ./pkg/attach/ -run Delete", out: "exit 1", status: "err",
    detail: { kind: "cmd", cmd: "go test ./pkg/attach/ -run Delete -race -count=1", text: FAIL_LOG } },
  { tool: "edit", arg: "pkg/attach/store.go", dim: "+5 −1", out: "ok", status: "ok",
    detail: { kind: "diff", lines: DIFF_LINES } },
  { tool: "ask_user", icon: "ask", arg: "Borro el worktree design-visual?", out: "answered", status: "ok",
    detail: { kind: "ask", q: "El worktree design-visual tiene 3 commits sin subir. ¿Lo borro igualmente?", a: "No. Súbelos primero y lo miramos." } },
  { tool: "playwright", icon: "mcp", arg: "browser_click", dim: "mcp", out: "rejected", status: "warn",
    detail: { kind: "log", text: "Rejected: permission mode is `ask` and you answered no.\nNothing was clicked; the page is unchanged." } },
  { tool: "bash", arg: "npm run build", out: "stopped at 42s", status: "cancel",
    detail: { kind: "cmd", cmd: "npm run build", text: "> moa-frontend build\n> node esbuild.mjs --prune\n\n  bundling…" } },
  { tool: "bash", arg: "go build ./...", status: "live", ago: 6,
    tail: "go: downloading golang.org/x/sys v0.28.0\ngo: downloading github.com/mattn/go-isatty v0.0.20\n# moa/pkg/serve\ncompiling pkg/serve…" },
];

/* Seven in a row: the grouping case. Folded by default, which is production's
   behaviour above three rows. */
const ROWS_GROUP = [
  { tool: "ls", icon: "read", arg: "pkg/serve/frontend/src/catalog", out: "31 entries", status: "ok" },
  { tool: "read", arg: "src/catalog/zones-lab.jsx", out: "1 910 lines", status: "ok" },
  { tool: "read", arg: "src/catalog/zones-lab.css", out: "2 219 lines", status: "ok" },
  { tool: "grep", arg: "zl-live", dim: "src/catalog", out: "42 hits", status: "ok",
    detail: { kind: "log", text: "zones-lab.jsx:812   const LIVE_BG = [\nzones-lab.jsx:874   function LiveZone({ fg, bg, open …\nzones-lab.css:1384  .zl-live {" } },
  { tool: "fetch_content", icon: "fetch", arg: "developer.mozilla.org/…/container-queries", out: "6.2 kB", status: "ok" },
  { tool: "tasks", arg: "create · measure the two densities", out: "#4", status: "ok" },
  { tool: "write", arg: "src/catalog/zones-work.css", dim: "+412", out: "ok", status: "ok" },
];

const ROWS_BIG = (onOpen) => [
  { tool: "bash", arg: "npm run build", dim: "frontend", out: "1 284 lines", status: "ok",
    detail: { kind: "big", total: 1284, kept: 200, text: BIG_TEXT } },
];

/* ── The two frames ──────────────────────────────────────────────────────
   Nothing invented for the shell: the phone is the phone from zones-lab (390
   wide) and the desktop is the CONVERSATION COLUMN only. The left column of
   sessions and the right session panel are unchanged by any of this work, so
   drawing them again here would only invite them to drift. */
function Frame({ kind, title, children }) {
  return (
    <div class={`zw-frame is-${kind}`}>
      <div class="zl-density-label">{title}</div>
      <div class={`zw-screen is-${kind}`}>{children}</div>
    </div>
  );
}

/* ── 1 · the specimen ────────────────────────────────────────────────── */
function ToolSpecimen({ dense, onOpenOutput }) {
  const [t0] = useState(() => Date.now());
  return (
    <div class={`zw-doc${dense ? " is-dense" : ""}`}>
      <p class="zw-cap">Every type that reads differently · expanded</p>
      <Ledger rows={ROWS_TYPES(t0)} dense={dense} t0={t0} onOpenOutput={onOpenOutput} openInit={["bash:go test ./pkg/attach/ -run Delete"]} />

      <p class="zw-cap">Seven in a row · folded, as production folds above three</p>
      <Ledger rows={ROWS_GROUP} folded dense={dense} t0={t0} onOpenOutput={onOpenOutput} />

      <p class="zw-cap">A lot of output · capped, scrollable, with a door</p>
      <Ledger rows={ROWS_BIG()} dense={dense} t0={t0} onOpenOutput={onOpenOutput} openInit={["bash:npm run build"]} />

      <p class="zw-cap">A delivered file, and one that was not · both are the deliverable, never a row</p>
      <Deliverable name="attach-race-report.md" kind="md" size="4.2 kB" />
      <Deliverable name="coverage.html" error="38 MB exceeds the 25 MB limit" />
    </div>
  );
}

/* ── 2 · SUBAGENT ────────────────────────────────────────────────────────
   The screen you land on when you open a delegated conversation.

   Order, top to bottom, and why:
     head     back (says where to), whose child this is, run mode, stop.
     facts    the printed dossier: model, thinking, elapsed, turns, tokens,
              spend. Same idea as the session panel's run facts -- printed,
              not boxed -- because the boxes are for what you press.
     rail     the siblings, only when there are at least two live.
     body     its transcript. This is the point of the screen.
     tail     while running: the now-line and a steer composer. When it has
              ended: the outcome banner and the way back.

   Desktop DECISION: it replaces the conversation column, exactly as
   production does, and does NOT open as a modal or a new pane. A subagent
   has a transcript, a composer and a status of its own -- it is a
   conversation, and conversations own the centre. The breadcrumb keeps the
   parent one click away and `esc` does the same thing as the arrow. A pane
   would have been the other candidate; it loses, because you would then have
   two composers on screen and no way to say which one Enter belongs to. */
const SA_STATES = [
  { id: "running", label: "Running", note: "Green, breathing, counting. The now-line says the current action and the composer steers it. Everything about the child is on the child's screen; the parent keeps only a row in the live bar." },
  { id: "waiting", label: "Waiting for you", note: "The state nobody had designed. Amber and still: no shimmer and no work counter, because the run is parked on you and movement would claim progress. The question is a card at the tail of ITS transcript, answered from here." },
  { id: "completed", label: "Completed", note: "The banner states the outcome, the duration and what it cost, and the result is copyable. The transcript stays: the outcome is a lid, not a replacement." },
  { id: "failed", label: "Failed", note: "Red rim, the error's first lines verbatim, copy. Nothing is summarised away: a failure you cannot paste is a failure you cannot report." },
  { id: "cancelled", label: "Cancelled", note: "Neutral, not red. You stopped it; there is nothing to fix. It states how far it got, so the work is not lost." },
];

const SA_TRANSCRIPT = [
  { who: "you", text: "Revisa el diff de pkg/attach y dime si el fix del borrado tapa la carrera o solo el síntoma.", when: "09:41" },
  { who: "agent", text: "Voy a leer el store y el test antes de opinar: el síntoma (blob huérfano) puede venir del orden de las escrituras o del contador de referencias, y son arreglos distintos." },
  { who: "agent", text: "El store guarda el índice y el contador por separado, así que el fix tiene que ser correcto en los dos. Miro dónde se decrementa refs." },
];

const SA_ROWS = (live) => [
  { tool: "read", arg: "pkg/attach/store.go", out: "212 lines", status: "ok" },
  { tool: "read", arg: "pkg/attach/store_test.go", out: "148 lines", status: "ok" },
  { tool: "bash", arg: "git diff -- pkg/attach", out: "38 lines", status: "ok",
    detail: { kind: "cmd", cmd: "git diff --stat -- pkg/attach", text: " pkg/attach/store.go      | 6 +++++-\n pkg/attach/store_test.go | 32 ++++++++++++++++++++++++++++++++\n 2 files changed, 37 insertions(+), 1 deletion(-)" } },
  live
    ? { tool: "grep", arg: "refs--", dim: "pkg/attach", status: "live", ago: 3 }
    : { tool: "grep", arg: "refs--", dim: "pkg/attach", out: "2 hits", status: "ok", detail: { kind: "log", text: "store.go:118  s.refs[id]--\nstore.go:204  s.refs[id]--" } },
];

function StopButton({ phone, onStop }) {
  const [armed, setArmed] = useState(false);
  useEffect(() => { if (!armed) return; const t = setTimeout(() => setArmed(false), 3000); return () => clearTimeout(t); }, [armed]);
  return (
    <button
      type="button"
      class={`zw-stop${armed ? " is-armed" : ""}${phone ? " is-phone" : ""}`}
      onClick={() => (armed ? (setArmed(false), onStop && onStop()) : setArmed(true))}
      aria-label={armed ? "Confirm stop" : "Stop"}
    >
      <span class="zw-stop-sq" aria-hidden="true" />
      <span class="zw-stop-t">{armed ? "sure?" : "Stop"}</span>
    </button>
  );
}

/* The facts strip. Printed, hairline above and below, values in mono.
   THIS is where finding 3 is answered: the strip exists on both screens and
   each prints ITS OWN currency. A subagent spends tokens and money, so it
   prints them. A command spends wall time and returns a code, so it prints
   those -- and printing `$0.00` for a command would invent a meter that does
   not exist and teach you to distrust the one that does. What was actually
   missing from bash was not cost: it was facts at all. */
function Facts({ items }) {
  return (
    <dl class="zw-facts">
      {items.map(([k, v, tone]) => (
        <div key={k}>
          <dt>{k}</dt>
          <dd class={`zl-data${tone ? ` is-${tone}` : ""}`}>{v}</dd>
        </div>
      ))}
    </dl>
  );
}

function NowLine({ text, phase, elapsed }) {
  return (
    <div class={`zw-now is-${phase}`} role="status" aria-live="polite">
      <span class={`zw-now-dot is-${phase}`} aria-hidden="true" />
      <span class="zw-now-t">{text}</span>
      {phase !== "waiting" && <span class="zw-now-el zl-data">{elapsed}</span>}
    </div>
  );
}

function Steer({ placeholder }) {
  const [v, setV] = useState("");
  return (
    <div class={`zw-composer${v.trim() ? " is-armed" : ""}`}>
      <textarea class="zw-ta" rows="1" placeholder={placeholder} value={v} onInput={(e) => setV(e.currentTarget.value)} aria-label={placeholder} />
      <button type="button" class="zw-send" aria-label="Send" disabled={!v.trim()}>
        <svg viewBox="0 0 16 16" aria-hidden="true"><path d="M8 13V3.5M8 3.5L3.8 7.7M8 3.5l4.2 4.2" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" /></svg>
      </button>
    </div>
  );
}

/* Waiting for you, designed. Two halves, and both are needed:
   · the QUESTION, as a card at the tail of the child's own transcript --
     because that is where it was asked and where the context is;
   · the fact that it is waiting, in the head and in the facts strip, so the
     state survives scrolling away from the card.
   The card carries the answers as real buttons (44 on the phone) and a free
   field at 16px. Amber, and completely still: the run is stopped on you. */
function AskCard({ phone }) {
  return (
    <div class="zw-ask" role="group" aria-label="The subagent is asking you something">
      <div class="zw-ask-head">
        <span class="zw-now-dot is-waiting" aria-hidden="true" />
        <span class="zw-ask-h">terra is waiting for your answer</span>
        <span class="zw-ask-since zl-data">since 09:47</span>
      </div>
      <p class="zw-ask-q">El fix tapa la carrera, pero <code class="zl-data">refs</code> puede quedar en −1 en sesiones ya guardadas. ¿Migro los contadores existentes o lo dejo como deuda?</p>
      <div class="zw-ask-opts">
        <button type="button" class={`zw-ask-b is-primary${phone ? " is-phone" : ""}`}>Migrate them now</button>
        <button type="button" class={`zw-ask-b${phone ? " is-phone" : ""}`}>Leave it as debt</button>
      </div>
      <input class="zw-ask-in" placeholder="…or answer in your own words" aria-label="Answer in your own words" />
    </div>
  );
}

function Banner({ outcome, phone, onBack }) {
  const MAP = {
    completed: { t: "Completed", tone: "ok" },
    failed: { t: "Failed", tone: "err" },
    cancelled: { t: "Cancelled", tone: "cancel" },
  };
  const m = MAP[outcome];
  return (
    <div class={`zw-banner is-${m.tone}`}>
      <div class="zw-banner-l1">
        <Mark status={m.tone === "ok" ? "ok" : m.tone === "err" ? "err" : "cancel"} />
        <span class="zw-banner-t">{m.t}</span>
        <span class="zw-banner-m zl-data">
          {outcome === "completed" && "4m12s · $0.42 · ↑18.2k ↓3.1k"}
          {outcome === "failed" && "1m38s · $0.11 · ↑6.4k ↓410"}
          {outcome === "cancelled" && "1m03s · $0.08 · you stopped it"}
        </span>
      </div>
      {outcome === "completed" && (
        <>
          <p class="zw-banner-b">Es la carrera, no el índice: <code class="zl-data">Delete</code> quita la entrada antes de comprobarla. El fix es correcto; queda deuda en los contadores ya guardados.</p>
          <div class="zw-banner-acts">
            <button type="button" class={`zw-btn${phone ? " is-phone" : ""}`}><CopyIcon />Copy result</button>
            <button type="button" class={`zw-btn is-primary${phone ? " is-phone" : ""}`} onClick={onBack}>Back to the conversation</button>
          </div>
        </>
      )}
      {outcome === "failed" && (
        <>
          <Log text={"panic: send on closed channel\n\ngoroutine 41 [running]:\nmoa/pkg/agent.(*Runner).emit(0xc0001a2000, …)\n\t/home/e/dev/moa/pkg/agent/runner.go:212 +0x1a4"} />
          <div class="zw-banner-acts">
            <button type="button" class={`zw-btn${phone ? " is-phone" : ""}`}><CopyIcon />Copy error</button>
            <button type="button" class={`zw-btn is-primary${phone ? " is-phone" : ""}`} onClick={onBack}>Back to the conversation</button>
          </div>
        </>
      )}
      {outcome === "cancelled" && (
        <>
          <p class="zw-banner-b">It had read 2 files and was searching for <code class="zl-data">refs--</code>. Nothing was written.</p>
          <div class="zw-banner-acts">
            <button type="button" class={`zw-btn is-primary${phone ? " is-phone" : ""}`} onClick={onBack}>Back to the conversation</button>
          </div>
        </>
      )}
    </div>
  );
}

function Bubble({ m }) {
  if (m.who === "you") {
    return (
      <div class="zw-user">
        <div class="zw-user-b">{m.text}</div>
        <span class="zw-user-w zl-data">{m.when}</span>
      </div>
    );
  }
  return <div class="zw-prose"><p>{m.text}</p></div>;
}

function SubagentScreen({ phone, state, onBack }) {
  const [t0] = useState(() => Date.now());
  const live = state === "running";
  const waiting = state === "waiting";
  const now = useNow(live || waiting);
  const terminal = state === "completed" || state === "failed" || state === "cancelled";
  const bodyRef = useRef(null);
  useEffect(() => { if (bodyRef.current) bodyRef.current.scrollTop = bodyRef.current.scrollHeight; }, [state]);
  const facts = [
    ["model", "Terra · high"],
    ["started", "09:41"],
    [waiting ? "waiting" : "elapsed", waiting ? "6m02s" : terminal ? (state === "completed" ? "4m12s" : state === "failed" ? "1m38s" : "1m03s") : fmtElapsed(now - t0 + 134000), waiting ? "wait" : null],
    ["turns", "3"],
    ["tokens", "↑18.2k ↓3.1k"],
    ["spend", "$0.42"],
  ];
  return (
    <>
      <div class={`zw-hd${phone ? " is-phone" : ""}`}>
        <button type="button" class={`zw-back${phone ? " is-phone" : ""}`} onClick={onBack} aria-label="Back to the conversation">
          <BackChevron />
        </button>
        {phone ? (
          /* The phone head carries the child's identity and its ONE job.
             The parent's name is not repeated here: the back chevron already
             says where you came from, and two names in 300px means neither is
             readable (measured: both were truncating). */
          <span class="zw-hd-id">
            <span class="zw-hd-t">
              <span class="zw-dotid" style={`--h:${hueOf("terra")}`} aria-hidden="true" />
              terra
            </span>
            <span class="zw-hd-s">review the diff</span>
          </span>
        ) : (
          <span class="zw-crumb">
            <button type="button" class="zw-crumb-p" onClick={onBack}>Buscar un bug bounty</button>
            <span class="zw-crumb-sep" aria-hidden="true">›</span>
            <span class="zw-crumb-c">
              <span class="zw-dotid" style={`--h:${hueOf("terra")}`} aria-hidden="true" />
              terra
              <span class="zw-crumb-task">· review the diff</span>
            </span>
          </span>
        )}
        <span class="zw-sp" />
        <span class={`zw-chip${waiting ? " is-wait" : ""}`}>{waiting ? "waiting" : "background"}</span>
        {!phone && !terminal && <kbd class="zw-kbd zl-data">esc</kbd>}
        {(live || waiting) && <StopButton phone={phone} />}
      </div>

      <Facts items={facts} />

      <div class="zw-rail" role="group" aria-label="Live siblings">
        <span class="zw-rail-k">also live</span>
        <button type="button" class="zw-rail-c is-on"><span class="zw-dotid" style={`--h:${hueOf("terra")}`} aria-hidden="true" />terra</button>
        <button type="button" class="zw-rail-c"><span class="zw-dotid" style={`--h:${hueOf("luna")}`} aria-hidden="true" />luna</button>
        {!phone && <span class="zw-rail-h zl-data">[ ]</span>}
      </div>

      <div class="zw-body" ref={bodyRef}>
        {SA_TRANSCRIPT.map((m, i) => <Bubble m={m} key={i} />)}
        <Ledger rows={SA_ROWS(live)} dense t0={t0} />
        {(terminal || waiting) && (
          <div class="zw-prose">
            <p>
              {waiting
                ? "El fix es correcto para las sesiones nuevas. Antes de seguir necesito una decisión tuya sobre lo que ya está en disco."
                : "Confirmado: la comprobación tiene que entrar dentro del lock; el test de concurrencia lo demuestra."}
            </p>
          </div>
        )}
        {waiting && <AskCard phone={phone} />}
        {terminal && <Banner outcome={state} phone={phone} onBack={onBack} />}
      </div>

      {(live || waiting) && (
        <div class="zw-dock">
          <NowLine
            phase={waiting ? "waiting" : "working"}
            text={waiting ? "Waiting for your answer" : "Searching pkg/attach for refs--"}
            elapsed={fmtElapsed(now - t0 + 3000)}
          />
          <Steer placeholder={waiting ? "Answer terra" : "Steer terra"} />
        </div>
      )}
    </>
  );
}

/* ── 3 · ASYNC BASH ──────────────────────────────────────────────────────
   The same screen, with the same head and the same facts strip, for the
   other kind of async work. Everything that differs, differs because a
   command is not a conversation:

     · no transcript, no steer: you cannot talk to it. The whole body is the
       output, so the output gets the height the transcript had.
     · the command itself is printed in full at the top, copyable. It is the
       one thing you need to read carefully and the one thing the dock's row
       had to truncate.
     · the facts are its own currency (finding 3): cwd, started, elapsed,
       lines, and -- once it ends -- the exit code, in the mark's colours.
     · following: it sticks to the tail while you have not scrolled. Scroll
       up and it stops and offers to jump back, which is the honest version
       of "it moved while I was reading".

   WAITING, for a command (finding 4, second half): a command cannot ask you
   anything -- moa gives it no stdin. What it CAN do is block on a prompt you
   will never see. So the designed state is not a question, it is a warning:
   after a while with no output the log's status goes amber and says so, and
   offers the only two things that help -- stop it, or open a shell there.
   Marked as a hypothesis: I did not verify that the backend reports stdin
   blocking, and this is inferred from `waiting for output`
   (BashJobLog.jsx:42) plus the absence of any waiting state in the model. */
const BASH_STATES = [
  { id: "running", label: "Running", note: "Live output, following the tail. Green and counting. Stop is two-step." },
  { id: "stalled", label: "No output", note: "Not designed today. After a while with nothing on stdout, amber: the command may be blocked on input you cannot give it. It offers the only two useful moves." },
  { id: "completed", label: "exit 0", note: "The log stays; the banner states the code and the duration. Exit code is a fact of the strip too, in the mark's colours." },
  { id: "failed", label: "exit 1", note: "Red. The last lines are the ones you need, and they are already at the bottom because the log is anchored to its end." },
  { id: "cancelled", label: "Cancelled", note: "Neutral. It says how much it had emitted before you stopped it." },
];

const BASH_CMD = "go test ./... -race -count=1 -timeout 20m 2>&1 | tee /tmp/race-$(date +%s).log";
const BASH_LINES = [
  "ok      moa/pkg/agent            2.418s",
  "ok      moa/pkg/agent/tools      1.902s",
  "ok      moa/pkg/attach           0.312s",
  "ok      moa/pkg/config           0.048s",
  "ok      moa/pkg/mcp              4.771s",
  "ok      moa/pkg/serve            6.204s",
  "ok      moa/pkg/serve/ws         1.118s",
  "ok      moa/pkg/session          3.402s",
  "ok      moa/pkg/store            0.884s",
];
const BASH_FAIL = [
  "--- FAIL: TestBroadcastRace (0.42s)",
  "    ws_test.go:212: race detected during execution of test",
  "==================",
  "WARNING: DATA RACE",
  "Write at 0x00c000188018 by goroutine 51:",
  "  moa/pkg/serve/ws.(*Hub).add()",
  "      /home/e/dev/moa/pkg/serve/ws/hub.go:88 +0x64",
  "FAIL    moa/pkg/serve/ws         1.401s",
  "FAIL",
];

function BashScreen({ phone, state, onBack }) {
  const [t0] = useState(() => Date.now());
  const live = state === "running";
  const stalled = state === "stalled";
  const now = useNow(live || stalled);
  const terminal = state === "completed" || state === "failed" || state === "cancelled";
  const [n, setN] = useState(6);
  useEffect(() => {
    if (!live) return;
    const t = setInterval(() => setN((v) => (v >= BASH_LINES.length ? 3 : v + 1)), 1400);
    return () => clearInterval(t);
  }, [live]);

  const lines = live ? BASH_LINES.slice(0, n)
    : stalled ? BASH_LINES.slice(0, 4).concat(["", "› applying migrations to moa_dev…"])
      : state === "failed" ? BASH_LINES.slice(0, 6).concat(BASH_FAIL)
        : state === "cancelled" ? BASH_LINES.slice(0, 5)
          : BASH_LINES.concat(["", "ok      all 14 packages"]);

  const exit = state === "completed" ? ["exit", "0", "ok"] : state === "failed" ? ["exit", "1", "err"] : state === "cancelled" ? ["exit", "stopped", "cancel"] : null;
  const facts = [
    ["cwd", "~/dev/moa/main"],
    ["started", "09:44"],
    [stalled ? "no output for" : "elapsed", stalled ? "3m12s" : terminal ? (state === "completed" ? "6m41s" : state === "failed" ? "2m08s" : "1m22s") : fmtElapsed(now - t0 + 41000), stalled ? "wait" : null],
    ["lines", `${(1051 + lines.length).toLocaleString("en-US")}`],
    ...(exit ? [exit] : []),
  ];

  return (
    <>
      <div class={`zw-hd${phone ? " is-phone" : ""}`}>
        <button type="button" class={`zw-back${phone ? " is-phone" : ""}`} onClick={onBack} aria-label="Back to the conversation">
          <BackChevron />
        </button>
        {phone ? (
          <span class="zw-hd-id">
            <span class="zw-hd-t"><span class="zw-hd-sig zl-data" aria-hidden="true">$</span>bash</span>
            <span class="zw-hd-s zl-data">go test ./... -race</span>
          </span>
        ) : (
          <span class="zw-crumb">
            <button type="button" class="zw-crumb-p" onClick={onBack}>Buscar un bug bounty</button>
            <span class="zw-crumb-sep" aria-hidden="true">›</span>
            <span class="zw-crumb-c"><span class="zw-hd-sig zl-data" aria-hidden="true">$</span>bash</span>
          </span>
        )}
        <span class="zw-sp" />
        <span class={`zw-chip${stalled ? " is-wait" : ""}`}>{stalled ? "no output" : "background"}</span>
        {!phone && !terminal && <kbd class="zw-kbd zl-data">esc</kbd>}
        {(live || stalled) && <StopButton phone={phone} />}
      </div>

      <div class="zw-cmdbox">
        <pre class="zw-cmd is-full zl-data"><span class="zw-cmd-sig">$</span>{BASH_CMD}</pre>
        <button type="button" class={`zw-icobtn${phone ? " is-phone" : ""}`} aria-label="Copy the command"><CopyIcon /></button>
      </div>

      <Facts items={facts} />

      <div class="zw-body is-log">
        <div class="zw-big-head zl-data">… 1 051 earlier lines not shown</div>
        <Log text={lines.join("\n")} tall follow />
        {(live || stalled) && (
          <div class={`zw-logstate${stalled ? " is-wait" : ""}`}>
            <span class={`zw-now-dot is-${stalled ? "waiting" : "working"}`} aria-hidden="true" />
            {stalled
              ? <span class="zw-logstate-t">No output for <span class="zl-data">3m12s</span>. It may be blocked on input; moa gives a background command no stdin.</span>
              : <span class="zw-logstate-t">Following the output. <span class="zl-data">{fmtElapsed(now - t0 + 41000)}</span></span>}
          </div>
        )}
        {stalled && (
          <div class="zw-banner-acts is-flush">
            <button type="button" class={`zw-btn${phone ? " is-phone" : ""}`}>Stop the command</button>
            <button type="button" class={`zw-btn${phone ? " is-phone" : ""}`}>Open a shell here</button>
          </div>
        )}
        {terminal && (
          <div class={`zw-banner is-${state === "completed" ? "ok" : state === "failed" ? "err" : "cancel"} is-flat`}>
            <div class="zw-banner-l1">
              <Mark status={state === "completed" ? "ok" : state === "failed" ? "err" : "cancel"} />
              <span class="zw-banner-t">
                {state === "completed" && "Finished · exit 0"}
                {state === "failed" && "Failed · exit 1"}
                {state === "cancelled" && "Cancelled"}
              </span>
              <span class="zw-banner-m zl-data">
                {state === "completed" && "6m41s · 1 060 lines"}
                {state === "failed" && "2m08s · 1 066 lines"}
                {state === "cancelled" && "1m22s · 1 056 lines emitted"}
              </span>
            </div>
            <div class="zw-banner-acts">
              <button type="button" class={`zw-btn${phone ? " is-phone" : ""}`}><CopyIcon />Copy output</button>
              <button type="button" class={`zw-btn is-primary${phone ? " is-phone" : ""}`} onClick={onBack}>Back to the conversation</button>
            </div>
          </div>
        )}
      </div>
    </>
  );
}

/* ── Lab chrome ──────────────────────────────────────────────────────── */
function Seg({ label, options, value, onChange }) {
  const cur = options.find((o) => o.id === value);
  return (
    <div class="zl-lab-ctl">
      <div class="zl-lab-seg" role="radiogroup" aria-label={label}>
        {options.map((o) => (
          <button
            type="button"
            role="radio"
            aria-checked={o.id === value}
            class={`zl-lab-opt${o.id === value ? " is-on" : ""}`}
            onClick={() => onChange(o.id)}
            key={o.id}
          >{o.label}</button>
        ))}
      </div>
      {cur?.note && <p class="zl-lab-note">{cur.note}</p>}
    </div>
  );
}

function Section({ id, n, title, children, blurb }) {
  return (
    <section class="zw-sec" id={id}>
      <div class="zw-sec-hd">
        <span class="zw-sec-n zl-data">{n}</span>
        <h2>{title}</h2>
      </div>
      <p class="zw-sec-b">{blurb}</p>
      {children}
    </section>
  );
}

export function WorkLab() {
  useEffect(() => {
    document.documentElement.setAttribute("data-ambient", "on");
    return () => document.documentElement.removeAttribute("data-ambient");
  }, []);
  const [sa, setSa] = useState(() => new URLSearchParams(location.search).get("sa") || "running");
  const [bash, setBash] = useState(() => new URLSearchParams(location.search).get("bash") || "running");
  const goBash = () => {
    setBash("completed");
    document.getElementById("zw-bash")?.scrollIntoView({ behavior: "smooth", block: "start" });
  };
  const noop = () => {};

  return (
    <div class="zl zw">
      <div class="zl-aurora" aria-hidden="true" />
      <header class="zl-head">
        <h1>Work</h1>
        <p>
          What the agent did, in line; and the two places work runs away to.
          The live bar over the composer lists them; a row is the door; this is
          what is behind the door.
        </p>
      </header>

      <Section
        id="zw-tools"
        n="1"
        title="Tool calls"
        blurb="One recessed slab per run of consecutive calls. A row says the tool, its object and its result; only a row with something to open carries a chevron, and a row with nothing to open is not a button at all. Four terminal marks, because a call can end in four different ways — and cancelled, which the ledger has no state for today, is grey on purpose: you stopped it, so there is nothing to report."
      >
        <div class="zw-stage">
          <Frame kind="phone" title="Phone">
            <div class="zw-scroll"><ToolSpecimen dense onOpenOutput={goBash} /></div>
          </Frame>
          <Frame kind="desk" title="Desktop · conversation column">
            <div class="zw-scroll"><ToolSpecimen onOpenOutput={goBash} /></div>
          </Frame>
        </div>
      </Section>

      <Section
        id="zw-sub"
        n="2"
        title="Subagent"
        blurb="A delegated conversation, so it takes the centre: the same back, the same crumb, its own transcript, its own composer while it lives. On the phone it is a pushed screen with a chevron; on desktop it replaces the conversation column and esc is the arrow."
      >
        <Seg label="Subagent state" options={SA_STATES} value={sa} onChange={setSa} />
        <div class="zw-stage">
          <Frame kind="phone" title="Phone">
            <SubagentScreen phone state={sa} onBack={noop} />
          </Frame>
          <Frame kind="desk" title="Desktop · conversation column">
            <SubagentScreen state={sa} onBack={noop} />
          </Frame>
        </div>
      </Section>

      <Section
        id="zw-bash"
        n="3"
        title="Background command"
        blurb="Same head, same facts strip, different currency: a command spends wall time and returns a code, so that is what it prints — printing a cost it never had would be a lie. The body is all output because there is nothing to say to it."
      >
        <Seg label="Command state" options={BASH_STATES} value={bash} onChange={setBash} />
        <div class="zw-stage">
          <Frame kind="phone" title="Phone">
            <BashScreen phone state={bash} onBack={noop} />
          </Frame>
          <Frame kind="desk" title="Desktop · conversation column">
            <BashScreen state={bash} onBack={noop} />
          </Frame>
        </div>
      </Section>
    </div>
  );
}
