import { Rewind as RewindIcon, CornerDownRight, ChevronDown } from "lucide-preact";
import { AssistantDocument } from "../components/AssistantDocument/AssistantDocument.jsx";
import { ActivityLedger } from "../components/ActivityLedger/ActivityLedger.jsx";
import { WaypointAttachments } from "../components/UserWaypoint/WaypointAttachments.jsx";
import "../components/UserWaypoint/UserWaypoint.css";
import "./u2-lab.css";

// u2-lab — the user's message, asked from scratch.
//
// Three directions that each drop a premise of today's message (peach edge,
// same axis as the prose, one look for every case, painted the same fresh and
// two hours later). None of them is "today with the rewind moved". Static
// markup, nothing wired: the shipped UserWaypoint is untouched. Every
// direction shows the SAME whole conversation — five messages of yours, the
// work between them — because what is judged is the conversation, not the
// piece.
//
//   A · Encabezado — the message is a section heading: a rule cuts the page,
//       the first line is the title, the rest is body. Rewind = the mark on
//       the rule ("cut here"). A steer is not a section, it is an aside.
//   B · Índice — every message of yours is a sticky one-line row with a
//       running number; past ones fold to that row, the latest keeps its
//       body. Scrolling a long answer, the question stays at the top.
//   C · Hilo — the message carries no mark at all; it is what moa's work
//       hangs from. The thread line is on the ANSWER, the rewind is the knot
//       where it starts, a steer is a branch off the thread.

const SHOT =
  "iVBORw0KGgoAAAANSUhEUgAAACAAAAAgCAYAAABzenr0AAACRElEQVR4AbXBUUEYQAxAsby2OlCCFCTsb1ZmYeJQASou6ePj82dlZWVlZWVlZWVlZWVlZWVlZWVlZWVlZWVlZWVlZWXlTAghhBBCCCGEEEIIIYQQQgghhBByJoQQQgghhBBCCCGEEEIIIYQQQgg5E0IIIYQQQgghhBBCCCGEEEIIIYScCSGEEEIIIYQQQgghhBBCCCGEEELOhBBCCCGEEEIIIYQQQgghhBBCCCFnQgghhBBCCCGEEEIIIYQQQgghhJAzIYQQQgghhBBCCCGEEEIIIYQQQsiZEEIIIYQQQgghhBBCCCGEEEIIIeRMCCGEEEIIIYQQQgghhBBCCCGEEHImhBBCCCGEEEIIIYQQQgghhBBCCDkTQgghhBBCCCGEEEIIIYQQQgghhJwJIYQQQgghhBBCCCGEEEIIIYQQQs6EEEIIIYQQQgghhBBCCCGEEEIIIWdCCCGEEEIIIYQQQgghhBBCCCGEkDMhhBBCCCGEEEIIIYQQQgghhBBCyJkQQgghhBBCCCGEEEIIIYQQQggh5EwIIYQQQgghhBBCCCGEEEIIIYQQciaEEEIIIYQQQgghhBBCCCGEEEIIORNCCCGEEEIIIYQQQgghhBBCCCGEnAkhhBBCCCGEEEIIIYQQQgghhBBCzoQQQr7+/PfK97+/CCFnQgghT00IIWdCCCFPTQghZ0IIIU9NCCFnQgghT00IIWdCCCFPTQghZ0IIIU9NCCFnQgghT00IIWdCCCFPTQghZ0IIIU9NCCFnQgghT00IIb+jEwkCBQ8mCgAAAABJRU5ErkJggg==";

const ATTACHMENTS = [
  { type: "image", data: SHOT, attachment_size: 862208, mime_type: "image/png", filename: "hero-desktop.png" },
  { type: "document", attachment_id: "att-informe", attachment_size: 1468006, mime_type: "application/pdf", filename: "informe-q3.pdf" },
];

const LEDGER_1 = [
  { id: "a1", tool: "read", arg: { text: "pkg/serve/frontend/src/layout/StatusStrip/StatusStrip.jsx" }, out: "412 lines", status: "ok" },
  { id: "a2", tool: "edit", arg: { text: "pkg/serve/frontend/src/layout/StatusStrip/StatusStrip.jsx" }, out: "7 lines", status: "ok" },
];
const LEDGER_2 = [
  { id: "b1", tool: "read", arg: { text: "pkg/serve/frontend/src/layout/StatusStrip/StatusStrip.css" }, out: "188 lines", status: "ok" },
  { id: "b2", tool: "edit", arg: { text: "pkg/serve/frontend/src/layout/mobile/MobileStatusLine/MobileStatusLine.jsx" }, out: "+9 −2", status: "ok" },
  { id: "b3", tool: "bash", arg: { text: "node scripts/fidelity.mjs --piece status-line" }, out: "3 moved", status: "err" },
];
const LEDGER_3 = [
  { id: "c1", tool: "bash", arg: { text: "bun test src/layout/StatusStrip" }, out: "41 pass", status: "ok" },
];
const LEDGER_LIVE = [
  { id: "d1", tool: "read", arg: { text: "pkg/serve/ws.go", detail: "lines 210–340" }, out: "130 lines", status: "ok" },
  {
    id: "d2",
    tool: "grep",
    arg: { text: '"Subscribe(" — pkg/bus/' },
    live: true,
    startedAt: Date.now() - 4000,
  },
];

// The conversation, once. `kind` is what discriminates: short, long (with the
// user's own line breaks), steer (sent while moa was working), attachments,
// and a task that came from the parent session.
const LONG = [
  "Two things before you touch it:",
  <>1. The status strip already wraps at 1100px — check <code>StatusStrip.css</code> before adding a word.</>,
  "2. On the phone it goes in the capsule, not the strip. Same word, same colour.",
  "Then run the fidelity harness and tell me which scenes moved.",
];

const CONVERSATION = [
  { user: { kind: "short", lines: ["Put fast on the desktop status strip, after yolo."], n: 1 } },
  { moa: [<p>I'll hang it on the strip, same word as on the phone.</p>, <ActivityLedger rows={LEDGER_1} />, <p>Done. The word sits after the permission chip, same colour as on mobile.</p>] },
  { user: { kind: "long", lines: LONG, n: 2 } },
  { moa: [<p>The strip wraps at 1100px, so the word goes in the overflow group; on the phone I put it in the capsule next to the model.</p>, <ActivityLedger rows={LEDGER_2} />] },
  { user: { kind: "steer", lines: ["Skip the flaky e2e and rerun only the unit suite"], n: 3 } },
  { moa: [<p>Rerunning only the unit suite. Three scenes moved, all on the phone: the capsule grew by one word.</p>, <ActivityLedger rows={LEDGER_3} />] },
  { user: { kind: "attachments", lines: ["Mira esto y dime qué falla respecto al informe"], n: 4, attachments: ATTACHMENTS } },
  { moa: [<p>The hero in the screenshot uses the Q2 figure; the report says 14.2%, the page says 12.8%. Everything else matches.</p>] },
  { user: { kind: "parent", lines: ["Audit pkg/serve/ws.go for the resume race and report the exact line."], n: 5 } },
  { moa: [<p>Reading the reconnect path first to see how the snapshot and the subscription are sequenced.</p>, <ActivityLedger rows={LEDGER_LIVE} />], streaming: true },
];

function Text({ lines, class: cls = "" }) {
  return (
    <div class={`u2-text${cls ? ` ${cls}` : ""}`}>
      {lines.map((l, i) => <p key={i}>{l}</p>)}
    </div>
  );
}

function Moa({ children, streaming }) {
  return <AssistantDocument streaming={streaming}>{children}</AssistantDocument>;
}

function Skirt({ attachments }) {
  if (!attachments) return null;
  return <WaypointAttachments attachments={attachments} sessionId="demo" onOpenImage={() => {}} />;
}

// ── A · Encabezado ────────────────────────────────────────────────────────
// The rule is the boundary; the mark on it is the rewind ("cut the
// conversation here"), always at the start of the rule whatever the text's
// length. The first line is the title; a long message has a body under it.
// A steer opens no section: it is an aside inside the current one.
function HeadingUser({ msg }) {
  const [first, ...rest] = msg.lines;
  if (msg.kind === "steer") {
    return (
      <div class="u2a-aside">
        <CornerDownRight aria-hidden="true" />
        <span class="u2a-aside-tag">steer</span>
        <Text lines={msg.lines} />
        <button type="button" class="u2-mark u2-mark-inline" aria-label="Rewind the conversation to this message" title="Rewind here">
          <RewindIcon aria-hidden="true" />
        </button>
      </div>
    );
  }
  return (
    <header class={`u2a-head${msg.kind === "parent" ? " is-parent" : ""}`}>
      <div class="u2a-rule">
        <button type="button" class="u2-mark" aria-label="Rewind the conversation to this message" title="Rewind here">
          <RewindIcon aria-hidden="true" />
        </button>
        <hr />
        {msg.kind === "parent" && <span class="u2a-from">from parent</span>}
        <span class="u2a-n zl-data">{String(msg.n).padStart(2, "0")}</span>
      </div>
      <Text lines={[first]} class="u2a-title" />
      {rest.length > 0 && <Text lines={rest} class="u2a-body" />}
      <Skirt attachments={msg.attachments} />
    </header>
  );
}

function DirectionA() {
  return (
    <div class="u2-col u2-a">
      {CONVERSATION.map((t, i) =>
        t.user ? <HeadingUser key={i} msg={t.user} /> : <Moa key={i} streaming={t.streaming}>{t.moa}</Moa>
      )}
    </div>
  );
}

// ── B · Índice ────────────────────────────────────────────────────────────
// Every message of yours is one sticky row: number, first line, rewind, and a
// fold count when there is more. Past messages ARE the row; the latest keeps
// its full text below it. Scroll a long answer and its question stays put.
function IndexUser({ msg, latest, children }) {
  const [first, ...rest] = msg.lines;
  const folded = !latest;
  const hidden = rest.length + (msg.attachments ? msg.attachments.length : 0);
  return (
    <section class={`u2b-sec${msg.kind === "steer" ? " is-steer" : ""}${msg.kind === "parent" ? " is-parent" : ""}`}>
      <div class="u2b-row">
        <span class="u2b-n zl-data">{String(msg.n).padStart(2, "0")}</span>
        {msg.kind === "steer" && <CornerDownRight class="u2b-steer-ico" aria-hidden="true" />}
        {msg.kind === "parent" && <span class="u2b-tag">parent</span>}
        <span class="u2b-first">{first}</span>
        {folded && hidden > 0 && (
          <button type="button" class="u2b-more" aria-expanded="false">
            +{hidden} <ChevronDown aria-hidden="true" />
          </button>
        )}
        <button type="button" class="u2-mark" aria-label="Rewind the conversation to this message" title="Rewind here">
          <RewindIcon aria-hidden="true" />
        </button>
      </div>
      {!folded && rest.length > 0 && <Text lines={rest} class="u2b-body" />}
      {!folded && <Skirt attachments={msg.attachments} />}
      <div class="u2b-work">{children}</div>
    </section>
  );
}

// The row sticks for as long as its section is on screen, so each section
// holds the work that answered it: one question at the top at a time.
function DirectionB({ height }) {
  const groups = [];
  for (const t of CONVERSATION) {
    if (t.user) groups.push({ user: t.user, items: [] });
    else groups[groups.length - 1].items.push(t);
  }
  return (
    <div class="u2-col u2-b u2-scroll" style={{ height: `${height}px` }}>
      {groups.map((g, i) => (
        <IndexUser key={i} msg={g.user} latest={i === groups.length - 1}>
          {g.items.map((t, j) => <Moa key={j} streaming={t.streaming}>{t.moa}</Moa>)}
        </IndexUser>
      ))}
    </div>
  );
}

// ── C · Hilo ──────────────────────────────────────────────────────────────
// The message has no mark: plain text on the canvas, the root of a thread.
// The line is on moa's WORK, hanging from the message; the rewind is the
// knot where the thread starts, under the first letter. A steer sent while
// moa works is a branch off the thread, not a new root.
function ThreadRoot({ msg }) {
  return (
    <div class={`u2c-root${msg.kind === "parent" ? " is-parent" : ""}`}>
      {msg.kind === "parent" && <span class="u2c-from">from parent</span>}
      <Text lines={msg.lines} />
      <Skirt attachments={msg.attachments} />
    </div>
  );
}

function ThreadBranch({ msg }) {
  return (
    <div class="u2c-branch">
      <span class="u2c-branch-tag">steer</span>
      <Text lines={msg.lines} />
      <button type="button" class="u2-mark u2c-branch-mark" aria-label="Rewind the conversation to this message" title="Rewind here">
        <RewindIcon aria-hidden="true" />
      </button>
    </div>
  );
}

function DirectionC() {
  // Group: each root user message opens a thread that runs until the next
  // root; a steer stays inside the thread it interrupted.
  const groups = [];
  for (const t of CONVERSATION) {
    if (t.user && t.user.kind !== "steer") groups.push({ root: t.user, items: [] });
    else groups[groups.length - 1].items.push(t);
  }
  return (
    <div class="u2-col u2-c">
      {groups.map((g, i) => (
        <section class="u2c-sec" key={i}>
          <ThreadRoot msg={g.root} />
          <div class="u2c-thread">
            <button type="button" class="u2-mark u2c-knot" aria-label="Rewind the conversation to this message" title="Rewind here">
              <RewindIcon aria-hidden="true" />
            </button>
            {g.items.map((t, j) =>
              t.user ? <ThreadBranch key={j} msg={t.user} /> : <Moa key={j} streaming={t.streaming}>{t.moa}</Moa>
            )}
          </div>
        </section>
      ))}
    </div>
  );
}

const DIRECTIONS = [
  {
    id: "a",
    label: "A · Encabezado",
    note: "El mensaje no es un elemento del transcript: es el título del tramo. Una regla corta la página, la primera línea va en título y el resto en cuerpo; todo lo que hago cuelga debajo. El rewind es la marca sobre la regla («corta aquí»), siempre al principio de la regla, a la misma distancia del texto sea cual sea su longitud. Un steer no abre tramo: es un aparte dentro del actual. Sin melocotón.",
    render: () => <DirectionA />,
  },
  {
    id: "b",
    label: "B · Índice",
    note: "Un mensaje tuyo es una fila pegajosa de una línea: número, primera línea, rewind, y «+n» si hay más. Los pasados SON esa fila (se despliegan al tocar); el último conserva su cuerpo. Al recorrer una respuesta larga, la pregunta que la abrió se queda arriba. Incómoda: esconde texto que escribiste hasta que lo tocas. Sin melocotón. Capturado con scroll a media conversación.",
    render: (h) => <DirectionB height={h} />,
  },
  {
    id: "c",
    label: "C · Hilo",
    note: "El mensaje no lleva marca. Es texto plano en el lienzo, raíz de un hilo: la línea la lleva MI trabajo, colgando de tu mensaje, y el rewind es el nudo donde arranca el hilo, bajo tu primera letra. Un steer es una rama del hilo, no otra raíz. El coste: mi prosa cede 20px de medida. Sin melocotón.",
    render: () => <DirectionC />,
  },
];

function Frame({ id, kind, children }) {
  return (
    <div class={`u2-frame u2-${kind}`} data-shot={`${id}-${kind === "desk" ? "desktop" : "movil"}`}>
      {children}
    </div>
  );
}

export function U2Lab() {
  return (
    <div class="u2">
      <header class="u2-head">
        <h1>mensaje del usuario · <em>desde cero</em></h1>
        <p>
          Tres direcciones, la misma conversación entera en escritorio (680) y en el móvil (390).
          Ninguna es «lo de ahora con el rewind en otro sitio». Maqueta estática con las piezas reales
          de producción para la prosa y el ledger; el mensaje es nuevo en cada una.
        </p>
      </header>
      {DIRECTIONS.map((d) => (
        <section class="u2-dir" id={`u2-${d.id}`} key={d.id}>
          <header class="u2-dir-head">
            <h2>{d.label}</h2>
            <p>{d.note}</p>
          </header>
          <div class="u2-strip">
            <Frame id={d.id} kind="desk">{d.render(820)}</Frame>
            <Frame id={d.id} kind="phone">{d.render(720)}</Frame>
          </div>
        </section>
      ))}
    </div>
  );
}
