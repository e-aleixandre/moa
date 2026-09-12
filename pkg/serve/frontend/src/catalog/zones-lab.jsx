import { useEffect, useRef, useState } from "preact/hooks";
import "./zones-lab.css";
import { FROZEN } from "./fidelity-freeze.js";
/* MIGRATED (METODO §4, piece 1 of the list): the session row no longer has a
   private copy here. Its markup and its CSS were MOVED to
   components/SessionRow, class names and all, and the prototype imports them
   back. There is one definition now, and a change to it can only land in one
   place. The dot comes with it, because a dot alone is not a piece. */
import { Dot } from "../components/SessionRow/SessionRow.jsx";
/* Same move, the composer: markup and CSS live in layout/Composer now, and
   the prototype draws the shipped one. See the adapter at `Composer`. */
import { Composer as ProductionComposer } from "../layout/Composer/Composer.jsx";
/* Same move, the settings sheet: markup and CSS live in
   components/GlobalSettings now, and the prototype draws the shipped one. */
import { GlobalSettings } from "../components/GlobalSettings/GlobalSettings.jsx";
/* Same move, the session panel: markup and CSS live in
   components/SessionPanel now, and the prototype draws the shipped one.
   See the adapter at `SessionPanel`. */
import { SessionPanel as ProductionSessionPanel } from "../components/SessionPanel/SessionPanel.jsx";
import { PANEL_PAGES } from "../data/session-panel.js";
/* Same move, the transcript: markup and CSS live in layout/Stream,
   components/UserWaypoint and components/AssistantDocument now, and the
   prototype draws the shipped ones. See the adapters at `Transcript`,
   `UserMessage`, `StreamingProse`. */
import { Transcript as ProductionTranscript } from "../layout/Stream/Stream.jsx";
import { UserWaypoint } from "../components/UserWaypoint/UserWaypoint.jsx";
import { AssistantDocument, Prose } from "../components/AssistantDocument/AssistantDocument.jsx";
/* Same move, the live zone: markup and CSS live in layout/LiveBar now, and
   the prototype draws the shipped one. See the adapter at `LiveZone`. */
import { LiveBar as ProductionLiveBar } from "../layout/LiveBar/LiveBar.jsx";
import { formatElapsed } from "../data/util/activity.js";
/* CtxRing moved with the status line (layout/StatusStrip). The prototype's
   StatusLine still draws the gauges itself; the ring is the one piece of
   that line that already has a single definition. */
import { CtxRing } from "../layout/StatusStrip/StatusStrip.jsx";
/* Same move, the model and permission pickers: markup and CSS live in
   ModelSelector / PermissionControl now, and the prototype draws the shipped
   ones. See the adapters at `ModelPicker`, `PermPicker`, `Popover`, `Sheet`. */
import { ModelSelector as ProductionModelSelector, PickerPopover, PickerSheet } from "../components/ModelSelector/ModelSelector.jsx";
import { PermissionOptions } from "../components/PermissionControl/PermissionControl.jsx";
/* Same move, the sidebar: markup and CSS live in layout/Sidebar now, and
   the prototype draws the shipped one. See the adapter at `Sidebar`. */
import { Sidebar as ProductionSidebar } from "../layout/Sidebar/Sidebar.jsx";
import { projectMonogram } from "../data/util/format.js";

/* The three-zone skeleton, both densities side by side.
   This is a PROTOTYPE, not production: it draws the shell only (where things
   live and how they open), with the real transcript stubbed as grey bars.
   Spatial grammar: the LEFT edge is the other sessions, the RIGHT edge is
   this session, the bottom is state. Both drawers use the same motion and the
   same gesture, mirrored, so learning one teaches the other. */

/* Every session carries the project it lives in, as a coloured monogram. The
   references all put an icon on every row; a chat client has no icon per
   conversation, but it does have a folder -- and that is the thing you
   actually navigate by, so it earns the slot. Colour is derived from the
   project name, so the same repo always looks the same. State is a separate
   datum and lives in the dot next to the age. */
/* The state vocabulary is PRODUCTION's, not the prototype's: what this file
   used to call "needs" is `permission` everywhere else in the product
   (data/util/format.js sessionDisplayDotState), and the row these fixtures now
   feed is production's. One word per state, or the migrated piece would need a
   translation table at its door — which is the exact thing being removed. */
const SESSIONS = [
  { title: "Buscar un bug bounty", when: "now", path: "~/dev/moa", project: "moa", state: "running", brief: "Running · 4m", tone: "neutral" },
  { title: "Check access to two repos", when: "28m", path: "~/dev/gugo", project: "gugo", state: "permission", brief: "Needs your answer", tone: "yellow" },
  { title: "Deploy fails on ARM runner", when: "1h", path: "~/dev/tienda", project: "tienda", state: "error", brief: "Stopped with an error", tone: "red" },
  { title: "Resumen de la factura de octubre", when: "12m", path: "~/dev/moa", project: "moa", state: "unseen", brief: "Answered · not read yet", tone: "mauve" },
  { title: "Limpiar Docker y worktrees", when: "35m", path: "~/dev", project: "dev", state: "idle" },
  { title: "Búscame un dominio para el side project", when: "36m", path: "~/dev", project: "dev", state: "idle" },
  { title: "Browse Gugo GitLab", when: "39d", path: "~/dev/gugo", project: "gugo", state: "idle" },
  { title: "MenuApp", when: "41d", path: "~/dev/menuapp", project: "menuapp", state: "idle" },
];

function PlusIcon() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true">
      <path d="M8 3.5v9M3.5 8h9" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" />
    </svg>
  );
}

/* MIGRATED (METODO §4, the sidebar): the left column has no private copy here.
   Its markup and CSS were MOVED to layout/Sidebar, class names and all, and
   the prototype imports them back. What sits here now is only an adapter: the
   prototype's fixtures mapped onto the props the shipped column takes.

   Search FILTERS this list; the ⌘K keycap is decoration in the lab (production
   wires it to the palette). Inbox, version and settings are the things about
   the APP, so they live in the foot — the same place production keeps them. */
const SIDEBAR_ROWS = SESSIONS.map((s, i) => ({
  id: `zl-${i}`,
  title: s.title,
  state: s.state,
  unseen: s.state === "unseen",
  when: s.when,
  brief: s.brief || "",
  briefTone: s.tone || "",
  path: s.brief ? "" : s.path,
  cwd: s.path,
  mono: projectMonogram(s.path) || { text: s.project.slice(0, 2), hue: 210 },
  saved: s.state === "idle",
}));
const SIDEBAR_ACTIVE = SIDEBAR_ROWS.filter((s) => !s.saved);
const SIDEBAR_SAVED = SIDEBAR_ROWS.filter((s) => s.saved);

/* Identity hues, used by the live-zone fixtures (a name is always the same
   colour). The sidebar's monograms now come from production's projectMonogram. */
const HUES = [210, 265, 170, 320, 40, 190];
function projectHue(name) {
  let h = 0;
  for (let i = 0; i < (name || "").length; i++) h = (h * 31 + name.charCodeAt(i)) >>> 0;
  return HUES[h % HUES.length];
}

function Sidebar({ onPick, desktop, density = "desktop", onSettings, view, onView }) {
  return (
    <ProductionSidebar
      density={density}
      jump={!!desktop}
      version={{ current: "v0.37.2" }}
      active={SIDEBAR_ACTIVE}
      saved={SIDEBAR_SAVED}
      activeId={SIDEBAR_ROWS[0].id}
      inboxVisible
      inboxCount={1}
      groupByProject={view === "project"}
      onGroupByProject={(folder) => onView?.(folder ? "project" : "recent")}
      onSelectSession={() => onPick?.()}
      onSettings={onSettings}
    />
  );
}

/* ── Global settings ───────────────────────────────────────────────────────
   MIGRATED (METODO §4): the sheet has no private copy here. Its markup and
   its CSS were MOVED to components/GlobalSettings, class names and all, and
   the prototype imports them back (see the import at the top of this file).

   The prototype drew four sections and two of them were placeholders: a "80%"
   that was a picture of a number, and an "Inherit the model" switch for a
   setting that does not exist. What ships in their place are the settings
   production actually has — the compaction threshold, what the agent is told
   on the way there, the summarizing model and the subagent allowlist — each
   as one row in THIS grammar, with its options on a page pushed inside the
   same sheet. So this file now renders four real settings where it used to
   draw two fictional ones, in the shape the owner accepted.

   Deliberately NOT in the session panel: these outlive every session, and the
   panel means "this one". A centred sheet on the desktop and a bottom sheet
   on the phone, because it belongs to no edge: the left is other sessions,
   the right is this one. */

/* ── The right drawer: this session's dossier ──────────────────────────────
   MIGRATED (METODO §4): the panel has no private copy here. Its markup and
   its CSS were MOVED to components/SessionPanel, class names and all, and
   the prototype imports them back. What sits here now is only an adapter:
   the prototype's fixtures mapped onto the props the shipped panel takes.

   The rule is unchanged: the LINE holds the controls for the next turn;
   the PANEL is the dossier of the session. */

const PANEL_CREATED = (() => {
  const d = new Date(Date.now());
  d.setHours(9, 12, 0, 0);
  return d.toISOString();
})();
const PANEL_SESSION = {
  id: "a3f91c…7c2e",
  title: "Buscar un bug bounty",
  cwd: "/home/ealeixandre/dev/moa/main",
  created: PANEL_CREATED,
  state: "running",
  provider: "anthropic",
  fast: true,
  costUSD: 1.84,
  contextPercent: 63,
  contextWindow: 200000,
  runTokensUp: 12400,
  runTokensDown: 1800,
  runTokenHint: false,
  tokenLabel: "↑12.4k ↓1.8k",
  worktree: "design-visual",
  planResetNotes: { week: "resets Mon 09:00" },
  goalActive: true,
  goalIteration: 3,
  tasks: [
    { status: "done" }, { status: "done" },
    { status: "pending" }, { status: "pending" }, { status: "pending" },
  ],
  mcp: { total: 3, unhealthy: 1 },
  messages: Array.from({ length: 14 }, (_, i) => ({ role: "user", id: `u${i}` })),
};
const PANEL_FACTS = [
  { id: "tokens", label: "Tokens", value: "↑12.4k ↓1.8k" },
  { id: "spend", label: "Spend", value: "$1.84" },
  { id: "turns", label: "Turns", value: "14" },
  { id: "fast", label: "Fast", value: "on" },
  { id: "goal", label: "Goal", value: "iteration 3" },
  { id: "tasks", label: "Tasks", value: "2/5" },
];
const PANEL_USAGE = {
  available: true,
  five_hour: { utilization: 62, resets_at: new Date(Date.now() + (2 * 3600 + 10 * 60) * 1000).toISOString() },
  seven_day: { utilization: 31, resets_at: new Date(Date.now() + 4 * 86400000).toISOString() },
  extra_usage: { is_enabled: true, used_credits: 420, monthly_limit: 2000, currency: "USD", decimal_places: 2 },
};
const PANEL_MCP = [
  { name: "github", tools: 12, state: "ready" },
  { name: "playwright", tools: 24, state: "ready" },
  {
    name: "linear", tools: 0, state: "failed",
    error: "spawn npx ENOENT — is Node on PATH for the service?",
    foot: "stdio · 2 restarts",
    whys: {
      session: "Only this conversation, until it ends",
      project: "Whenever you work in ~/dev/moa",
      global: "Every project and future session",
    },
  },
];
const PANEL_ARTIFACTS = [
  { id: "1", name: "attach-race-report.md", sizeLabel: "4.2 kB", when: "09:28" },
  { id: "2", name: "race-test.log", sizeLabel: "1.1 kB", when: "09:33" },
  { id: "3", name: "coverage.html", sizeLabel: "38 kB", when: "09:34" },
];

function SessionPanel({ onClose, page = "root", onPage, open = true, style }) {
  return (
    <ProductionSessionPanel
      session={PANEL_SESSION}
      usage={PANEL_USAGE}
      open={open}
      page={page}
      onClose={onClose}
      onPage={onPage}
      mcpServers={PANEL_MCP}
      artifacts={PANEL_ARTIFACTS}
      facts={PANEL_FACTS}
      inline
      style={style}
    />
  );
}

/* ── Transcript ──────────────────────────────────────────────────────────
   Representative content, not grey bars. The user's message is the only
   thing with a peach edge; the assistant's turn has no frame at all -- it is
   the page. Tool work and deliverables are objects ON the page: ledger
   (recessed, sheet tone) and artifact (raised, the one thing you take away). */

const SMALL_ICONS = {
  read: <path d="M3.5 2.5h6l3 3v8h-9z M9.5 2.5v3h3" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round" />,
  grep: <><circle cx="7" cy="7" r="4" fill="none" stroke="currentColor" stroke-width="1.5" /><path d="M10 10l3.5 3.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" /></>,
  bash: <path d="M3 4l4 4-4 4M8.5 12H13" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />,
  edit: <path d="M11.5 2.5l2 2L6 12H4v-2z" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round" />,
  write: <path d="M3.5 2.5h6l3 3v8h-9z M8 7v4M6 9h4" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round" />,
};
function ToolIcon({ tool }) {
  return <svg class="zl-tool-ico" viewBox="0 0 16 16" aria-hidden="true">{SMALL_ICONS[tool] || SMALL_ICONS.bash}</svg>;
}

/* One row of the ledger. Terminated rows with a detail are buttons that open
   it inline; the running row shows its elapsed time in place of a result. */
function LedgerRow({ tool, arg, dim, out, status, detail, open, onToggle, live, elapsed }) {
  const Tag = detail ? "button" : "div";
  return (
    <>
      <Tag
        type={detail ? "button" : undefined}
        class={`zl-lg-row${live ? " is-live" : ""}${open ? " is-open" : ""}`}
        onClick={detail ? onToggle : undefined}
        aria-expanded={detail ? open : undefined}
      >
        <ToolIcon tool={tool} />
        <span class="zl-lg-txt">
          <span class="zl-lg-tool">{tool}</span>
          <span class="zl-lg-arg zl-data">{arg}</span>
          {dim && <span class="zl-lg-dim"> · {dim}</span>}
        </span>
        {live
          ? <span class="zl-lg-out zl-data">{elapsed}</span>
          : out && <span class={`zl-lg-out zl-data${status === "err" ? " is-err" : ""}`}>{out}</span>}
        <span class={`zl-lg-mark is-${live ? "live" : status}`} aria-hidden="true">
          {status === "ok" && !live && <svg viewBox="0 0 12 12"><path d="M2.5 6.5l2.5 2.5 4.5-5" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" /></svg>}
          {status === "err" && <svg viewBox="0 0 12 12"><path d="M3 3l6 6M9 3l-6 6" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" /></svg>}
        </span>
        {detail && (
          <span class="zl-lg-chev" aria-hidden="true">
            <svg viewBox="0 0 12 12"><path d="M4 2.5L7.5 6 4 9.5" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" /></svg>
          </span>
        )}
        {!live && <span class="sr-only">{status === "err" ? "failed" : "completed"}</span>}
        {live && <span class="sr-only">running</span>}
      </Tag>
      {detail && open && <div class="zl-lg-detail">{detail}</div>}
    </>
  );
}

/* Diff detail: the production DiffBlock is a code block with gutter numbers.
   Here it lives INSIDE a ledger row (the product fuses a diff that follows a
   ledger into its rows), so it is recessed one more step, not a new card. */
function Diff() {
  const lines = [
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

function Ledger({ rows, folded: foldedInit = true, dense }) {
  const [open, setOpen] = useState(() => new Set());
  const toggle = (k) => setOpen((v) => { const n = new Set(v); n.has(k) ? n.delete(k) : n.add(k); return n; });
  const [folded, setFolded] = useState(foldedInit && rows.length > 3);
  const hidden = folded ? rows.slice(0, rows.length - 2) : [];
  const shown = folded ? rows.slice(rows.length - 2) : rows;
  const live = rows.some((r) => r.live);
  const failed = rows.some((r) => r.status === "err");
  return (
    <div class={`zl-ledger${live ? " is-live" : ""}${dense ? " is-dense" : ""}`}>
      {rows.length > 3 && (
        <button type="button" class="zl-lg-head" onClick={() => setFolded((v) => !v)} aria-expanded={!folded}>
          <svg class={`zl-lg-chev${folded ? "" : " is-open"}`} viewBox="0 0 12 12" aria-hidden="true">
            <path d="M4 2.5L7.5 6 4 9.5" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" />
          </svg>
          <span class="zl-lg-head-t">
            {folded ? <><span class="zl-data">{hidden.length}</span> earlier actions</> : <><span class="zl-data">{rows.length}</span> actions</>}
          </span>
          {failed && <span class="zl-lg-head-fail"><span class="zl-data">1</span> failed</span>}
        </button>
      )}
      {shown.map((r, i) => (
        <LedgerRow
          key={r.arg + i}
          {...r}
          open={open.has(r.arg)}
          onToggle={() => toggle(r.arg)}
        />
      ))}
    </div>
  );
}

const LEDGER_A = [
  { tool: "grep", arg: "Delete(", dim: "pkg/attach", out: "3 hits", status: "ok" },
  { tool: "read", arg: "pkg/attach/store.go", out: "212 lines", status: "ok" },
  { tool: "read", arg: "pkg/attach/store_test.go", out: "148 lines", status: "ok" },
  { tool: "bash", arg: "go test ./pkg/attach/", out: "exit 1", status: "err", detail: (
    <pre class="zl-log zl-data">{`--- FAIL: TestDeleteMissing (0.00s)
    store_test.go:91: expected ErrNotFound, got <nil>
FAIL
FAIL    moa/pkg/attach  0.014s`}</pre>
  ) },
  { tool: "edit", arg: "pkg/attach/store.go", dim: "+5 −1", out: "ok", status: "ok", detail: <Diff /> },
];
const LEDGER_B = [
  { tool: "bash", arg: "go test ./pkg/attach/", out: "ok", status: "ok", detail: (
    <pre class="zl-log zl-data">{`ok    moa/pkg/attach  0.312s
ok    moa/pkg/attach/store  0.088s`}</pre>
  ) },
  { tool: "bash", arg: "go vet ./...", live: true, status: "ok", elapsed: "4s" },
];
// The same ledger once the turn has finished: the live row has returned.
const LEDGER_B_DONE = LEDGER_B.map((r) => (r.live ? { ...r, live: false, out: "ok", elapsed: undefined } : r));

/* Artifact: a deliverable. Raised one step above the page, a real file
   glyph, and the whole card is the open action -- it is what you take away
   from the turn, so it is the one framed object in the assistant's prose. */
function Artifact({ name, kind, size, dense }) {
  return (
    <button type="button" class={`zl-art${dense ? " is-dense" : ""}`} aria-label={`Open artifact ${name}`}>
      {/* One clean sheet-with-folded-corner. The extension was stamped across
          the glyph, which read as a sticker rather than a file; it belongs in
          the metadata line with the size, where the other facts already are. */}
      <span class="zl-art-ico" aria-hidden="true">
        <svg viewBox="0 0 20 24">
          <path d="M2.75 1h8.5L17.25 7v15.25a.75.75 0 0 1-.75.75h-13a.75.75 0 0 1-.75-.75V1.75A.75.75 0 0 1 2.75 1z" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round" />
          <path d="M11.25 1v5.25a.75.75 0 0 0 .75.75h5.25" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round" />
        </svg>
      </span>
      <span class="zl-art-main">
        <span class="zl-art-name">{name}</span>
        <span class="zl-art-meta zl-data">{kind} · {size}</span>
      </span>
      <span class="zl-art-act" aria-hidden="true">
        <svg viewBox="0 0 16 16">
          <path d="M8 2.5v8M4.5 7L8 10.5 11.5 7M3 13h10" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" />
        </svg>
      </span>
    </button>
  );
}

/* ── Transcript ──────────────────────────────────────────────────────────
   MIGRATED (METODO §4): the transcript, the user message and the prose have
   no private copy here. Their markup and CSS were MOVED to layout/Stream,
   components/UserWaypoint and components/AssistantDocument, class names and
   all, and the prototype imports them back. What sits here now is only an
   adapter: the prototype's fixtures mapped onto the props the shipped
   pieces take, plus the burst-span lab that feeds Prose the way a websocket
   feeds production markdown.

   The rule is unchanged: the user's message is the only thing with a peach
   edge; the assistant's turn has no frame at all -- it is the page. Tool
   work and deliverables are objects ON the page (ledger, artifact) and are
   not this piece. */

function UserMessage({ children, when }) {
  return <UserWaypoint time={when}>{children}</UserWaypoint>;
}

/* ── Streaming: the arriving-text effect ──────────────────────────────────
   Production concatenates deltas once per animation frame and re-renders the
   markdown. Anything per-character is out: it would be one DOM node per
   letter over thousands of words. What CAN be animated cheaply is the
   BOUNDARY: the newest chunk of text fades in as a single inline span, and
   the caret follows it. Old text never moves, never repaints beyond layout.

   Mechanics in this lab: the text is cut into word-ish tokens and appended at
   a variable pace, in bursts like a real model, each burst wrapped in one
   <span class="zl-new"> that runs a 220ms opacity ramp once. Spans are
   flattened back into plain text after ~10 of them, so the DOM never grows.
   The caret is a block the height of the line, breathing only while idle
   (waiting for the next delta), steady while text is flowing: that is the
   cue "still coming" vs "thinking". */
const STREAM_SOURCE = `Confirmado: es una carrera en el borrado. \`Delete\` quita el índice antes de comprobar que existe, así que dos llamadas concurrentes al mismo blob dejan la segunda sin error y el contador de referencias en −1.

He movido la comprobación dentro del lock y añadido un test que lanza cien borrados en paralelo. Pasa en local; ahora corre \`go vet\` para descartar que el cambio de firma rompa otro paquete.`;

function tokenize(src) {
  // Split on word boundaries but keep the delimiters, so re-joining is exact.
  // Inline spans (`code`, **bold**) are kept whole: a burst boundary falling
  // inside one leaves an orphan backtick on each side, and the lab renders the
  // marker instead of the code. Real deltas have the same hazard, which is why
  // production parses the settled text rather than the fragment.
  return src.match(/`[^`]+`\s*|\*\*[^*]+\*\*\s*|\S+\s*|\s+/g) || [];
}

/* Very small inline-markdown for the lab: `code`, **bold**, line breaks. */
function inline(text) {
  const out = [];
  const re = /(`[^`]+`|\*\*[^*]+\*\*)/g;
  let last = 0;
  let m;
  while ((m = re.exec(text))) {
    if (m.index > last) out.push(text.slice(last, m.index));
    const t = m[0];
    if (t[0] === "`") out.push(<code>{t.slice(1, -1)}</code>);
    else out.push(<strong>{t.slice(2, -2)}</strong>);
    last = m.index + t.length;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}

function useStream(playingProp) {
  // FROZEN is the fidelity scene (?view=scene), and only that: a timer-driven
  // stream cannot be captured twice the same way, whatever the clock says. It
  // settles on the finished text, which is the state the prose spends most of
  // its life in anyway. Everywhere else `playing` is untouched.
  const playing = playingProp && !FROZEN;
  const tokens = useRef(tokenize(STREAM_SOURCE));
  // Not playing = the turn is already finished: show it settled.
  const [state, setState] = useState(() => ({ i: playing ? 0 : tokens.current.length, bursts: [], idle: true }));
  useEffect(() => {
    if (!playing) return;
    let alive = true;
    let timer;
    let idleTimer;
    let cur = 0;
    let bursts = [];
    const step = () => {
      if (!alive) return;
      if (cur >= tokens.current.length) {
        // loop: pause on the finished text, then start over
        timer = setTimeout(() => { cur = 0; bursts = []; setState({ i: 0, bursts: [], idle: true }); timer = setTimeout(step, 400); }, 2800);
        return;
      }
      const n = 1 + Math.floor(Math.random() * 4);
      const to = Math.min(tokens.current.length, cur + n);
      bursts = [...bursts.slice(-9), { from: cur, to }];
      cur = to;
      setState({ i: cur, bursts, idle: false });
      // real deltas arrive in bursts with gaps: 40-140ms, occasional stalls
      const gap = Math.random() < 0.12 ? 500 + Math.random() * 500 : 40 + Math.random() * 100;
      timer = setTimeout(step, gap);
      // the caret starts blinking only once nothing has arrived for a while
      clearTimeout(idleTimer);
      idleTimer = setTimeout(() => { if (alive) setState((s) => ({ ...s, idle: true })); }, 350);
    };
    timer = setTimeout(step, 400);
    return () => { alive = false; clearTimeout(timer); clearTimeout(idleTimer); };
  }, [playing]);
  return { tokens: tokens.current, i: state.i, bursts: state.bursts, idle: state.idle, done: state.i >= tokens.current.length };
}

function StreamingProse({ playing }) {
  const s = useStream(playing);
  // Everything before the oldest tracked burst is settled plain text; the
  // bursts are the animated tail. Paragraph breaks may fall anywhere, so the
  // whole text is split into paragraphs first, and each burst span is cut at
  // the breaks it straddles.
  const settledEnd = s.bursts.length ? s.bursts[0].from : s.i;
  const settled = s.tokens.slice(0, settledEnd).join("");
  const paras = settled.split("\n\n").map((p) => [inline(p)]);
  for (const b of s.bursts) {
    const parts = s.tokens.slice(b.from, b.to).join("").split("\n\n");
    parts.forEach((part, k) => {
      if (k > 0) paras.push([]);
      if (part) paras[paras.length - 1].push(<span class="zl-new" key={`${b.from}:${k}`}>{inline(part)}</span>);
    });
  }
  return (
    <Prose streaming={!s.done} done={s.done}>
      {paras.map((children, k) => (
        <p key={k}>
          {children}
          {k === paras.length - 1 && !s.done && (
            <span class={`zl-caret${s.idle ? " is-idle" : ""}`} aria-hidden="true" />
          )}
        </p>
      ))}
    </Prose>
  );
}

/* ── Live zone ─────────────────────────────────────────────────────────────
   MIGRATED (METODO §4): the strip has no private copy here. Its markup and
   its CSS were MOVED to layout/LiveBar, class names and all, and the
   prototype imports them back. What sits here now is only an adapter: the
   lab's `{ fg, bg, t0 }` fixtures mapped onto the shipped session + agents
   shape. Production streams the phrase from activityText and the background
   from liveTrayAgents(); the fixtures are scaffolding for a demo.

   The panel's open state is still owned by the host (`useLive`), so the
   phone can force it shut while typing without the adapter growing a
   keyboard of its own. */
const LIVE_BG = [
  { kind: "agent", name: "terra", task: "review the diff", doing: "Reading pkg/attach/store.go", ago: 134 },
  { kind: "bash", cmd: "go test ./... -race", ago: 41 },
  { kind: "bash", cmd: "npm run build", ago: 12 },
];
const FG_WORKING = { phase: "working", text: "Running go vet", ago: 4 };
const FG_WAITING = { phase: "waiting", text: "Waiting for you" };

/* The lab presets. `open` is the initial panel state; each host then owns
   it, so the owner can open and close the panel in any preset. */
const LIVE_STATES = [
  { id: "idle", label: "Idle", fg: null, bg: [], note: "Nothing alive. The zone does not exist; the transcript has the space." },
  { id: "working", label: "Working", fg: FG_WORKING, bg: [], note: "The foreground run: one verb, a breathing green dot, a counter that ticks. Nothing async." },
  { id: "waiting", label: "Waiting for you", fg: FG_WAITING, bg: [], note: "Parked on you: amber, still, no counter. The loud prompt stays inline above; this is its quiet echo, pinned." },
  { id: "background", label: "Background only", fg: null, bg: LIVE_BG, note: "Nothing in the foreground, three things in the background: the sentence slot rotates a spotlight, each item with its own identity mark. The tally is the door." },
  { id: "all", label: "Everything", fg: FG_WORKING, bg: LIVE_BG, note: "Foreground busy AND three async. The foreground keeps the sentence; the background is only dots and a count. One line, no second verb." },
  { id: "open", label: "Everything, open", fg: FG_WORKING, bg: LIVE_BG, open: true, note: "The panel: one row per live thing, grouped by kind, opening upward so the row stays where it was. Tap a row to go to it." },
];

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

function fixtureSession(fg, t0) {
  if (!fg) return { state: "idle" };
  if (fg.phase === "waiting") return { state: "permission", runStartedAtMs: t0 };
  return {
    state: "running",
    runStartedAtMs: t0 - (fg.ago || 0) * 1000,
    liveLabel: fg.text,
  };
}

function fixtureAgents(bg, t0, now) {
  return (bg || []).map((b, i) => {
    const time = formatElapsed(now - (t0 - (b.ago || 0) * 1000));
    if (b.kind === "agent" || b.kind === "subagent") {
      return {
        id: b.id || b.name || `a${i}`,
        kind: "subagent",
        name: b.name,
        hue: projectHue(b.name),
        action: b.doing || b.action,
        task: b.task,
        time,
      };
    }
    return {
      id: b.id || b.cmd || `b${i}`,
      kind: "bash",
      name: "bash",
      action: b.cmd || b.action,
      time,
    };
  });
}

function LiveZone({ fg, bg = [], open, onToggle, dense, t0 }) {
  const alive = !!fg || bg.length > 0;
  const now = useNow(alive);
  const origin = t0 || now;
  return (
    <ProductionLiveBar
      session={fixtureSession(fg, origin)}
      agents={fixtureAgents(bg, origin, now)}
      open={open}
      onToggle={onToggle}
      dense={dense}
      nowMs={now}
      onOpen={() => {}}
    />
  );
}

/* Each host owns the panel: the preset seeds it, the owner can toggle it. */
function useLive(preset, forceCompact) {
  const [open, setOpen] = useState(!!preset.open);
  const [t0, setT0] = useState(() => Date.now());
  useEffect(() => { setOpen(!!preset.open); setT0(Date.now()); }, [preset]);
  return { fg: preset.fg, bg: preset.bg, open: open && !forceCompact, onToggle: () => setOpen((v) => !v), t0 };
}

/* The blocking card, out of scope here (it has its own component in
   production); this stub only shows WHERE it sits -- after the transcript,
   before the composer -- and that "needs you" is yellow. */
const ASK_CARD = (
  <div class="zl-ask" role="group" aria-label="Permission requested">
    <div class="zl-ask-t">Run <code class="zl-data">git push origin fix/attach-race</code>?</div>
    <div class="zl-ask-acts">
      <button type="button" class="zl-ask-btn is-primary">Allow</button>
      <button type="button" class="zl-ask-btn">Deny</button>
    </div>
  </div>
);

function Transcript({ dense, streaming = true, short, tail }) {
  // Follow the tail like production does: the transcript is pinned to its
  // bottom while a turn is streaming. (Production also un-pins when the user
  // scrolls up; this lab only shows the pinned case.)
  const ref = useRef(null);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    el.scrollTop = el.scrollHeight;
    if (!streaming) return;
    const ro = new ResizeObserver(() => { el.scrollTop = el.scrollHeight; });
    ro.observe(el);
    return () => ro.disconnect();
  }, [streaming]);
  return (
    <ProductionTranscript dense={dense} scrollRef={ref}>
      {short && (
        <UserMessage when="09:31">
          Vale. Confírmalo con un test de concurrencia y pasa vet antes de dar por bueno el cambio.
        </UserMessage>
      )}
      {!short && (
        <>
          <UserMessage when="09:12">
            El store de attachments pierde blobs si dos sesiones borran el mismo a la vez. ¿Es carrera o es el índice?
          </UserMessage>

          <AssistantDocument>
            <Prose>
              <p>Voy a mirar primero cómo se ordena el borrado respecto al índice, porque el síntoma que describes (un blob que sobrevive sin dueño) es más típico de una escritura sin lock que de una corrupción del índice.</p>
            </Prose>
            <Ledger rows={LEDGER_A} dense={dense} />
            <Prose>
              <h3>Qué he encontrado</h3>
              <p>Es una carrera, no el índice. <code>Delete</code> quita la entrada del mapa <em>antes</em> de comprobar que existe, así que el segundo borrado no falla y decrementa el contador dos veces:</p>
              <ol>
                <li>La sesión A entra en <code>Delete</code>, toma el lock y borra la entrada.</li>
                <li>La sesión B entra justo después: la entrada ya no está, pero el código no lo comprueba y sigue.</li>
                <li>Las dos decrementan <code>refs</code>; el blob queda en −1 y el GC no lo toca nunca.</li>
              </ol>
              <p>El test que fallaba arriba es el que lo demuestra: esperaba <code>ErrNotFound</code> en el segundo borrado y recibía <code>nil</code>. Con la comprobación dentro del lock pasa.</p>
            </Prose>
            <Artifact name="attach-race-report.md" kind="md" size="4.2 kB" dense={dense} />
          </AssistantDocument>

          <UserMessage when="09:31">
            Vale. Confírmalo con un test de concurrencia y pasa vet antes de dar por bueno el cambio.
          </UserMessage>
        </>
      )}

      <AssistantDocument>
        <Ledger rows={streaming ? LEDGER_B : LEDGER_B_DONE} dense={dense} folded={false} />
        <StreamingProse playing={streaming} />
      </AssistantDocument>
      {tail}
    </ProductionTranscript>
  );
}

/* ── Status line ─────────────────────────────────────────────────────────
   Eleven data can be on this line. They are not equal, and the line should
   not pretend they are. Three tiers, and a tier is a place, not a colour:

   1  SETTINGS  (left)   model+thinking, permissions, fast   -- what you set.
                         Buttons: they open pickers. Always present.
   2  GAUGES    (right)  context ring, spend, tokens        -- what the run
                         costs. Read constantly, so they are stable, mono,
                         and never jump around. Context+spend are one button
                         (the door to Usage); tokens are text.
   3  EVENTS    (centre) goal, tasks, MCP, on extra          -- only there
                         while something is happening. They appear between
                         the two fixed groups so neither group moves when an
                         event comes and goes. Each is a word plus a datum,
                         and only the ones that are alarms carry state colour
                         (MCP unhealthy: red; on extra: yellow). Goal and
                         tasks are neutral: progress, not danger.

   Width degrades tier by tier, never element by element: at each step a
   whole tier loses its words and keeps its data, so the line always reads
   the same order of things. */
const LEVELS = ["off", "low", "medium", "high", "xhigh"];
function ThinkMeter({ level }) {
  const n = LEVELS.indexOf(level);
  return (
    <span class="zl-think" aria-hidden="true">
      {[1, 2, 3, 4].map((k) => <i class={k <= n ? "" : "is-off"} key={k} />)}
    </span>
  );
}

const FULL_STATUS = {
  model: "Daybreak Blue", thinking: "medium", perm: "yolo", fast: true,
  ctx: 63, spend: "$1.84", up: "12.4k", down: "1.8k",
  goal: { iteration: 3 }, tasks: { done: 2, total: 5 },
  mcp: { total: 3, unhealthy: 1 }, onExtra: true,
};

/* ── What opens when you tap a setting ─────────────────────────────────────
   The line's rule: it holds the controls for the NEXT turn. Two of its
   buttons are controls (model+thinking+fast, permissions) and open a picker;
   the third (the ring) is a reading and opens the session panel on its
   Usage page -- the panel is where "how is this going" lives, and a second
   home for the same numbers would be a second thing to keep in sync.

   The picker is one component per control and one surface per density:
   on desktop and in a pane it is a popover anchored to its button, opening
   UPWARD (the line is at the bottom; the button stays under your pointer);
   on the phone it is a bottom sheet, the same content, with the transcript
   still visible above it -- that is what keeps model/permissions at two
   taps on the phone, where the panel would cost a third and hide the
   conversation. Popover and sheet are the same sheet-tone surface the
   drawers use, with the same head (eyebrow, or back + title one level in),
   so the three read as one family: drawer, popover, sheet.

   The model picker is production's ModelSelector reduced to its parts:
   current model (tap: the provider view), the pinned grid, the door to all
   providers, the thinking stepper, the fast switch. Provider and per-provider
   views are pushed INSIDE the popover with a back button, like the panel's
   pages: one navigation idiom for every second level in the product. */
const MODELS = [
  { name: "Daybreak Blue", sub: "1M ctx", provider: "Anthropic", pinned: true },
  { name: "Opus", sub: "4.8 · 1M ctx", provider: "Anthropic", pinned: true },
  { name: "Sonnet", sub: "4.6 · 1M ctx", provider: "Anthropic", pinned: true },
  { name: "Haiku", sub: "4.5 · 200k ctx", provider: "Anthropic" },
  { name: "Sol", sub: "5.5 · 400k ctx", provider: "OpenAI", pinned: true },
  { name: "Terra", sub: "5.3 codex · 400k ctx", provider: "OpenAI", pinned: true },
  { name: "Luna", sub: "5.1 mini · 400k ctx", provider: "OpenAI", pinned: true },
  { name: "GPT-5.5", sub: "400k ctx", provider: "OpenAI" },
  { name: "Fable", sub: "3.1 pro · 1M ctx", provider: "Google" },
  { name: "Flash", sub: "3.1 · 1M ctx", provider: "Google" },
  { name: "Grok", sub: "4.2 · 256k ctx", provider: "xAI" },
  { name: "Grok Fast", sub: "4.2 · 128k ctx", provider: "xAI" },
];

/* MIGRATED (METODO §4, the pickers): ModelPicker, PermPicker, Popover and
   Sheet have no private copy here. Their markup and CSS were MOVED to
   ModelSelector / PermissionControl, class names and all, and the prototype
   imports them back. What sits here now is only an adapter: the prototype's
   invented catalog mapped onto the props the shipped picker takes. */
const CATALOG_SPECS = MODELS.map((m) => ({
  id: m.name,
  catalogId: m.name,
  name: m.name,
  provider: m.provider,
  codename: m.name,
  sub: m.sub,
}));
const CATALOG_PINNED = MODELS.filter((m) => m.pinned).map((m) => m.name);

function ModelPicker({ s, onChange, onDone, view, setView }) {
  return (
    <ProductionModelSelector
      models={CATALOG_SPECS}
      selected={s.model}
      sessionModel={s.model}
      thinking={s.thinking}
      fast={s.fast}
      fastSupported
      pinnedIDs={CATALOG_PINNED}
      view={view}
      setView={setView}
      onSelect={(id) => { onChange({ model: id }); onDone(); }}
      onThinkingChange={(thinking) => onChange({ thinking })}
      onFastChange={(fast) => onChange({ fast })}
    />
  );
}

function PermPicker({ s, onChange, onDone }) {
  return (
    <PermissionOptions
      mode={s.perm}
      onPick={(perm) => { onChange({ perm }); onDone(); }}
    />
  );
}

function Popover({ kind, s, onChange, onClose }) {
  return (
    <PickerPopover kind={kind} models={CATALOG_SPECS} onClose={onClose}>
      {(v) => kind === "model"
        ? <ModelPicker s={s} onChange={onChange} onDone={onClose} view={v.view} setView={v.setView} />
        : <PermPicker s={s} onChange={onChange} onDone={onClose} />}
    </PickerPopover>
  );
}

function Sheet({ kind, s, onChange, onClose }) {
  return (
    <PickerSheet kind={kind} models={CATALOG_SPECS} onClose={onClose}>
      {(v) => kind === "model"
        ? <ModelPicker s={s} onChange={onChange} onDone={onClose} view={v.view} setView={v.setView} />
        : <PermPicker s={s} onChange={onChange} onDone={onClose} />}
    </PickerSheet>
  );
}

/* Each host owns its settings and which picker is open. `inline` hosts
   (desktop, pane) render the popover anchored inside the line; the phone
   renders a sheet itself. */
function useSettings(initial, forced) {
  const [s, setS] = useState(initial);
  const [pick, setPick] = useState(null);
  useEffect(() => { setS(initial); }, [initial]);
  useEffect(() => { setPick(forced === "model" || forced === "perm" ? forced : null); }, [forced]);
  const onChange = (patch) => setS((v) => ({ ...v, ...patch }));
  return { s, onChange, pick, setPick, close: () => setPick(null) };
}

function StatusLine({ s = FULL_STATUS, compact, pick, onPick, onUsage, onChange, inline }) {
  const open = (k) => onPick && onPick(pick === k ? null : k);
  const pop = (k) => inline && pick === k && <Popover kind={k} s={s} onChange={onChange} onClose={() => onPick(null)} />;
  return (
    <div class={`zl-status${compact ? " is-compact" : ""}`}>
      {/* tier 1 — settings */}
      <div class="zl-st-group is-settings">
        <span class="zl-st-anchor">
          <button type="button" class={`zl-st zl-st-model zl-p1${pick === "model" ? " is-open" : ""}`} onClick={() => open("model")} aria-expanded={pick === "model"} aria-haspopup="dialog" aria-label={`Model & thinking: ${s.model}, ${s.thinking}`}>
            <span class="zl-st-word zl-st-model-name">{s.model}</span>
            <ThinkMeter level={s.thinking} />
          </button>
          {pop("model")}
        </span>
        <span class="zl-st-anchor">
          <button type="button" class={`zl-st zl-st-perm zl-p1 is-${s.perm}${pick === "perm" ? " is-open" : ""}`} onClick={() => open("perm")} aria-expanded={pick === "perm"} aria-haspopup="dialog" aria-label={`Permission mode: ${s.perm}`}>
            <span class="zl-st-word">{s.perm}</span>
          </button>
          {pop("perm")}
        </span>
        {s.fast && (
          <span class="zl-st zl-st-fast zl-p4" title="Fast mode: billed at a premium rate">
            <svg class="zl-st-ico" viewBox="0 0 16 16" aria-hidden="true"><path d="M9 1.5L3.5 9h4l-.5 5.5L12.5 7h-4z" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round" /></svg>
            <span class="zl-st-word">fast</span>
          </span>
        )}
      </div>

      {/* tier 3 — events, only while they exist */}
      <div class="zl-st-group is-events">
        {!compact && s.goal && (
          <span class="zl-st zl-st-ev zl-p4" title="Goal active, iteration 3">
            <svg class="zl-st-ico" viewBox="0 0 16 16" aria-hidden="true"><circle cx="8" cy="8" r="5.5" fill="none" stroke="currentColor" stroke-width="1.5" /><circle cx="8" cy="8" r="1.8" fill="currentColor" /></svg>
            <span class="zl-st-word">goal</span><span class="zl-data">{s.goal.iteration}</span>
          </span>
        )}
        {!compact && s.tasks && (
          <span class="zl-st zl-st-ev zl-p4" title="Tasks: 2 of 5 done">
            <svg class="zl-st-ico" viewBox="0 0 16 16" aria-hidden="true"><path d="M3 4.5l1.5 1.5 3-3M3 10.5l1.5 1.5 3-3M9 5h4M9 11h4" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" /></svg>
            <span class="zl-st-word">tasks</span><span class="zl-data">{s.tasks.done}/{s.tasks.total}</span>
          </span>
        )}
        {s.mcp && s.mcp.total > 0 && (
          <button type="button" class={`zl-st zl-st-ev ${s.mcp.unhealthy ? "zl-p2 is-alarm-red" : "zl-p4"}`} aria-label={s.mcp.unhealthy ? `MCP: ${s.mcp.unhealthy} of ${s.mcp.total} need attention` : `MCP: ${s.mcp.total} servers`}>
            <svg class="zl-st-ico" viewBox="0 0 16 16" aria-hidden="true"><path d="M5 2v3M11 2v3M3.5 5h9v3a4.5 4.5 0 0 1-9 0zM8 12.5V15" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" /></svg>
            <span class="zl-st-word">mcp</span>
            <span class="zl-data">{s.mcp.unhealthy ? `${s.mcp.unhealthy}/${s.mcp.total}` : s.mcp.total}</span>
          </button>
        )}
        {s.onExtra && (
          <span class="zl-st zl-st-ev zl-p2 is-alarm-yellow" title="Served from extra usage (pay-as-you-go)">
            <svg class="zl-st-ico" viewBox="0 0 16 16" aria-hidden="true"><path d="M8 1.5c.5 3-3 4-3 8a3 3 0 0 0 6 0c0-1.5-.6-2.5-1.2-3.2-.3 1.2-1 1.7-1.3 1.7C9 6 9.5 3.5 8 1.5z" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round" /></svg>
            <span class="zl-st-word">extra</span>
          </span>
        )}
      </div>

      {/* tier 2 — gauges */}
      <div class="zl-st-group is-gauges">
        <button type="button" class="zl-st zl-st-ctx zl-p1" onClick={onUsage} aria-label={`Context ${s.ctx}% used, ${s.spend} spent — show usage`}>
          <CtxRing pct={s.ctx} />
          <span class="zl-data zl-num">{s.ctx}<span class="zl-unit">%</span></span>
          <span class="zl-st-sep" aria-hidden="true" />
          <span class="zl-data zl-num zl-st-spend">{s.spend}</span>
        </button>
        <span class="zl-st zl-st-tok zl-data zl-p3" title="Tokens this run">
          <span class="zl-arrow" aria-hidden="true">↑</span><span class="zl-num">{s.up}</span>
          <span class="zl-arrow" aria-hidden="true">↓</span><span class="zl-num">{s.down}</span>
        </span>
      </div>
    </div>
  );
}

/* MIGRATED (METODO §4, the composer): the slab has no private copy here.
   Its markup and its CSS were MOVED to layout/Composer, class names and all,
   and the prototype imports them back. What sits here now is only an
   adapter: the prototype's `onFocusChange` (the phone's typing veil) mapped
   onto the shipped component. A change to the slab can only land in one
   place -- and, unlike a copy, this file FAILS the pixel harness when
   production's composer changes, which is the whole point of the move. */
function Composer({ onFocusChange }) {
  return <ProductionComposer onFocusChange={onFocusChange} />;
}

/* Edge gestures, mirrored. A 28px zone on either edge starts a drag that
   pulls that edge's drawer in; the drawer follows the finger and commits past
   90px. With a drawer open, dragging it back toward its own edge closes it.
   The vertical guard abandons the gesture if the finger is really scrolling. */
const W_LEFT = 300;
const W_RIGHT = 320;
const EDGE = 28;

function useEdgeDrawers(hostRef) {
  const [left, setLeft] = useState(false);
  const [right, setRight] = useState(false);
  const [drag, setDrag] = useState(null); // { side, dx }
  const start = useRef(null);

  const onTouchStart = (e) => {
    const host = hostRef.current.getBoundingClientRect();
    const t = e.touches[0];
    const x = t.clientX - host.left;
    let side = null;
    if (left) side = "left";
    else if (right) side = "right";
    else if (x <= EDGE) side = "left";
    else if (x >= host.width - EDGE) side = "right";
    if (!side) return;
    start.current = { x: t.clientX, y: t.clientY, side };
  };
  const onTouchMove = (e) => {
    if (!start.current) return;
    const t = e.touches[0];
    const dx = t.clientX - start.current.x;
    const dy = Math.abs(t.clientY - start.current.y);
    if (drag == null && dy > Math.abs(dx)) { start.current = null; return; }
    setDrag({ side: start.current.side, dx });
  };
  const onTouchEnd = () => {
    if (!start.current) { setDrag(null); return; }
    const { side } = start.current;
    const dx = drag?.dx || 0;
    if (side === "left") {
      if (left && dx < -90) setLeft(false);
      if (!left && dx > 90) setLeft(true);
    } else {
      if (right && dx > 90) setRight(false);
      if (!right && dx < -90) setRight(true);
    }
    start.current = null;
    setDrag(null);
  };

  // Offsets while dragging, clamped so a drawer never overshoots its edge.
  const leftX = drag?.side === "left"
    ? Math.max(-W_LEFT, Math.min(0, (left ? 0 : -W_LEFT) + drag.dx))
    : null;
  const rightX = drag?.side === "right"
    ? Math.max(0, Math.min(W_RIGHT, (right ? 0 : W_RIGHT) + drag.dx))
    : null;
  const veil = leftX != null
    ? 1 + leftX / W_LEFT
    : rightX != null
      ? 1 - rightX / W_RIGHT
      : null;

  return {
    left, right, setLeft, setRight, leftX, rightX, veil,
    handlers: { onTouchStart, onTouchMove, onTouchEnd },
  };
}

/* The settings sheet, owned by the host like every other surface, so the lab's
   preset can open it and the harness can capture it. Two presets, because the
   sheet has two levels: "settings" is the root list of rows, "settings-page"
   is a row's second page pushed in place. */
function useSettingsSurface(forced) {
  const [open, setOpen] = useState(false);
  const [page, setPage] = useState("root");
  useEffect(() => {
    if (forced === "settings") { setOpen(true); setPage("root"); }
    else if (forced === "settings-page") { setOpen(true); setPage("compact-strategy"); }
    else { setOpen(false); setPage("root"); }
  }, [forced]);
  return { open, page, setPage, show: () => setOpen(true), close: () => { setOpen(false); setPage("root"); } };
}

/* The session panel's page, owned by the host: the ring on the line opens
   the panel straight on Usage; closing resets to the root. `forced` is the
   lab's preset (a page name, or "panel" for the root). */
function usePanel(forced, setOpen) {
  const [page, setPage] = useState("root");
  useEffect(() => {
    const p = forced === "panel" ? "root" : forced;
    if (p === "root" || p in PANEL_PAGES) { setPage(p); setOpen(true); } else { setOpen(false); setPage("root"); }
  }, [forced]);
  return {
    page, setPage,
    show: (p = "root") => { setPage(p); setOpen(true); },
    close: () => { setOpen(false); setPage("root"); },
  };
}

/* ── Phone ─────────────────────────────────────────────────────────────── */
function Phone({ label, live: preset, surface }) {
  const [view, setView] = useState("recent");
  const settings = useSettingsSurface(surface);
  const host = useRef(null);
  const d = useEdgeDrawers(host);
  const panel = usePanel(surface, d.setRight);
  const set = useSettings(FULL_STATUS, surface);
  const anyOpen = d.left || d.right || d.veil != null;
  // Typing wins: with the keyboard up the panel is forced shut.
  const [typing, setTyping] = useState(false);
  const live = useLive(preset, typing);
  const closeRight = panel.close;
  // The edge gesture closes the drawer without going through panel.close:
  // reset its page on the open→closed edge so the next open starts at root.
  const wasRight = useRef(false);
  useEffect(() => { if (wasRight.current && !d.right) panel.setPage("root"); wasRight.current = d.right; }, [d.right]);

  return (
    <div class="zl-phone-wrap">
      <div class="zl-density-label">{label}</div>
      <div class="zl-phone" ref={host} {...d.handlers}>
        <Transcript streaming={!!preset.fg && preset.fg.phase === "working"} tail={preset.fg?.phase === "waiting" ? ASK_CARD : null} />

        {/* three floating capsules: sidebar / this session / new */}
        <div class="zl-chrome">
          <button type="button" class="zl-cap zl-cap-left" onClick={() => d.setLeft(true)} aria-label="Sessions">
            <span class="zl-burger" aria-hidden="true" />
            <span class="zl-cap-badge" />
          </button>
          <button type="button" class="zl-cap zl-chip" onClick={() => d.setRight(true)}>
            <span class="zl-chip-name">Buscar un bug bounty</span>
            <svg class="zl-chev" viewBox="0 0 16 16" aria-hidden="true">
              <path d="M4.5 6.5L8 10l3.5-3.5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" />
            </svg>
          </button>
          <button type="button" class="zl-cap zl-cap-right" aria-label="New session"><PlusIcon /></button>
        </div>

        <div class="zl-dock">
          <LiveZone {...live} />
          <Composer onFocusChange={setTyping} />
          <StatusLine compact s={set.s} pick={set.pick} onPick={set.setPick} onChange={set.onChange} onUsage={() => panel.show("usage")} />
        </div>

        {set.pick && (
          <>
            <div class="zl-scrim is-sheet" onClick={set.close} />
            <Sheet kind={set.pick} s={set.s} onChange={set.onChange} onClose={set.close} />
          </>
        )}

        {anyOpen && (
          <div
            class="zl-scrim"
            style={d.veil != null ? `opacity:${d.veil};transition:none` : ""}
            onClick={() => { d.setLeft(false); closeRight(); }}
          />
        )}
        {settings.open && (
          <GlobalSettings
            phone
            inline
            open
            onClose={settings.close}
            initialPage={settings.page}
            soundEnabled
            version={{ current: "v0.37.2" }}
          />
        )}
        <div
          class={`zl-side zl-side-left${d.left ? " is-open" : ""}`}
          style={d.leftX != null ? `transform:translateX(${d.leftX}px);transition:none` : ""}
        >
          <Sidebar density="phone" onPick={() => d.setLeft(false)} onSettings={settings.show} view={view} onView={setView} />
        </div>
        <SessionPanel
          open={d.right}
          onClose={closeRight}
          page={panel.page}
          onPage={panel.setPage}
          style={d.rightX != null ? `transform:translateX(${d.rightX}px);transition:none` : undefined}
        />
      </div>
      <p class="zl-hint">
        Swipe in from the left edge for the other sessions, from the right edge
        for this one. Or tap ≡ and the name.
      </p>
    </div>
  );
}

/* ── Desktop ───────────────────────────────────────────────────────────── */
function HeadActions() {
  return (
    <>
      <button type="button" class="zl-desk-act" aria-label="Live preview">
        <svg viewBox="0 0 16 16" aria-hidden="true">
          <rect x="2" y="3" width="12" height="10" rx="2" fill="none" stroke="currentColor" stroke-width="1.5" />
          <path d="M2 6.5h12" stroke="currentColor" stroke-width="1.5" />
        </svg>
      </button>
      <button type="button" class="zl-desk-act" aria-label="Split">
        <svg viewBox="0 0 16 16" aria-hidden="true">
          <rect x="2" y="3" width="12" height="10" rx="2" fill="none" stroke="currentColor" stroke-width="1.5" />
          <path d="M8 3v10" stroke="currentColor" stroke-width="1.5" />
        </svg>
      </button>
    </>
  );
}

function Desktop({ label, live: preset, surface }) {
  const [view, setView] = useState("recent");
  const settings = useSettingsSurface(surface);
  const [open, setOpen] = useState(false);
  const panel = usePanel(surface, setOpen);
  const set = useSettings(FULL_STATUS, surface);
  const live = useLive(preset, false);
  return (
    <div class="zl-desk-wrap">
      <div class="zl-density-label">{label}</div>
      <div class="zl-desk">
        {settings.open && (
          <GlobalSettings
            inline
            open
            onClose={settings.close}
            initialPage={settings.page}
            soundEnabled
            version={{ current: "v0.37.2" }}
          />
        )}
        <div class="zl-desk-side">
          <Sidebar onPick={() => {}} desktop onSettings={settings.show} view={view} onView={setView} />
        </div>
        <div class="zl-desk-main">
          <div class="zl-desk-head">
            <button type="button" class="zl-crumb" onClick={() => panel.show()} aria-expanded={open}>
              <span class="zl-crumb-title">Buscar un bug bounty</span>
              <span class="zl-crumb-path zl-data">~/dev/moa</span>
            </button>
            <span class="zl-spacer" />
            <HeadActions />
          </div>
          <Transcript streaming={!!preset.fg && preset.fg.phase === "working"} tail={preset.fg?.phase === "waiting" ? ASK_CARD : null} />
          {set.pick && <div class="zl-veil" onClick={set.close} />}
          <div class="zl-dock">
            <LiveZone {...live} />
            <Composer />
            <StatusLine inline s={set.s} pick={set.pick} onPick={set.setPick} onChange={set.onChange} onUsage={() => panel.show("usage")} />
          </div>
          {open && <div class="zl-scrim" onClick={panel.close} />}
          <SessionPanel open={open} onClose={panel.close} page={panel.page} onPage={panel.setPage} />
        </div>
      </div>
      <p class="zl-hint">
        The same list, permanent, on the left. The same session drawer slides
        in over the transcript from the right, opened from the crumb.
      </p>
    </div>
  );
}

/* ── Grid ──────────────────────────────────────────────────────────────────
   Several sessions on one screen. A pane is the conversation column with its
   chrome compressed, not a different product: same transcript, same dock,
   same status line in its compact form (the existing `compact` prop: goal
   and tasks drop, context loses its word). What a pane adds is a head that
   says which session and whether it needs you; what it loses is width, so
   the transcript switches to its dense rhythm (tighter measure, smaller
   ledger and artifact) and the composer sits at one line. */
const PANES = [
  { title: "Buscar un bug bounty", path: "~/dev/moa", state: "running", focus: true, n: 1,
    status: { ...FULL_STATUS, goal: null, tasks: null, mcp: null, onExtra: false, fast: false, ctx: 63, spend: "$1.84" } },
  { title: "Check access to two repos", path: "~/dev/gugo", state: "permission", n: 2,
    status: { ...FULL_STATUS, model: "Terra", thinking: "high", perm: "ask", goal: null, tasks: null, mcp: null, onExtra: false, fast: false, ctx: 21, spend: "$0.42", up: "3.1k", down: "640" },
    tail: ASK_CARD },
  { title: "Deploy fails on ARM runner", path: "~/dev/tienda", state: "error", n: 3,
    status: { ...FULL_STATUS, model: "Sol", thinking: "high", perm: "auto", goal: null, tasks: null, onExtra: true, fast: false, ctx: 88, spend: "$6.10", up: "41k", down: "9.2k" },
    tail: (
      <div class="zl-sys is-error">Stopped: provider returned <span class="zl-data">529 overloaded</span> three times. Send a message to retry.</div>
    ) },
];

function Pane({ p, streaming, live: preset, surface }) {
  const live = useLive(preset || LIVE_STATES[0], false);
  const set = useSettings(p.status, surface);
  return (
    <section class={`zl-pane${p.focus ? " is-focus" : ""}`} aria-label={`Pane ${p.n}: ${p.title}`}>
      <div class="zl-pane-head">
        <Dot state={p.state} />
        <button type="button" class="zl-pane-title">
          <span class="zl-pane-t">{p.title}</span>
          <span class="zl-pane-path zl-data">{p.path}</span>
        </button>
        <span class="zl-spacer" />
        <kbd class="zl-kbd zl-data" title={`Focus with ⌘${p.n}`}>⌘{p.n}</kbd>
        <HeadActions />
      </div>
      <div class="zl-pane-body">
        <Transcript dense streaming={streaming} short={p.n !== 1} tail={p.tail} />
      </div>
      {set.pick && <div class="zl-veil" onClick={set.close} />}
      <div class="zl-dock is-pane">
        <LiveZone {...live} dense />
        <Composer />
        <StatusLine inline s={set.s} compact pick={set.pick} onPick={set.setPick} onChange={set.onChange} />
      </div>
    </section>
  );
}

function Grid({ label, live, surface }) {
  return (
    <div class="zl-grid-wrap">
      <div class="zl-density-label">{label}</div>
      <div class="zl-desk zl-grid">
        <div class="zl-grid-bar">
          <span class="zl-grid-bar-t">Layout · <span class="zl-data">3</span> panes</span>
          <span class="zl-spacer" />
          <span class="zl-grid-needs"><span class="zl-data">1</span> needs you</span>
        </div>
        <div class="zl-grid-panes">
          <Pane p={PANES[0]} streaming={!!live.fg && live.fg.phase === "working"} live={live} surface={surface} />
          <div class="zl-grid-col">
            <Pane p={PANES[1]} streaming={false} />
            <Pane p={PANES[2]} streaming={false} />
          </div>
        </div>
      </div>
      <p class="zl-hint">
        The 2+1 preset. Each pane is the single conversation with the compact
        status line and a dense transcript; the pane head replaces the crumb
        and carries the session's state dot. The live zone follows the
        preset in the focused pane.
      </p>
    </div>
  );
}

/* ── Status line study ─────────────────────────────────────────────────────────
   The same line, every datum present, at the widths it actually meets:
   the desktop column, a grid pane, the phone dock, a narrow pane. Degrading
   is done with container queries on the line itself, so it is the width of
   the line -- not the device -- that decides. */
const STUDY_WIDTHS = [
  { w: 816, note: "desktop column: everything, words and data. This is the only width where the events say their names." },
  { w: 640, note: "wide grid pane (compact): goal and tasks drop, events are icon + datum, tokens still there" },
  { w: 520, note: "grid pane (compact): tokens go; spend stays" },
  { w: 366, note: "phone dock (compact): spend folds into the ring's popover; settings keep their words; alarms keep their datum" },
  { w: 300, note: "narrow pane: model name truncates, non-alarm events are icon only. Below this the pane has no composer either." },
];

function StatusLineStudy() {
  return (
    <div class="zl-study">
      <div class="zl-density-label">Status line · all eleven data · five widths</div>
      {STUDY_WIDTHS.map(({ w, note }) => (
        <div class="zl-study-row" key={w}>
          <div class="zl-study-w zl-data">{w}px</div>
          <div class="zl-study-line" style={`width:${w + 24}px`}>
            <StatusLine compact={w < 700} />
          </div>
          <div class="zl-study-note">{note}</div>
        </div>
      ))}
    </div>
  );
}

/* ── Live zone study ──────────────────────────────────────────────────────
   The "everything" row at the widths it meets, with the panel open at the
   narrowest so the cap is visible. Degradation is by container width:
   < 420 the tally loses its dots (count + chevron), < 340 the sentence
   loses its counter. The verb is the last thing to go, and it only
   truncates. */
const LIVE_STUDY_WIDTHS = [
  { w: 816, note: "desktop column: verb, counter, dots, count" },
  { w: 520, note: "grid pane: the same, dense (32px row)" },
  { w: 366, note: "phone dock: the same at 44px; dots go below 420" },
  { w: 300, note: "narrow pane: the counter goes, the verb truncates, the count stays. Panel open: rows keep their identity mark and elapsed.", open: true },
];

function LiveZoneStudy() {
  const preset = LIVE_STATES.find((s) => s.id === "all");
  return (
    <div class="zl-study">
      <div class="zl-density-label">Live zone · everything at once · four widths</div>
      {LIVE_STUDY_WIDTHS.map(({ w, note, open }) => (
        <div class="zl-study-row" key={w}>
          <div class="zl-study-w zl-data">{w}px</div>
          <div class="zl-study-line" style={`width:${w + 24}px`}>
            <StudyLive preset={open ? { ...preset, open: true } : preset} dense={w < 700} />
          </div>
          <div class="zl-study-note">{note}</div>
        </div>
      ))}
    </div>
  );
}
function StudyLive({ preset, dense }) {
  const live = useLive(preset, false);
  return <LiveZone {...live} dense={dense} />;
}

/* Lab control: one segmented switch, not product. */
function LabSeg({ label, options, value, onChange }) {
  const cur = options.find((s) => s.id === value);
  return (
    <div class="zl-lab-ctl">
      <div class="zl-lab-seg" role="radiogroup" aria-label={label}>
        {options.map((s) => (
          <button
            type="button"
            role="radio"
            aria-checked={s.id === value}
            class={`zl-lab-opt${s.id === value ? " is-on" : ""}`}
            onClick={() => onChange(s.id)}
            key={s.id}
          >{s.label}</button>
        ))}
      </div>
      {cur?.note && <p class="zl-lab-note">{cur.note}</p>}
    </div>
  );
}

/* The surfaces the owner asked to see without touching anything: the panel
   and its three pages, and the two pickers. Each preset opens the same thing
   in every host on the page, so the densities can be compared side by side. */
const SURFACES = [
  { id: "none", label: "Closed", note: "Everything shut. Tap the crumb / the name for the panel; tap a setting on the line for its picker; tap the ring for Usage." },
  { id: "panel", label: "Panel", note: "The session dossier: identity, run facts, three rows (Usage, MCP, Artifacts) that push a page inside the panel, lifecycle at the foot. No model, no permissions: those are the line's." },
  { id: "usage", label: "Panel · Usage", note: "What the ring opens. This session's spend, context and tokens; then the quota of the provider this session uses, named so it never reads as global." },
  { id: "mcp", label: "Panel · MCP", note: "Production's dossier, in the panel: one row per server; the open one shows its verdict, its three scopes with the reach on the same line as the switch, and its error." },
  { id: "artifacts", label: "Panel · Artifacts", note: "The list. Opening one goes to the centre (inline on desktop, full screen on the phone). The reader never lives in 320px." },
  { id: "model", label: "Model picker", note: "What the model tap opens: a popover above its button on desktop and in a pane, a bottom sheet on the phone. Current model, pinned, the door to all providers (pushed inside with back), thinking, fast." },
  { id: "perm", label: "Permissions", note: "What the permission tap opens: three rows in the line's own colours, one line each of what it does. Pick one and it closes." },
  { id: "settings", label: "Settings", note: "What the gear opens: the GLOBAL settings, so it belongs to no edge — centred on the desktop, a bottom sheet on the phone. Rows, not a form: name and one line of explanation on the left, the value on the right. A choice between several opens a second page inside the same panel." },
  { id: "settings-page", label: "Settings · a page", note: "The second level: a row whose value is a choice pushes a page in place, with back + title in the head. Same idiom as the panel's dossiers and the model picker's providers." },
];
function LiveSwitch({ value, onChange }) {
  const cur = LIVE_STATES.find((s) => s.id === value);
  return (
    <div class="zl-lab-ctl">
      <div class="zl-lab-seg" role="radiogroup" aria-label="Live zone state">
        {LIVE_STATES.map((s) => (
          <button
            type="button"
            role="radio"
            aria-checked={s.id === value}
            class={`zl-lab-opt${s.id === value ? " is-on" : ""}`}
            onClick={() => onChange(s.id)}
            key={s.id}
          >{s.label}</button>
        ))}
      </div>
      <p class="zl-lab-note">{cur.note}</p>
    </div>
  );
}

export function ZonesLab() {
  useEffect(() => {
    document.documentElement.setAttribute("data-ambient", "on");
    return () => document.documentElement.removeAttribute("data-ambient");
  }, []);
  const [liveId, setLiveId] = useState(() => new URLSearchParams(location.search).get("live") || "working");
  const live = LIVE_STATES.find((s) => s.id === liveId) || LIVE_STATES[1];
  const [surface, setSurface] = useState(() => new URLSearchParams(location.search).get("surface") || "none");
  return (
    <div class="zl">
      <div class="zl-aurora" aria-hidden="true" />
      <header class="zl-head">
        <h1>Three zones</h1>
        <p>
          Left is the other sessions. Right is this session. Bottom is state.
          The two densities differ in host — drawers against a permanent column
          — and share the list, the dock and the session drawer.
        </p>
      </header>
      <LiveSwitch value={live.id} onChange={setLiveId} />
      <LabSeg label="Open surface" options={SURFACES} value={surface} onChange={setSurface} />
      <div class="zl-stage">
        <Phone label="Phone" live={live} surface={surface} />
        <Desktop label="Desktop" live={live} surface={surface} />
      </div>
      <div class="zl-stage">
        <Grid label="Desktop · grid" live={live} surface={surface} />
      </div>
      <LiveZoneStudy />
      <StatusLineStudy />
    </div>
  );
}

/* Exported for the fidelity harness only (scene.jsx). The prototype's own
   entry point is still ZonesLab; these are the same hosts it renders, mounted
   one at a time so a capture frames one thing. Nothing about the prototype
   changes by naming them. */
export { Phone as ZonesPhone, Desktop as ZonesDesktop, Grid as ZonesGrid, Sidebar as ZonesSidebar, StatusLineStudy, LiveZoneStudy, LIVE_STATES };
