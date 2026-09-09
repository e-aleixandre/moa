import { useEffect, useRef, useState } from "preact/hooks";
import "./zones-lab.css";

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
const SESSIONS = [
  { title: "Buscar un bug bounty", when: "now", path: "~/dev/moa", project: "moa", state: "running", brief: "Running · 4m" },
  { title: "Check access to two repos", when: "28m", path: "~/dev/gugo", project: "gugo", state: "needs", brief: "Needs your answer" },
  { title: "Deploy fails on ARM runner", when: "1h", path: "~/dev/tienda", project: "tienda", state: "error", brief: "Stopped with an error" },
  { title: "Limpiar Docker y worktrees", when: "35m", path: "~/dev", project: "dev", state: "idle" },
  { title: "Búscame un dominio para el side project", when: "36m", path: "~/dev", project: "dev", state: "idle" },
  { title: "Browse Gugo GitLab", when: "39d", path: "~/dev/gugo", project: "gugo", state: "idle" },
  { title: "MenuApp", when: "41d", path: "~/dev/menuapp", project: "menuapp", state: "idle" },
];

/* Identity hues, deliberately none of them peach: that one means "you wrote
   this" and may not be spent on decoration. */
const HUES = [210, 265, 170, 320, 40, 190];
function projectHue(name) {
  let h = 0;
  for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) >>> 0;
  return HUES[h % HUES.length];
}

/* Identity and state are two data: the monogram says WHICH project, the dot
   says WHAT it is doing. Folding state into the monogram made the same repo
   change colour from row to row, which defeats the point of a monogram. */
function Monogram({ project }) {
  const hue = projectHue(project);
  return (
    <span class="zl-mono" style={`--h:${hue}`} aria-hidden="true">
      {project.slice(0, 2)}
    </span>
  );
}

const ACTIVE = SESSIONS.filter((s) => s.state !== "idle");
const SAVED = SESSIONS.filter((s) => s.state === "idle");

function Dot({ state }) {
  return <span class={`zl-dot is-${state}`} aria-hidden="true" />;
}

function PlusIcon() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true">
      <path d="M8 3.5v9M3.5 8h9" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" />
    </svg>
  );
}

function Row({ s, current, onPick }) {
  return (
    <button
      type="button"
      class={`zl-row${current ? " is-current" : ""}`}
      aria-current={current ? "true" : undefined}
      onClick={onPick}
    >
      <Monogram project={s.project} />
      <span class="zl-row-main">
        <span class="zl-row-l1">
          <span class="zl-row-title">{s.title}</span>
          <span class="zl-row-meta">
            <Dot state={s.state} />
            <span class="zl-row-when zl-data">{s.when}</span>
          </span>
        </span>
        {/* Active sessions say what they are doing; saved ones say where they
            live. Two lines is the budget, so the more useful datum wins. */}
        {s.brief
          ? <span class={`zl-row-brief is-${s.state}`}>{s.brief}</span>
          : <span class="zl-row-path zl-data">{s.path}</span>}
      </span>
    </button>
  );
}

function SessionList({ onPick }) {
  return (
    <div class="zl-list">
      <div class="zl-group"><span>Active</span><span class="zl-group-n zl-data">{ACTIVE.length}</span></div>
      {ACTIVE.map((s, i) => <Row s={s} current={i === 0} onPick={onPick} key={s.title} />)}
      <div class="zl-group"><span>Saved</span><span class="zl-group-n zl-data">{SAVED.length}</span></div>
      {SAVED.map((s) => <Row s={s} current={false} onPick={onPick} key={s.title} />)}
    </div>
  );
}

/* The left drawer body, shared by both densities. */
function Sidebar({ onPick, desktop }) {
  return (
    <>
      <div class="zl-side-head">
        <span class="zl-side-title">moa</span>
        {/* Search is a recess cut into the sheet: present at rest, so it reads
            as an object you can reach for, but sunken so it never competes
            with the raised things (the active row, New session). */}
        <label class="zl-search">
          <svg class="zl-search-ico" viewBox="0 0 16 16" aria-hidden="true">
            <circle cx="7" cy="7" r="4.5" fill="none" stroke="currentColor" stroke-width="1.6" />
            <path d="M10.5 10.5L14 14" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" />
          </svg>
          <input class="zl-search-in" placeholder="Search" aria-label="Search sessions" />
          {desktop && <kbd class="zl-kbd zl-data">⌘K</kbd>}
        </label>
      </div>
      <SessionList onPick={onPick} />
      {/* New anchors the bottom, where the thumb is and where the empty half of
          the column was. It is the one action, so it gets the width. */}
      <button type="button" class="zl-side-new">
        <PlusIcon />New session
      </button>
      <div class="zl-side-foot">
        <button type="button" class="zl-inbox">
          <svg viewBox="0 0 16 16" aria-hidden="true">
            <path d="M2 9.5V12a1 1 0 0 0 1 1h10a1 1 0 0 0 1-1V9.5M2 9.5h3.2l.8 1.5h4l.8-1.5H14M2 9.5l1.6-5.2A1 1 0 0 1 4.6 3.5h6.8a1 1 0 0 1 1 .8L14 9.5" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round" />
          </svg>
          Inbox
          <span class="zl-inbox-n zl-data">1</span>
        </button>
        <span class="zl-ver zl-data">v0.37.2</span>
      </div>
    </>
  );
}

/* The right drawer: this session. Deliberately excludes model, permissions
   and fast -- those live in the status line, and putting them here too would
   break "one datum, one place". */
function SessionPanel({ onClose }) {
  return (
    <>
      <div class="zl-side-head">
        <span class="zl-side-title is-eyebrow">This session</span>
        <button type="button" class="zl-x" onClick={onClose} aria-label="Close">
          <svg viewBox="0 0 16 16" aria-hidden="true">
            <path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" />
          </svg>
        </button>
      </div>
      <div class="zl-panel-body">
        <label class="zl-field">
          <span class="zl-label">Name</span>
          <input class="zl-input" defaultValue="Buscar un bug bounty" />
        </label>
        <div class="zl-field">
          <span class="zl-label">Folder</span>
          <div class="zl-input is-static zl-data">
            <span class="zl-path-dir">~/dev/moa/</span>main
          </div>
        </div>
        <dl class="zl-facts">
          <div><dt>Started</dt><dd class="zl-data">09:12</dd></div>
          <div><dt>Turns</dt><dd class="zl-data">14</dd></div>
          <div><dt>Branch</dt><dd class="zl-data">design-visual</dd></div>
        </dl>
      </div>
      <div class="zl-panel-acts">
        <button type="button" class="zl-act">
          <span class="zl-act-t">Save for later</span>
          <span class="zl-act-d">Stops the agent, keeps the session in Saved.</span>
        </button>
        <button type="button" class="zl-act is-danger">
          <span class="zl-act-t">Close session</span>
          <span class="zl-act-d">Removes it from the list. The transcript stays on disk.</span>
        </button>
      </div>
    </>
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

function UserMessage({ children, when }) {
  return (
    <div class="zl-user">
      <div class="zl-user-body">{children}</div>
      <span class="zl-user-when zl-data">{when}</span>
    </div>
  );
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

function useStream(playing) {
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
    <div class={`zl-prose is-streaming${s.done ? " is-done" : ""}`} aria-busy={!s.done}>
      {paras.map((children, k) => (
        <p key={k}>
          {children}
          {k === paras.length - 1 && !s.done && (
            <span class={`zl-caret${s.idle ? " is-idle" : ""}`} aria-hidden="true" />
          )}
        </p>
      ))}
    </div>
  );
}

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
    for (const c of el.children) ro.observe(c);
    return () => ro.disconnect();
  }, [streaming]);
  return (
    <div class={`zl-transcript${dense ? " is-dense" : ""}`} ref={ref}>
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

          <div class="zl-turn">
            <div class="zl-prose">
              <p>Voy a mirar primero cómo se ordena el borrado respecto al índice, porque el síntoma que describes (un blob que sobrevive sin dueño) es más típico de una escritura sin lock que de una corrupción del índice.</p>
            </div>
            <Ledger rows={LEDGER_A} dense={dense} />
            <div class="zl-prose">
              <h3>Qué he encontrado</h3>
              <p>Es una carrera, no el índice. <code>Delete</code> quita la entrada del mapa <em>antes</em> de comprobar que existe, así que el segundo borrado no falla y decrementa el contador dos veces:</p>
              <ol>
                <li>La sesión A entra en <code>Delete</code>, toma el lock y borra la entrada.</li>
                <li>La sesión B entra justo después: la entrada ya no está, pero el código no lo comprueba y sigue.</li>
                <li>Las dos decrementan <code>refs</code>; el blob queda en −1 y el GC no lo toca nunca.</li>
              </ol>
              <p>El test que fallaba arriba es el que lo demuestra: esperaba <code>ErrNotFound</code> en el segundo borrado y recibía <code>nil</code>. Con la comprobación dentro del lock pasa.</p>
            </div>
            <Artifact name="attach-race-report.md" kind="md" size="4.2 kB" dense={dense} />
          </div>

          <UserMessage when="09:31">
            Vale. Confírmalo con un test de concurrencia y pasa vet antes de dar por bueno el cambio.
          </UserMessage>
        </>
      )}

      <div class="zl-turn">
        <Ledger rows={streaming ? LEDGER_B : LEDGER_B_DONE} dense={dense} folded={false} />
        <StreamingProse playing={streaming} />
      </div>
      {tail}
    </div>
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

function CtxRing({ pct }) {
  const r = 6.5;
  const c = 2 * Math.PI * r;
  const tone = pct >= 90 ? "is-hot" : pct >= 70 ? "is-warm" : "";
  return (
    <svg class={`zl-ring ${tone}`} viewBox="0 0 16 16" aria-hidden="true">
      <circle cx="8" cy="8" r={r} class="zl-ring-track" />
      <circle
        cx="8" cy="8" r={r} class="zl-ring-arc"
        stroke-dasharray={`${(c * pct) / 100} ${c}`}
        transform="rotate(-90 8 8)"
      />
    </svg>
  );
}

const FULL_STATUS = {
  model: "Daybreak Blue", thinking: "medium", perm: "yolo", fast: true,
  ctx: 63, spend: "$1.84", up: "12.4k", down: "1.8k",
  goal: { iteration: 3 }, tasks: { done: 2, total: 5 },
  mcp: { total: 3, unhealthy: 1 }, onExtra: true,
};

function StatusLine({ s = FULL_STATUS, compact }) {
  return (
    <div class={`zl-status${compact ? " is-compact" : ""}`}>
      {/* tier 1 — settings */}
      <div class="zl-st-group is-settings">
        <button type="button" class="zl-st zl-st-model" aria-label={`Model & thinking: ${s.model}, ${s.thinking}`}>
          <span class="zl-st-word zl-st-model-name">{s.model}</span>
          <ThinkMeter level={s.thinking} />
        </button>
        <button type="button" class={`zl-st zl-st-perm is-${s.perm}`} aria-label={`Permission mode: ${s.perm}`}>
          <span class="zl-st-word">{s.perm}</span>
        </button>
        {s.fast && (
          <span class="zl-st zl-st-fast" title="Fast mode: billed at a premium rate">
            <svg class="zl-st-ico" viewBox="0 0 16 16" aria-hidden="true"><path d="M9 1.5L3.5 9h4l-.5 5.5L12.5 7h-4z" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round" /></svg>
            <span class="zl-st-word">fast</span>
          </span>
        )}
      </div>

      {/* tier 3 — events, only while they exist */}
      <div class="zl-st-group is-events">
        {!compact && s.goal && (
          <span class="zl-st zl-st-ev" title="Goal active, iteration 3">
            <svg class="zl-st-ico" viewBox="0 0 16 16" aria-hidden="true"><circle cx="8" cy="8" r="5.5" fill="none" stroke="currentColor" stroke-width="1.5" /><circle cx="8" cy="8" r="1.8" fill="currentColor" /></svg>
            <span class="zl-st-word">goal</span><span class="zl-data">{s.goal.iteration}</span>
          </span>
        )}
        {!compact && s.tasks && (
          <span class="zl-st zl-st-ev" title="Tasks: 2 of 5 done">
            <svg class="zl-st-ico" viewBox="0 0 16 16" aria-hidden="true"><path d="M3 4.5l1.5 1.5 3-3M3 10.5l1.5 1.5 3-3M9 5h4M9 11h4" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" /></svg>
            <span class="zl-st-word">tasks</span><span class="zl-data">{s.tasks.done}/{s.tasks.total}</span>
          </span>
        )}
        {s.mcp && s.mcp.total > 0 && (
          <button type="button" class={`zl-st zl-st-ev${s.mcp.unhealthy ? " is-alarm-red" : ""}`} aria-label={s.mcp.unhealthy ? `MCP: ${s.mcp.unhealthy} of ${s.mcp.total} need attention` : `MCP: ${s.mcp.total} servers`}>
            <svg class="zl-st-ico" viewBox="0 0 16 16" aria-hidden="true"><path d="M5 2v3M11 2v3M3.5 5h9v3a4.5 4.5 0 0 1-9 0zM8 12.5V15" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" /></svg>
            <span class="zl-st-word">mcp</span>
            <span class="zl-data">{s.mcp.unhealthy ? `${s.mcp.unhealthy}/${s.mcp.total}` : s.mcp.total}</span>
          </button>
        )}
        {s.onExtra && (
          <span class="zl-st zl-st-ev is-alarm-yellow" title="Served from extra usage (pay-as-you-go)">
            <svg class="zl-st-ico" viewBox="0 0 16 16" aria-hidden="true"><path d="M8 1.5c.5 3-3 4-3 8a3 3 0 0 0 6 0c0-1.5-.6-2.5-1.2-3.2-.3 1.2-1 1.7-1.3 1.7C9 6 9.5 3.5 8 1.5z" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round" /></svg>
            <span class="zl-st-word">extra</span>
          </span>
        )}
      </div>

      {/* tier 2 — gauges */}
      <div class="zl-st-group is-gauges">
        <button type="button" class="zl-st zl-st-ctx" aria-label={`Context ${s.ctx}% used, ${s.spend} spent — show usage`}>
          <CtxRing pct={s.ctx} />
          <span class="zl-data zl-num">{s.ctx}<span class="zl-unit">%</span></span>
          <span class="zl-st-sep" aria-hidden="true" />
          <span class="zl-data zl-num zl-st-spend">{s.spend}</span>
        </button>
        <span class="zl-st zl-st-tok zl-data" title="Tokens this run">
          <span class="zl-arrow" aria-hidden="true">↑</span><span class="zl-num">{s.up}</span>
          <span class="zl-arrow" aria-hidden="true">↓</span><span class="zl-num">{s.down}</span>
        </span>
      </div>
    </div>
  );
}

/* Composer. A raised slab floating over the transcript, not a hole in it:
   the field is the thing you look at most, so it gets the most careful
   surface. The send button arms when there is something to send and takes
   the accent -- peach is "you said this", which is what the message becomes
   AFTER sending, not the button. */
function Composer() {
  const [draft, setDraft] = useState("");
  const ref = useRef(null);
  const onInput = (e) => {
    setDraft(e.currentTarget.value);
    const el = e.currentTarget;
    el.style.height = "0";
    el.style.height = `${Math.min(el.scrollHeight, 132)}px`;
  };
  const armed = draft.trim().length > 0;
  return (
    <div class={`zl-composer${armed ? " is-armed" : ""}`}>
      <button type="button" class="zl-attach" aria-label="Attach">
        <PlusIcon />
      </button>
      <textarea
        ref={ref}
        class="zl-ta"
        rows="1"
        placeholder="Message moa"
        value={draft}
        onInput={onInput}
        aria-label="Message"
      />
      <button type="button" class="zl-send" aria-label="Send" disabled={!armed}>
        <svg viewBox="0 0 16 16" aria-hidden="true">
          <path d="M8 13V3.5M8 3.5L3.8 7.7M8 3.5l4.2 4.2" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" />
        </svg>
      </button>
    </div>
  );
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

/* ── Phone ─────────────────────────────────────────────────────────────── */
function Phone({ label }) {
  const host = useRef(null);
  const d = useEdgeDrawers(host);
  const anyOpen = d.left || d.right || d.veil != null;

  return (
    <div class="zl-phone-wrap">
      <div class="zl-density-label">{label}</div>
      <div class="zl-phone" ref={host} {...d.handlers}>
        <Transcript />

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
          <Composer />
          <StatusLine compact />
        </div>

        {anyOpen && (
          <div
            class="zl-scrim"
            style={d.veil != null ? `opacity:${d.veil};transition:none` : ""}
            onClick={() => { d.setLeft(false); d.setRight(false); }}
          />
        )}
        <div
          class={`zl-side zl-side-left${d.left ? " is-open" : ""}`}
          style={d.leftX != null ? `transform:translateX(${d.leftX}px);transition:none` : ""}
        >
          <Sidebar onPick={() => d.setLeft(false)} />
        </div>
        <div
          class={`zl-side zl-side-right${d.right ? " is-open" : ""}`}
          role="dialog"
          aria-label="This session"
          aria-hidden={!d.right}
          style={d.rightX != null ? `transform:translateX(${d.rightX}px);transition:none` : ""}
        >
          <SessionPanel onClose={() => d.setRight(false)} />
        </div>
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

function Desktop({ label }) {
  const [panel, setPanel] = useState(false);
  return (
    <div class="zl-desk-wrap">
      <div class="zl-density-label">{label}</div>
      <div class="zl-desk">
        <div class="zl-desk-side">
          <Sidebar onPick={() => {}} desktop />
        </div>
        <div class="zl-desk-main">
          <div class="zl-desk-head">
            <button type="button" class="zl-crumb" onClick={() => setPanel(true)} aria-expanded={panel}>
              <span class="zl-crumb-title">Buscar un bug bounty</span>
              <span class="zl-crumb-path zl-data">~/dev/moa</span>
            </button>
            <span class="zl-spacer" />
            <HeadActions />
          </div>
          <Transcript />
          <div class="zl-dock">
            <Composer />
            <StatusLine />
          </div>
          {panel && <div class="zl-scrim" onClick={() => setPanel(false)} />}
          <div
            class={`zl-side zl-side-right${panel ? " is-open" : ""}`}
            role="dialog"
            aria-label="This session"
            aria-hidden={!panel}
          >
            <SessionPanel onClose={() => setPanel(false)} />
          </div>
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
  { title: "Check access to two repos", path: "~/dev/gugo", state: "needs", n: 2,
    status: { ...FULL_STATUS, model: "Terra", thinking: "high", perm: "ask", goal: null, tasks: null, mcp: null, onExtra: false, fast: false, ctx: 21, spend: "$0.42", up: "3.1k", down: "640" },
    tail: (
      /* The blocking card is out of scope here (it has its own component in
         production); this stub only shows WHERE it sits -- after the
         transcript, before the composer -- and that "needs you" is yellow. */
      <div class="zl-ask" role="group" aria-label="Permission requested">
        <div class="zl-ask-t">Run <code class="zl-data">git push origin fix/attach-race</code>?</div>
        <div class="zl-ask-acts">
          <button type="button" class="zl-ask-btn is-primary">Allow</button>
          <button type="button" class="zl-ask-btn">Deny</button>
        </div>
      </div>
    ) },
  { title: "Deploy fails on ARM runner", path: "~/dev/tienda", state: "error", n: 3,
    status: { ...FULL_STATUS, model: "Sol", thinking: "high", perm: "auto", goal: null, tasks: null, onExtra: true, fast: false, ctx: 88, spend: "$6.10", up: "41k", down: "9.2k" },
    tail: (
      <div class="zl-sys is-error">Stopped: provider returned <span class="zl-data">529 overloaded</span> three times. Send a message to retry.</div>
    ) },
];

function Pane({ p, streaming }) {
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
      <div class="zl-dock is-pane">
        <Composer />
        <StatusLine s={p.status} compact />
      </div>
    </section>
  );
}

function Grid({ label }) {
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
          <Pane p={PANES[0]} streaming />
          <div class="zl-grid-col">
            <Pane p={PANES[1]} streaming={false} />
            <Pane p={PANES[2]} streaming={false} />
          </div>
        </div>
      </div>
      <p class="zl-hint">
        The 2+1 preset. Each pane is the single conversation with the compact
        status line and a dense transcript; the pane head replaces the crumb
        and carries the session's state dot.
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

export function ZonesLab() {
  useEffect(() => {
    document.documentElement.setAttribute("data-ambient", "on");
    return () => document.documentElement.removeAttribute("data-ambient");
  }, []);
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
      <div class="zl-stage">
        <Phone label="Phone" />
        <Desktop label="Desktop" />
      </div>
      <div class="zl-stage">
        <Grid label="Desktop · grid" />
      </div>
      <StatusLineStudy />
    </div>
  );
}
