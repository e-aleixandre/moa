import { useState } from "preact/hooks";
import { Rewind as RewindIcon, Copy as CopyIcon, Check as CheckIcon } from "lucide-preact";
import { AssistantDocument } from "../components/AssistantDocument/AssistantDocument.jsx";
import { ActivityLedger } from "../components/ActivityLedger/ActivityLedger.jsx";
import { WaypointAttachments } from "../components/UserWaypoint/WaypointAttachments.jsx";
import { copyToClipboard } from "../data/util/format.js";
import "../components/UserWaypoint/UserWaypoint.css";
import "./u4-lab.css";

// u4-lab — CATALOG ONLY. One direction, three treatments of its actions.
//
// The owner chose u3's direction A (the notebook cell) and asked for two
// changes: drop `In [n]` — the gutter carries the SEND TIME instead — and put
// a copy button on the user's message. The cell shape, the slab and the
// prose hanging off the same gutter are u3-A verbatim; the only thing that
// varies across the three is WHERE the actions live and WHEN they show.
//
//   1 · Canalón   actions stack in the gutter under the clock, on hover/focus.
//   2 · Estela    actions ride at the end of the last line, always in flow.
//   3 · Riel      actions pinned to the slab's right edge, always dim.
//
// The gutter now carries two actions (copy and rewind), not one, so each
// treatment has to answer the same three questions: does a one-line message
// grow, can a thumb reach it with no hover, and does the gutter still work
// when the clock also appears on moa's turns (what the owner wants next).
// `UserWaypoint` of production is NOT touched.

const SHOT =
  "iVBORw0KGgoAAAANSUhEUgAAACAAAAAgCAYAAABzenr0AAACRElEQVR4AbXBUUEYQAxAsby2OlCCFCTsb1ZmYeJQASou6ePj82dlZWVlZWVlZWVlZWVlZWVlZWVlZWVlZWVlZWVlZWXlTAghhBBCCCGEEEIIIYQQQgghhBByJoQQQgghhBBCCCGEEEIIIYQQQgg5E0IIIYQQQgghhBBCCCGEEEIIIYScCSGEEEIIIYQQQgghhBBCCCGEEELOhBBCCCGEEEIIIYQQQgghhBBCCCFnQgghhBBCCCGEEEIIIYQQQgghhJAzIYQQQgghhBBCCCGEEEIIIYQQQsiZEEIIIYQQQgghhBBCCCGEEEIIIeRMCCGEEEIIIYQQQgghhBBCCCGEkDMhhBBCCCGEEEIIIYQQQgghhBBCzoQQQgghhBBCCCGEEEIIIYQQQggh5EwIIYQQQgghhBBCCCGEEEIIIYQQciaEEEIIIYQQQgghhBBCCCGEEEIIORNCCCGEEEIIIYQQQgghhBBCCCGEnAkhhBBCCCGEEEIIIYQQQgghhBBCzoQQQgghhBBCCCGEEEIIIYQQQggh5EwIIYQQQgghhBBCCCGEEEIIIYQQciaEEEIIIYQQQgghhBBCCCGEEEIIORNCCCGEEEIIIYQQQgghhBBCCCGEnAkhhJCvP/+98v3vL0LImRBCyFMTQsiZEELIUxNCyJkQQshTE0LImRBCyFMTQsiZEELIUxNCyJkQQshTE0LImRBCyFMTQsiZEELIUxNCyFMTQsjv6ESCQMGDiQIAAAAASUVORK5CYII=";

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
  { id: "d2", tool: "grep", arg: { text: '"Subscribe(" — pkg/bus/' }, live: true, startedAt: Date.now() - 4000 },
];

const LONG = [
  "Two things before you touch it:",
  <>1. The status strip already wraps at 1100px — check <code>StatusStrip.css</code> before adding a word.</>,
  "2. On the phone it goes in the capsule, not the strip. Same word, same colour.",
  "Then run the fidelity harness and tell me which scenes moved.",
];

// u3's conversation verbatim, with `In [n]` replaced by the clock the owner
// asked for. The first message carries `day` because a session resumed the
// next morning is the case the gutter has to survive: the day rides ABOVE the
// time in micro mono, so the time column itself never changes width.
const CONVERSATION = [
  { user: { kind: "short", lines: ["Put fast on the desktop status strip, after yolo."], at: "23:52", day: "ayer", hover: false } },
  { moa: [<p>I'll hang it on the strip, same word as on the phone.</p>, <ActivityLedger rows={LEDGER_1} />, <p>Done. The word sits after the permission chip, same colour as on mobile.</p>] },
  { user: { kind: "long", lines: LONG, at: "09:14", hover: true } },
  { moa: [<p>The strip wraps at 1100px, so the word goes in the overflow group; on the phone I put it in the capsule next to the model.</p>, <ActivityLedger rows={LEDGER_2} />] },
  { user: { kind: "steer", lines: ["Skip the flaky e2e and rerun only the unit suite"], at: "09:38" } },
  { moa: [<p>Rerunning only the unit suite. Three scenes moved, all on the phone: the capsule grew by one word.</p>, <ActivityLedger rows={LEDGER_3} />] },
  { user: { kind: "attachments", lines: ["Mira esto y dime qué falla respecto al informe"], at: "10:02", attachments: ATTACHMENTS } },
  { moa: [<p>The hero in the screenshot uses the Q2 figure; the report says 14.2%, the page says 12.8%. Everything else matches.</p>] },
  { user: { kind: "parent", lines: ["Audit pkg/serve/ws.go for the resume race and report the exact line."], at: "11:08" } },
  { moa: [<p>Reading the reconnect path first to see how the snapshot and the subscription are sequenced.</p>, <ActivityLedger rows={LEDGER_LIVE} />], streaming: true },
];

function Text({ lines, trailing }) {
  const last = lines.length - 1;
  return (
    <div class="u4-text">
      {lines.map((l, i) => (
        <p key={i}>
          {l}
          {trailing && i === last ? trailing : null}
        </p>
      ))}
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

// The clock: the row production never prints, because the client reads
// `msg.ts` and the server sends `timestamp`. Mono + tabular-nums so 09:14 and
// 11:08 occupy the exact same box; the day, when it is not today, rides above
// it rather than beside it, so the column width is a constant.
function Clock({ msg }) {
  return (
    <span class="u4-clock">
      {msg.day && <span class="u4-day">{msg.day}</span>}
      <time class="u4-hhmm zl-data">{msg.at}</time>
    </span>
  );
}

function CopyAction({ text, label = "Copy this message" }) {
  const [done, setDone] = useState(false);
  return (
    <button
      type="button"
      class={`u4-act${done ? " is-done" : ""}`}
      aria-label={label}
      title={done ? "Copied" : "Copy"}
      onClick={() => copyToClipboard(text).then((ok) => { if (ok) { setDone(true); setTimeout(() => setDone(false), 1400); } })}
    >
      {done ? <CheckIcon aria-hidden="true" /> : <CopyIcon aria-hidden="true" />}
    </button>
  );
}

function RewindAction() {
  return (
    <button type="button" class="u4-act" aria-label="Rewind the conversation to this message" title="Rewind here">
      <RewindIcon aria-hidden="true" />
    </button>
  );
}

function Actions({ class: cls = "", text }) {
  return (
    <span class={`u4-acts${cls ? ` ${cls}` : ""}`}>
      <CopyAction text={text} />
      <RewindAction />
    </span>
  );
}

function plain(lines) {
  return lines.map((l) => (typeof l === "string" ? l : "…")).join("\n");
}

function Flow({ U, class: cls }) {
  return (
    <div class={`u4-col ${cls}`}>
      {CONVERSATION.map((t, i) =>
        t.user ? <U key={i} msg={t.user} /> : <Moa key={i} streaming={t.streaming}>{t.moa}</Moa>
      )}
    </div>
  );
}

function cellClass(msg, extra) {
  return `u4-cell ${extra}${msg.kind === "steer" ? " is-steer" : ""}${msg.kind === "parent" ? " is-parent" : ""}${msg.hover ? " is-hover-demo" : ""}`;
}

// ── 1 · Canalón ───────────────────────────────────────────────────────────
// The actions stack in the gutter, directly under the clock, and appear on
// hover or keyboard focus. The gutter below a message is dead space (moa's
// output starts past it), so the pair is absolutely positioned there: a
// one-line message does not grow by a pixel when the buttons arrive. On a
// coarse pointer there is no hover, so they are simply always there, dim —
// which is why a phone cell is taller than a desktop one at rest.
function GutterCell({ msg }) {
  return (
    <div class={cellClass(msg, "u4a-cell")}>
      <div class="u4-gutter">
        <Clock msg={msg} />
        <Actions class="u4a-acts" text={plain(msg.lines)} />
      </div>
      <div class="u4-body">
        <Text lines={msg.lines} />
        <Skirt attachments={msg.attachments} />
        {msg.kind === "parent" && <span class="u4-src">from parent session</span>}
      </div>
    </div>
  );
}

const Variant1 = () => <Flow U={GutterCell} class="u4-1" />;

// ── 2 · Estela ────────────────────────────────────────────────────────────
// The actions are set INSIDE the text, riding immediately after its last
// word, like the marginal marks of a proof. They cost no vertical space on
// any length, they are always visible so the phone needs no special case,
// and they sit where the eye finishes reading instead of where the message
// began. Cost: on a last line that fills the measure they wrap to their own
// line, and they travel — no two messages put them in the same place.
function TrailCell({ msg }) {
  return (
    <div class={cellClass(msg, "u4b-cell")}>
      <div class="u4-gutter">
        <Clock msg={msg} />
      </div>
      <div class="u4-body">
        <Text lines={msg.lines} trailing={<Actions class="u4b-acts" text={plain(msg.lines)} />} />
        <Skirt attachments={msg.attachments} />
        {msg.kind === "parent" && <span class="u4-src">from parent session</span>}
      </div>
    </div>
  );
}

const Variant2 = () => <Flow U={TrailCell} class="u4-2" />;

// ── 3 · Riel ──────────────────────────────────────────────────────────────
// A rail on the slab's right edge, inside it: always rendered, at a light so
// low it reads as part of the slab's texture, and it lifts to full contrast
// when the pointer enters the cell or a key focuses it. The slab reserves the
// rail's width, so nothing ever moves and nothing is discovered by accident:
// the same pixels are the target on a phone, already 44px, with no hover, no
// long press and no menu. Cost: two glyphs on screen at all times, and the
// measure loses 44px.
function RailCell({ msg }) {
  return (
    <div class={cellClass(msg, "u4c-cell")}>
      <div class="u4-gutter">
        <Clock msg={msg} />
      </div>
      <div class="u4-body">
        <Text lines={msg.lines} />
        <Skirt attachments={msg.attachments} />
        {msg.kind === "parent" && <span class="u4-src">from parent session</span>}
        <Actions class="u4c-rail" text={plain(msg.lines)} />
      </div>
    </div>
  );
}

const Variant3 = () => <Flow U={RailCell} class="u4-3" />;

const VARIANTS = [
  {
    id: "1",
    label: "1 · Canalón",
    render: () => <Variant1 />,
    note: "Las acciones se apilan en el canalón, bajo la hora, y aparecen al pasar por encima o al enfocar con teclado. El canalón bajo un mensaje es hueco (mi prosa empieza pasado él), así que el par va posicionado ahí: un mensaje de una línea no crece ni un píxel cuando llegan los botones. En puntero grueso no hay hover, así que están siempre, atenuados — por eso la celda de móvil es más alta en reposo que la de escritorio. En la captura, el segundo mensaje va con el hover forzado para poder juzgar los dos estados a la vez.",
  },
  {
    id: "2",
    label: "2 · Estela",
    render: () => <Variant2 />,
    note: "Las acciones van DENTRO del texto, justo detrás de su última palabra, como las marcas al margen de una prueba de imprenta. No cuestan altura en ninguna longitud, están siempre visibles (el móvil no necesita caso aparte) y caen donde el ojo termina de leer, no donde el mensaje empezó. Coste: en una última línea que llena la medida saltan a su propia línea, y viajan — no hay dos mensajes que las pongan en el mismo sitio.",
  },
  {
    id: "3",
    label: "3 · Riel",
    render: () => <Variant3 />,
    note: "Un riel en el borde derecho de la losa, por dentro: siempre presente, a una luz tan baja que se lee como textura de la losa, y sube a contraste pleno cuando el puntero entra en la celda o el teclado la enfoca. La losa reserva su anchura, así que nada se mueve y nada se descubre por accidente: los mismos píxeles son la diana en el teléfono, ya a 44px, sin hover, sin pulsación larga y sin menú. Coste: dos glifos en pantalla todo el rato, y la medida pierde 44px.",
  },
];

// Not decided, shown only so the growth is visible: if the actions ever
// become more than two, the rail is the one of the three that already has a
// place to put them (it expands along its own edge, no new surface). The
// owner raised a horizontal menu and dropped it himself — this is the sketch
// of what it would cost, not a proposal.
function MenuSketch() {
  return (
    <div class="u4-cell u4c-cell is-hover-demo">
      <div class="u4-gutter"><Clock msg={{ at: "09:14" }} /></div>
      <div class="u4-body">
        <Text lines={["Put fast on the desktop status strip, after yolo."]} />
        <span class="u4-acts u4c-rail is-wide">
          <CopyAction text="Put fast on the desktop status strip, after yolo." />
          <RewindAction />
          <button type="button" class="u4-act" aria-label="Quote this message" title="Quote"><CopyIcon aria-hidden="true" /></button>
          <button type="button" class="u4-act" aria-label="Edit and resend" title="Edit"><RewindIcon aria-hidden="true" /></button>
        </span>
      </div>
    </div>
  );
}

function Frame({ id, kind, children }) {
  return (
    <div class={`u4-frame u4-${kind}`} data-shot={`${id}-${kind === "desk" ? "desktop" : "movil"}`}>
      {children}
    </div>
  );
}

export function U4Lab() {
  return (
    <div class="u4">
      <header class="u4-head">
        <h1>celda · <em>dónde viven las acciones</em></h1>
        <p>
          La dirección elegida (celda de notebook) con los dos cambios pedidos: fuera <code>In [n]</code>, la
          hora de envío en el canalón, y un botón de copiar junto al rewind. Lo único que cambia entre las
          tres es el tratamiento de las acciones. Misma conversación entera, escritorio (680) y móvil (390),
          con corto, largo con saltos, steer, adjuntos y tarea del padre. Prosa y ledger son las piezas reales;
          <code>UserWaypoint</code> de producción no se toca.
        </p>
      </header>
      {VARIANTS.map((v) => (
        <section class="u4-dir" id={`u4-${v.id}`} key={v.id}>
          <header class="u4-dir-head">
            <h2>{v.label}</h2>
            <p>{v.note}</p>
          </header>
          <div class="u4-strip">
            <Frame id={v.id} kind="desk">{v.render()}</Frame>
            <Frame id={v.id} kind="phone">{v.render()}</Frame>
          </div>
        </section>
      ))}
      <section class="u4-dir" id="u4-menu">
        <header class="u4-dir-head">
          <h2>Extra · no decidido: si algún día son más de dos</h2>
          <p>
            El dueño apuntó un menú horizontal y él mismo lo descartó. No es una propuesta: sólo enseña que,
            de las tres, el riel es la única que ya tiene sitio donde crecer — se alarga por su propio borde,
            sin abrir ninguna superficie nueva. Las otras dos tendrían que inventar un contenedor.
          </p>
        </header>
        <div class="u4-strip">
          <div class="u4-frame u4-desk"><div class="u4-col u4-3"><MenuSketch /></div></div>
        </div>
      </section>
    </div>
  );
}
