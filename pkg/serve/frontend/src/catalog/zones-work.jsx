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
   A subagent is a DELEGATED ERRAND. While it lives, the screen follows the
   errand; when it ends, the screen IS its report. The internal conversation
   is the record of how it got there, not the identity of the screen.

   That fixes the hierarchy, and the hierarchy fixes the layout:
     1  the errand        what you asked for. It is the title.
     2  state or result   what it is doing, needs, or concluded.
     3  work log          the messages and tools that prove it.
     4  run details       model, tokens, cost, ids.

   What the previous draft did wrong, measured: on the phone a head plus a
   permanent facts strip ate 124 of 780px before one word of the work. Model,
   started, turns, tokens and spend answer none of the four questions you
   arrive with; they are audit, so they moved to Run details at the bottom.
   Duration is the one that survives near the state, because "how long has
   this been going" IS a question you arrive with.

   Running and terminal are the same object on the same route, so they share
   the head. They do NOT share the composition: a live errand opens at its
   live end and offers a composer; a finished one opens at the top with the
   result and folds the log away. Same screen, two shapes, one transition.

   Desktop DECISION unchanged: it replaces the conversation column and does
   not open as a modal or a second pane. Two composers on screen and no way
   to say which one Enter belongs to is the failure mode a pane would buy. */
const SA_STATES = [
  { id: "running", label: "Running", note: "Direct. The title is the errand, green says it works, the duration rides with the state. Now-line and the steer composer at the foot; the body opens at the live end. Model and money are not here: they are in Run details." },
  { id: "waiting", label: "Waiting for you", note: "Amber and FIXED: no breathing, no counter. A counter next to a parked run claims progress that is not happening. The question and its answers come first; the record stays below. One place to answer — the card — and no duplicate composer." },
  { id: "completed", label: "Completed", note: "A report. Neutral mark, never green: green means running here. Duration, then the whole result. The work log is a closed disclosure below it, and Run details closes the page. Nothing is pinned to the bottom: the report wins that space." },
  { id: "failed", label: "Failed", note: "Same shape as the report, but the literal error IS the result: verbatim, red, copyable. A failure you cannot paste is a failure you cannot report." },
  { id: "cancelled", label: "Cancelled", note: "Neutral, not red. It states the last real progress and stops there: the system cannot prove that nothing was written, so it does not say so." },
];

const SA_ERRAND = "Revisa el diff de pkg/attach y dime si el fix del borrado tapa la carrera o solo el síntoma.";
const SA_TITLE = "review the diff";
const SA_PARENT = "Buscar un bug bounty";

const SA_TRANSCRIPT = [
  { who: "you", text: SA_ERRAND, when: "09:41" },
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

/* ONE door out, and it says where it leads. Desktop prints the parent's own
   name; the phone prints `Parent`, because 390px cannot spend 22 characters
   on the way back without shortening the errand, and the errand is the title.
   Both carry the same full accessible name, so a screen reader hears the
   destination in either density. Esc still works on desktop; the `esc`
   capsule does not, because a shortcut does not deserve permanent pixels.
   No second "Back to the conversation" at the foot of the report: one door. */
function BackHome({ phone, parent, onBack }) {
  return (
    <button
      type="button"
      class={`zw-home${phone ? " is-phone" : ""}`}
      onClick={onBack}
      aria-label={`Back to ${parent}`}
    >
      <BackChevron />
      <span class="zw-home-t">{phone ? "Parent" : parent}</span>
    </button>
  );
}

/* The state word, and the ONE number allowed to travel with it. Terminal
   states carry no dot: a finished run has no pulse, and a green tick on
   "Completed" would spend the running colour on something that has stopped.
   Green breathes, amber does not move at all.

   Completed and cancelled get a MARK and no word: the report's own headline
   ("Completed in 4m12s") is 30px below and says it better, and repeating the
   word in both places is the duplication the head was supposed to end.
   Running, Waiting and Failed keep the word, because in those three the head
   is the only place that carries the state while you scroll. */
function StateWord({ tone, word, time, markOnly }) {
  const dot = tone === "running" || tone === "waiting";
  return (
    <span class={`zw-state is-${tone}`}>
      {dot && <span class={`zw-now-dot is-${tone === "running" ? "working" : "waiting"}`} aria-hidden="true" />}
      {markOnly
        ? <><Mark status={markOnly} /><span class="zw-sr">{word}</span></>
        : <span class="zw-state-w">{word}</span>}
      {time && <span class="zw-state-t zl-data">{time}</span>}
    </span>
  );
}

/* The head of both screens. One region, one hairline, and the title is the
   WORK -- the errand for a subagent, the command for a process. The agent
   and its model are the line that truncates first; the errand never does.

   `copy` makes the title itself the copy target, which is what a command
   needs: it is the one thing on that screen you take away verbatim, and a
   16px icon beside 76 characters of shell is a worse target than the 76
   characters. An errand is prose and is not copied, so it stays an h3. */
function WorkHead({ phone, parent, onBack, title, titleMono, copy, sub, state, right }) {
  const cls = `zw-hd-title${titleMono ? " zl-data" : ""}`;
  return (
    <div class={`zw-hd${phone ? " is-phone" : ""}`}>
      <div class="zw-hd-top">
        <BackHome phone={phone} parent={parent} onBack={onBack} />
        {state}
        <span class="zw-sp" />
        {right}
      </div>
      {copy
        ? (
          <button type="button" class={`${cls} is-copy${phone ? " is-phone" : ""}`} aria-label={copy}>
            <span class="zw-hd-title-t">{title}</span>
            <CopyIcon />
          </button>
        )
        : <h3 class={cls}>{title}</h3>}
      {sub && <p class="zw-hd-sub">{sub}</p>}
    </div>
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

/* Waiting for you, as an interruption and not as a slower kind of running.
   The card goes FIRST, above the record, because a question buried under a
   long transcript is a question you answer late. Amber, and completely
   still: no shimmer, no elapsed-as-work. `since 09:47` is a timestamp, not a
   counter -- it states when it stopped, it does not animate towards
   anything. The card is the only place to answer: the steer composer is gone
   in this state, because two routes to one action means guessing which one
   the run is listening to. */
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

/* A disclosure. Closed by default on a finished run: the record is evidence,
   and evidence is what you open when the report is not enough. It never
   loses anything -- the transcript and the full ledger are inside. */
function Disclosure({ label, count, children, openInit = false }) {
  const [open, setOpen] = useState(openInit);
  return (
    <div class={`zw-disc${open ? " is-open" : ""}`}>
      <button type="button" class="zw-disc-b" onClick={() => setOpen((v) => !v)} aria-expanded={open}>
        <span class={`zw-chev${open ? " is-open" : ""}`} aria-hidden="true">
          <svg viewBox="0 0 12 12"><path d="M4 2.5L7.5 6 4 9.5" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" /></svg>
        </span>
        <span class="zw-disc-t">{label}</span>
        {count && <span class="zw-disc-n zl-data">{count}</span>}
      </button>
      {open && <div class="zw-disc-body">{children}</div>}
    </div>
  );
}

/* Run details: the audit, at the end, where audit belongs. Printed rows, not
   boxes -- boxes are for what you press -- except the ids, which you press to
   copy, and therefore look pressable. */
function RunDetails({ phone, rows, ids }) {
  return (
    <Disclosure label="Run details">
      <dl class="zw-rd">
        {rows.map(([k, v, tone]) => (
          <div key={k}>
            <dt>{k}</dt>
            <dd class={`zl-data${tone ? ` is-${tone}` : ""}`}>{v}</dd>
          </div>
        ))}
      </dl>
      {ids && (
        <div class="zw-ids">
          {ids.map(([k, v]) => (
            <button type="button" class={`zw-id${phone ? " is-phone" : ""}`} key={k} aria-label={`Copy ${k}: ${v}`}>
              <span class="zw-id-k">{k}</span>
              <span class="zw-id-v zl-data">{v}</span>
              <CopyIcon />
            </button>
          ))}
        </div>
      )}
    </Disclosure>
  );
}

/* The report. Terminal runs are not a banner stapled to the bottom of a
   transcript: the outcome sentence and the result ARE the page, and they get
   the top of it.

   The state lives in the head, not here: a neutral mark for completed and
   cancelled, the red word for failed. Completed is never green -- in this
   system green means RUNNING, and a green tick on something that stopped
   teaches the eye that green is decoration.

   `Copy result` is secondary and alone: there is no mauve button to go back,
   because the door is already in the head and a report has no primary action
   other than being read. */
function Report({ outcome, phone }) {
  const word = outcome === "completed" ? "Completed in 4m12s" : outcome === "failed" ? "Failed after 1m38s" : "Cancelled after 1m03s";
  return (
    <div class={`zw-report is-${outcome}`}>
      <div class="zw-report-h">
        {/* No mark here. The head above carries the persistent state -- a
            neutral mark for completed/cancelled, the red word for failed --
            and this is a headline sentence, which does not need a glyph to
            repeat it 100px lower. One state, one persistent place, one
            contextual expression. */}
        <span class="zw-report-t">{word}</span>
      </div>
      {outcome === "completed" && (
        <>
          <div class="zw-result">
            <p>Es la carrera, no el índice: <code class="zl-data">Delete</code> quita la entrada del índice antes de comprobar que existe, así que dos borrados concurrentes del mismo id dejan el blob sin dueño.</p>
            <p>El fix del diff es correcto: mueve la comprobación dentro del lock y el test de concurrencia lo demuestra. Queda deuda: los contadores <code class="zl-data">refs</code> ya guardados en disco pueden estar en −1 y nadie los migra.</p>
          </div>
          <div class="zw-report-acts">
            <button type="button" class={`zw-btn${phone ? " is-phone" : ""}`}><CopyIcon />Copy result</button>
          </div>
        </>
      )}
      {outcome === "failed" && (
        <>
          {/* The error verbatim, and it is the result -- not a summary of it,
              not the first two lines with the rest folded away. */}
          <Log text={"panic: send on closed channel\n\ngoroutine 41 [running]:\nmoa/pkg/agent.(*Runner).emit(0xc0001a2000, 0x1, 0xc0004a1e00)\n\t/home/e/dev/moa/pkg/agent/runner.go:212 +0x1a4\nmoa/pkg/agent.(*Runner).Step(0xc0001a2000)\n\t/home/e/dev/moa/pkg/agent/runner.go:158 +0x2c8\ncreated by moa/pkg/agent.Spawn\n\t/home/e/dev/moa/pkg/agent/spawn.go:74 +0x11c"} tall />
          <div class="zw-report-acts">
            <button type="button" class={`zw-btn${phone ? " is-phone" : ""}`}><CopyIcon />Copy error</button>
          </div>
        </>
      )}
      {outcome === "cancelled" && (
        /* What it had actually done, and nothing more. The earlier draft said
           "Nothing was written", which the system cannot prove: the run had
           executed three tools and any of them could have touched disk. A
           screen that guarantees what it does not know is worse than a screen
           that stops at the last fact. */
        <div class="zw-result">
          <p>Last progress: it had read <code class="zl-data">store.go</code> and <code class="zl-data">store_test.go</code>, run <code class="zl-data">git diff</code>, and was searching for <code class="zl-data">refs--</code>. It reached no conclusion.</p>
        </div>
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

/* The record. In a terminal run this is what the disclosure holds; while the
   run is parked it stays open, because you may need it to answer. The last
   agent message is NOT repeated as a result: if the final message and the
   result payload are the same thing, it is painted once. Duplication is not
   traceability. */
function WorkLog({ live, t0 }) {
  return (
    <>
      {SA_TRANSCRIPT.map((m, i) => <Bubble m={m} key={i} />)}
      <Ledger rows={SA_ROWS(live)} dense t0={t0} />
    </>
  );
}

function SubagentScreen({ phone, state, onBack }) {
  const [t0] = useState(() => Date.now());
  const live = state === "running";
  const waiting = state === "waiting";
  const now = useNow(live);
  const terminal = state === "completed" || state === "failed" || state === "cancelled";
  const bodyRef = useRef(null);
  /* A live run opens at its live end; a report opens at the top. Same route,
     opposite anchors, and this is the moment the screen changes shape. */
  useEffect(() => {
    if (!bodyRef.current) return;
    bodyRef.current.scrollTop = terminal || waiting ? 0 : bodyRef.current.scrollHeight;
  }, [state, terminal, waiting]);

  const dur = state === "completed" ? "4m12s" : state === "failed" ? "1m38s" : state === "cancelled" ? "1m03s" : null;
  const details = [
    ["model", "Terra · high"],
    ["mode", "background"],
    ["started", "09:41"],
    ["duration", waiting ? "6m02s (parked 4m18s)" : dur || fmtElapsed(now - t0 + 134000)],
    ["turns", terminal ? "3" : "2"],
    ["tokens", terminal ? "↑18.2k ↓3.1k" : "↑11.4k ↓1.9k"],
    ["cost", terminal ? "$0.42" : "$0.26"],
  ];
  const ids = [["Job ID", "sa-9559b3b23433"], ["Parent", "ses-3f1a0c77"]];

  return (
    <>
      <WorkHead
        phone={phone}
        parent={SA_PARENT}
        onBack={onBack}
        title={SA_TITLE}
        sub={<><span class="zw-dotid" style={`--h:${hueOf("terra")}`} aria-hidden="true" />terra · high</>}
        state={
          live ? <StateWord tone="running" word="Running" time={fmtElapsed(now - t0 + 134000)} />
            : waiting ? <StateWord tone="waiting" word="Waiting for you" />
              : state === "failed"
                ? <StateWord tone="failed" word="Failed" />
                : <StateWord tone="neutral" word={state === "completed" ? "Completed" : "Cancelled"} markOnly={state === "completed" ? "ok" : "cancel"} />
        }
        right={(live || waiting) && <StopButton phone={phone} />}
      />

      <div class="zw-body" ref={bodyRef}>
        {waiting && <AskCard phone={phone} />}
        {terminal && <Report outcome={state} phone={phone} />}

        {terminal
          ? (
            <Disclosure label="Work log" count={`${SA_ROWS(false).length} actions`}>
              <WorkLog live={false} t0={t0} />
            </Disclosure>
          )
          : (
            <>
              {waiting && <p class="zw-logsep">Work log</p>}
              <WorkLog live={live} t0={t0} />
              {waiting && (
                <div class="zw-prose">
                  <p>El fix es correcto para las sesiones nuevas. Antes de seguir necesito una decisión tuya sobre lo que ya está en disco.</p>
                </div>
              )}
            </>
          )}

        {terminal && <RunDetails phone={phone} rows={details} ids={ids} />}
      </div>

      {/* Nothing is pinned in a terminal state: the report gets that space.
          Waiting pins nothing either -- the card is the answer field. */}
      {live && (
        <div class="zw-dock">
          <NowLine phase="working" text="Searching pkg/attach for refs--" elapsed={fmtElapsed(now - t0 + 3000)} />
          <Steer placeholder="Steer terra" />
        </div>
      )}
    </>
  );
}

/* ── 3 · ASYNC BASH ──────────────────────────────────────────────────────
   A background command is NOT a subagent with different facts. It is the
   CONSOLE OF A PROCESS. It shares the frame of open work -- the same door
   home, a state word, Stop, details -- and nothing else, because a process
   has no errand, no conversation, no model and no bill.

   Its order is its own:
     1  the command, in full, copyable. It is the title, in mono, because it
        is data and every other title here is words.
     2  cwd and state/elapsed/exit, one line, right under it.
     3  the output, monospaced, taking the whole centre and following the
        tail until you scroll away from it.
     4  Stop while it lives; Copy output when it ends.

   Terminal is NOT promoted above the log the way a subagent's report is, and
   that is the point of the distinction: in bash the exit code IS the result.
   There is no semantic conclusion to lift to the top -- the last lines of
   output are the conclusion, and they are already at the bottom because the
   log is anchored to its end.

   NO OUTPUT is a neutral fact, not a warning and not "waiting". Absence of
   stdout does not demonstrate that a process is blocked on stdin, and moa
   gives a background command no stdin to block on. So it is printed in the
   log's own status line, in the same ink as the rest of the line, and it
   offers nothing: there is no action that a fact this uncertain justifies. */
const BASH_STATES = [
  { id: "running", label: "Running", note: "The console. The command is the title, the output owns the centre, and it follows the tail. Green and counting, Stop is two-step. No tokens, no cost, no transcript: a process has none of those." },
  { id: "stalled", label: "No output", note: "A neutral fact, deliberately not amber and deliberately not called waiting: silence on stdout does not prove the process is blocked, and moa gives it no stdin anyway. The command is still running and still says so." },
  { id: "completed", label: "exit 0", note: "The exit code is the result, so it stays on the status line where it has been all along — it is not lifted into a report, because there is no conclusion to lift. Copy output replaces Stop." },
  { id: "failed", label: "exit 1", note: "Red on the code, and the lines you need are already at the bottom because the log is anchored to its end. Nothing gets summarised." },
  { id: "cancelled", label: "Cancelled", note: "Neutral. It states how much it had emitted before you stopped it, and stops there." },
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

  const dur = state === "completed" ? "6m41s" : state === "failed" ? "2m08s" : state === "cancelled" ? "1m22s" : fmtElapsed(now - t0 + 41000);
  /* The exit code is the result of this screen, so it is the state word --
     not a banner, not a fact hidden in a strip. Cancelled has no code. */
  const stateWord = state === "completed" ? { tone: "exit-ok", word: "exit 0" }
    : state === "failed" ? { tone: "exit-err", word: "exit 1" }
      : state === "cancelled" ? { tone: "neutral", word: "Cancelled" }
        : { tone: "running", word: "Running" };

  return (
    <>
      <WorkHead
        phone={phone}
        parent={SA_PARENT}
        onBack={onBack}
        title={<><span class="zw-cmd-sig" aria-hidden="true">$</span>{BASH_CMD}</>}
        titleMono
        copy="Copy the command"
        sub={<>
          <span class="zl-data">~/dev/moa/main</span>
          <span class="zw-hd-sep" aria-hidden="true">·</span>
          <span class="zl-data">{(1051 + lines.length).toLocaleString("en-US")} lines</span>
        </>}
        state={<StateWord tone={stateWord.tone} word={stateWord.word} time={dur} />}
        right={
          (live || stalled)
            ? <StopButton phone={phone} />
            : <button type="button" class={`zw-btn is-small${phone ? " is-phone" : ""}`}><CopyIcon />Copy output</button>
        }
      />

      <div class="zw-body is-log">
        <div class="zw-big-head zl-data">… 1 051 earlier lines not shown</div>
        <Log text={lines.join("\n")} tall follow />
        {/* Only while it lives. Once it has ended the head already says the
            code and the duration, and the log ends where it ends: a second
            sentence under it would be the third place saying the same thing.
            While it lives, this is the ONE contextual expression of state --
            and "No output for 3m12s" is printed in exactly the same ink as
            "Following the output", because silence on stdout is a fact, not
            an alarm: it does not prove the process is blocked, and moa gives
            a background command no stdin to block on. */}
        {(live || stalled) && (
          <div class="zw-logstate">
            <span class="zw-now-dot is-working" aria-hidden="true" />
            <span class="zw-logstate-t">
              {stalled
                ? <>No output for <span class="zl-data">3m12s</span>. The command is still running.</>
                : <>Following the output.</>}
            </span>
            <span class="zw-logstate-el zl-data">{fmtElapsed(now - t0 + 41000)}</span>
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
        blurb="A delegated errand: direct while it runs, a report when it ends. The title is what you asked for, never the agent's name; the agent and its model are the secondary line. Model, tokens and cost are audit, so they live in Run details at the foot — not as a toll strip before the work. One door home, and it says where it goes."
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
        blurb="Not a subagent with different facts: the console of a process. It shares the frame — the same door home, a state word, Stop, Copy output — and nothing else. The command is the title, in mono and copyable; the output owns the centre; the exit code is the result, so it is the state word and there is no report to lift above the log."
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
