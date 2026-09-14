import { useEffect, useRef, useState } from "preact/hooks";
import "./zones-lab.css";
import { FROZEN } from "./fidelity-freeze.js";
/* MIGRATED (METODO §4, piece 1 of the list): the session row no longer has a
   private copy here. Its markup and its CSS were MOVED to
   components/SessionRow, class names and all, and the prototype imports them
   back. There is one definition now, and a change to it can only land in one
   place. The dot comes with it, because a dot alone is not a piece. */
/* Same move, the composer: markup and CSS live in layout/Composer now, and
   the prototype draws the shipped one. See the adapter at `Composer`. */
import { Composer as ProductionComposer } from "../layout/Composer/Composer.jsx";
/* Same move, the grid: markup and CSS live in layout/Pane, PaneGrid and
   GridToolbar now, and the prototype draws the shipped ones. See the
   adapters at `Pane` and `Grid`. */
import { Pane as ProductionPane } from "../layout/Pane/Pane.jsx";
import { PaneGrid as ProductionPaneGrid } from "../layout/PaneGrid/PaneGrid.jsx";
import { GridToolbar } from "../layout/GridToolbar/GridToolbar.jsx";
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
/* Same move, the status line: markup and CSS live in layout/StatusStrip
   now, and the prototype draws the shipped one. See the adapter at
   `StatusLine`. ThinkMeter lives inside that component; there is not a
   second meter. */
import { StatusStrip } from "../layout/StatusStrip/StatusStrip.jsx";
/* Same move, the model and permission pickers: markup and CSS live in
   ModelSelector / PermissionControl now, and the prototype draws the shipped
   ones. See the adapters at `ModelPicker`, `PermPicker`, `Popover`, `Sheet`. */
import { ModelSelector as ProductionModelSelector, PickerPopover, PickerSheet } from "../components/ModelSelector/ModelSelector.jsx";
import { PermissionOptions } from "../components/PermissionControl/PermissionControl.jsx";
/* Same move, the sidebar: markup and CSS live in layout/Sidebar now, and
   the prototype draws the shipped one. See the adapter at `Sidebar`. */
import { Sidebar as ProductionSidebar } from "../layout/Sidebar/Sidebar.jsx";
import { PermissionCard } from "../components/PermissionCard/PermissionCard.jsx";
import { projectMonogram } from "../data/util/format.js";
/* Same move, the tool ledger: markup and CSS live in ActivityLedger now, and
   the prototype draws the shipped one. See the adapter at `Ledger`. Diffs
   that open inside a row use LedgerDiff; the artifact card is the shipped
   Artifact. */
import { ActivityLedger as ProductionLedger, LedgerDiff } from "../components/ActivityLedger/ActivityLedger.jsx";
import { Artifact } from "../components/Artifacts/Artifact.jsx";
import { ArtifactsDrawer } from "../components/Artifacts/ArtifactsDrawer.jsx";
import { setState } from "../data/store.js";
/* Same move, the phone header: markup and CSS live in layout/mobile/MobileChrome
   now, and the prototype draws the shipped one. See the adapter in `Phone`. */
import { MobileChrome } from "../layout/mobile/MobileChrome/MobileChrome.jsx";
/* Same move, the desktop head: markup and CSS live in layout/ChatHead now,
   and the prototype draws the shipped one. See the adapter in `Desktop`. */
import { ChatHead } from "../layout/ChatHead/ChatHead.jsx";

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
  // A healthy prompt cache, so the Usage page's cache rows are exercised by
  // the golden instead of frozen in their empty state. This is the shape of a
  // real Anthropic reading: most of the context replayed from cache, a small
  // write per turn, no streak — so nothing warns.
  cacheUsage: {
    available: true,
    ratio: 0.948,
    read: 16193532,
    written: 879314,
    streak: 1,
    alert: false,
  },
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
   (recessed, sheet tone) and artifact (raised, the one thing you take away).

   MIGRATED (METODO §4, the tool ledger): Ledger / LedgerRow / Diff / Artifact
   have no private copy here. Their markup and CSS were MOVED to
   ActivityLedger and Artifacts/Artifact, class names and all, and the
   prototype imports them back. What sits here now is only an adapter: the
   prototype's fixtures mapped onto the props the shipped pieces take. */

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

function adaptLedgerRow(row, i) {
  const arg = typeof row.arg === "object" && row.arg
    ? row.arg
    : { text: row.arg, detail: row.dim };
  const detail = row.detail == null
    ? undefined
    : (row.detail.node != null ? row.detail : { node: row.detail });
  return {
    ...row,
    id: row.id ?? `${row.tool}:${arg.text ?? ""}:${i}`,
    arg,
    detail,
  };
}

function Ledger({ rows, folded = true, dense }) {
  return (
    <ProductionLedger
      rows={rows.map(adaptLedgerRow)}
      folded={folded}
      dense={dense}
    />
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
  { tool: "edit", arg: "pkg/attach/store.go", dim: "+5 −1", out: "ok", status: "ok", detail: <LedgerDiff lines={DIFF_LINES} /> },
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

/* MIGRATED (METODO §4, the permission card): ASK_CARD has no private copy
   here. Its markup and CSS were MOVED to components/PermissionCard, class
   names and all, and the prototype imports them back. What sits here now is
   only an adapter: the lab's fixture command mapped onto the shipped props.
   Allow/Deny with no Always, because that is what the prototype drew. */
const ASK_CARD = (
  <PermissionCard command="git push origin fix/attach-race" />
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

/* MIGRATED (METODO §4, the status line): the line has no private copy here.
   Its markup and CSS were MOVED to layout/StatusStrip, class names and all,
   and the prototype imports them back. FULL_STATUS is the lab fixture the
   adapter maps onto the shipped props. ThinkMeter lives inside StatusStrip. */
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

function catalogTokens(v) {
  if (typeof v === "number" && Number.isFinite(v)) return v;
  const raw = String(v || "").trim();
  const k = /^([\d.]+)\s*k$/i.exec(raw);
  if (k) return Math.round(parseFloat(k[1]) * 1000);
  const n = Number(raw);
  return Number.isFinite(n) ? n : 0;
}

function catalogSession(s) {
  const session = {
    permissionMode: s.perm,
    fast: !!s.fast,
    onOverage: !!s.onExtra,
  };
  if (s.mcp && s.mcp.total > 0) session.mcp = s.mcp;
  if (s.goal) {
    session.goalActive = true;
    session.goalIteration = s.goal.iteration || 0;
  }
  if (s.tasks) {
    session.tasks = Array.from({ length: s.tasks.total }, (_, i) => ({
      status: i < s.tasks.done ? "done" : "open",
    }));
  }
  return session;
}

function StatusLine({ s = FULL_STATUS, compact, pick, onPick, onUsage, onChange, inline }) {
  const toggle = (k) => onPick?.(pick === k ? null : k);
  const pop = (k) => inline && pick === k
    ? <Popover kind={k} s={s} onChange={onChange} onClose={() => onPick(null)} />
    : null;
  return (
    <StatusStrip
      compact={!!compact}
      ctxPercent={s.ctx}
      tokensUp={catalogTokens(s.up)}
      tokensDown={catalogTokens(s.down)}
      spend={s.spend}
      session={catalogSession(s)}
      onOpenUsage={onUsage || (() => {})}
      onOpenMcp={() => {}}
      onPerm={() => toggle("perm")}
      permOpen={pick === "perm"}
      permPopover={pop("perm")}
      showTokens
      modelName={s.model}
      thinking={s.thinking}
      thinkingPosition={s.thinking}
      onModel={() => toggle("model")}
      modelOpen={pick === "model"}
      modelPopover={pop("model")}
    />
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

/* Artifacts is not a prop-driven component: the drawer reads the global store,
   because in the product it is mounted once and the conversation it shows is
   whichever one asked for it. So the lab seeds the store rather than faking a
   drawer, and what the harness photographs is the real thing in its real
   state. */
const LAB_ARTIFACTS = [
  { id: "a1", available: true, name: "fart-after-desktop.png", mime: "image/png", size: 184320, title: "Después · escritorio", description: "El plano elevado con los separadores finos, y las acciones reservadas a la derecha.", createdAt: "2026-09-09T18:12:00Z" },
  { id: "a2", available: true, name: "fart-before-desktop.png", mime: "image/png", size: 176128, title: "Antes · escritorio (referencia)", description: "Ochenta y tres tarjetas idénticas, sin una sola miniatura.", createdAt: "2026-09-09T18:04:00Z" },
  { id: "a3", available: true, name: "attach-race-report.md", mime: "text/markdown", size: 4300, title: "Informe de la carrera en adjuntos", description: "Dos sesiones reclamando el mismo blob; el índice sobrevive al borrado.", createdAt: "2026-09-09T09:28:00Z" },
  { id: "a4", available: true, name: "race-test.log", mime: "text/plain", size: 1126, title: "", description: "", createdAt: "2026-09-08T09:33:00Z" },
  { id: "a5", available: true, name: "coverage.html", mime: "text/html", size: 38912, title: "Cobertura tras el arreglo", description: "", createdAt: "2026-09-08T09:34:00Z" },
];

function useArtifactsSurface(forced, phone = false) {
  useEffect(() => {
    // The drawer asks the store whether it is on a phone; in the product that
    // is set by the real viewport, and the lab has to say so itself or the
    // phone scene would photograph the desktop drawer.
    if (forced === "artifacts" && phone) setState({ isMobile: true });
    if (forced === "artifacts") {
      // The drawer names its origin from the session roster, so the lab has to
      // put its conversation there or the header reads "Untitled".
      setState((st) => ({ sessions: { ...(st.sessions || {}), lab: { id: "lab", title: "Buscar un bug bounty" } } }));
      setState({ artifacts: {
        ownerSessionId: "lab", view: "list", fileId: null, from: "chat",
        expanded: false, seed: null, status: "ready", error: null,
        items: LAB_ARTIFACTS, token: 1,
      } });
    } else {
      // The drawer names its origin from the session roster, so the lab has to
      // put its conversation there or the header reads "Untitled".
      setState((st) => ({ sessions: { ...(st.sessions || {}), lab: { id: "lab", title: "Buscar un bug bounty" } } }));
      setState({ artifacts: { ownerSessionId: null, view: null, fileId: null, from: "chat", expanded: false, seed: null, status: "idle", error: null, items: [], token: 0 } });
    }
    return () => { if (phone) setState({ isMobile: false }); };
  }, [forced, phone]);
  return forced === "artifacts";
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
  const artifactsOpen = useArtifactsSurface(surface, true);
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

        {/* MIGRATED (METODO §4, the phone header): the three capsules have no
            private copy here. Markup and CSS were MOVED to MobileChrome, class
            names and all, and the prototype imports them back. What sits here
            now is only an adapter: the lab's drawers and the yellow specimen
            badge mapped onto the shipped props. The frame (390×780), the
            scrim, the edge gestures and the density label stay — they are
            the host, not the piece. */}
        <MobileChrome
          title="Buscar un bug bounty"
          attention={{ permission: 1, urgent: 1 }}
          open={d.left}
          onToggle={d.setLeft}
          panelOpen={d.right}
          onPanel={(next) => next ? d.setRight(true) : closeRight()}
          inboxCount={0}
        />

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
        {artifactsOpen && <ArtifactsDrawer />}
      </div>
      <p class="zl-hint">
        Swipe in from the left edge for the other sessions, from the right edge
        for this one. Or tap ≡ and the name.
      </p>
    </div>
  );
}

/* ── Desktop ─────────────────────────────────────────────────────────────
   MIGRATED (METODO §4): the crumb has no private copy here. Markup and CSS
   were MOVED to layout/ChatHead, class names and all, and the prototype
   imports them back. What sits here now is only an adapter: the lab's
   session title, path and panel toggle mapped onto the shipped head, plus
   the lab frame (density label, drawn desk, hint) which is the host, not
   the piece. */
function Desktop({ label, live: preset, surface }) {
  const [view, setView] = useState("recent");
  const settings = useSettingsSurface(surface);
  const artifactsOpen = useArtifactsSurface(surface);
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
          <ChatHead
            title="Buscar un bug bounty"
            path="~/dev/moa"
            panelOpen={open}
            onTitleClick={() => open ? panel.close() : panel.show()}
            onPreviewToggle={() => {}}
            onGridToggle={() => {}}
          />
          <Transcript streaming={!!preset.fg && preset.fg.phase === "working"} tail={preset.fg?.phase === "waiting" ? ASK_CARD : null} />
          {set.pick && <div class="zl-veil" onClick={set.close} />}
          <div class="zl-dock">
            <LiveZone {...live} />
            <Composer />
            <StatusLine inline s={set.s} pick={set.pick} onPick={set.setPick} onChange={set.onChange} onUsage={() => panel.show("usage")} />
          </div>
          {open && <div class="zl-scrim" onClick={panel.close} />}
          <SessionPanel open={open} onClose={panel.close} page={panel.page} onPage={panel.setPage} />
          {artifactsOpen && <ArtifactsDrawer />}
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
   MIGRATED (METODO §4): Pane and the bar have no private copy here. Markup
   and CSS were MOVED to layout/Pane, PaneGrid and GridToolbar, class names
   and all, and the prototype imports them back. What sits here now is only
   an adapter: the prototype's 2+1 fixtures mapped onto the shipped pane,
   plus the lab frame (density label, drawn 1298×820 desk, hint) which is
   the host, not the piece.

   A pane is still the conversation column with its chrome compressed: same
   transcript, same dock, same status line in its compact form. */
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
    <ProductionPane
      title={p.title}
      path={p.path}
      state={p.state}
      focused={!!p.focus}
      tileNumber={p.n}
      composer={<Composer />}
      dock={<LiveZone {...live} dense />}
      status={(
        <StatusLine
          inline
          s={set.s}
          compact
          pick={set.pick}
          onPick={set.setPick}
          onChange={set.onChange}
        />
      )}
      overlay={set.pick ? <div class="zl-veil" onClick={set.close} /> : null}
    >
      <Transcript dense streaming={streaming} short={p.n !== 1} tail={p.tail} />
    </ProductionPane>
  );
}

function Grid({ label, live, surface }) {
  return (
    <div class="zl-grid-wrap">
      <div class="zl-density-label">{label}</div>
      <div class="zl-desk zl-grid">
        <GridToolbar paneCount={3} needsYouCount={1} />
        <ProductionPaneGrid>
          <Pane p={PANES[0]} streaming={!!live.fg && live.fg.phase === "working"} live={live} surface={surface} />
          <div class="zl-grid-col">
            <Pane p={PANES[1]} streaming={false} />
            <Pane p={PANES[2]} streaming={false} />
          </div>
        </ProductionPaneGrid>
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
  { id: "model", label: "Model picker", note: "What the model tap opens: a popover above its button on desktop and in a pane, a bottom sheet on the phone. Current model, pinned, the door to all providers (pushed inside with back), thinking, fast." },
  { id: "perm", label: "Permissions", note: "What the permission tap opens: three rows in the line's own colours, one line each of what it does. Pick one and it closes." },
  { id: "settings", label: "Settings", note: "What the gear opens: the GLOBAL settings, so it belongs to no edge — centred on the desktop, a bottom sheet on the phone. Rows, not a form: name and one line of explanation on the left, the value on the right. A choice between several opens a second page inside the same panel." },
  { id: "artifacts", label: "Artifacts", note: "The one list of files in the product, reached from the composer's menu, the head entry, and the dossier's row. Grouped by day in server order, real thumbnails, title first and the filename underneath it. Replaces the panel page that used to list the same files in a different shape." },
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
