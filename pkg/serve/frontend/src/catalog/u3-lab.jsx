import { Rewind as RewindIcon, CornerDownRight } from "lucide-preact";
import { AssistantDocument } from "../components/AssistantDocument/AssistantDocument.jsx";
import { ActivityLedger } from "../components/ActivityLedger/ActivityLedger.jsx";
import { WaypointAttachments } from "../components/UserWaypoint/WaypointAttachments.jsx";
import "../components/UserWaypoint/UserWaypoint.css";
import "./u3-lab.css";

// u3-lab — CATALOG ONLY. Six treatments of the user's message, fast round.
//
// The fixture is u2-lab's conversation verbatim (same five messages, same
// work between them) so the six can be compared frame to frame. What is new
// is only the user's message. None of them is today's peach edge, none is a
// chat bubble, and none reproduces u2's numbered heading / sticky index /
// knotted thread.
//
//   A · Celda      the message is an input cell of a notebook (In [3]).
//   B · Margen     the message leaves the prose axis and lives in the margin.
//   C · Prompt     the message is a typed command line; moa's work is output.
//   D · Papel      the message is an inverted paper insert, full bleed.
//   E · Deriva     age is the design: old messages shrink and fade to a trace.
//   F · Hunk       the message is a diff hunk applied to the conversation.

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
  { id: "d2", tool: "grep", arg: { text: '"Subscribe(" — pkg/bus/' }, live: true, startedAt: Date.now() - 4000 },
];

const LONG = [
  "Two things before you touch it:",
  <>1. The status strip already wraps at 1100px — check <code>StatusStrip.css</code> before adding a word.</>,
  "2. On the phone it goes in the capsule, not the strip. Same word, same colour.",
  "Then run the fidelity harness and tell me which scenes moved.",
];

// The conversation, once, shared by the six. `kind` discriminates: short,
// long (own line breaks), steer, attachments, parent. `at`/`ago` exist
// because two directions want the clock the shipped row never prints.
const CONVERSATION = [
  { user: { kind: "short", lines: ["Put fast on the desktop status strip, after yolo."], n: 1, at: "09:14", ago: "2 h" } },
  { moa: [<p>I'll hang it on the strip, same word as on the phone.</p>, <ActivityLedger rows={LEDGER_1} />, <p>Done. The word sits after the permission chip, same colour as on mobile.</p>] },
  { user: { kind: "long", lines: LONG, n: 2, at: "09:31", ago: "97 m" } },
  { moa: [<p>The strip wraps at 1100px, so the word goes in the overflow group; on the phone I put it in the capsule next to the model.</p>, <ActivityLedger rows={LEDGER_2} />] },
  { user: { kind: "steer", lines: ["Skip the flaky e2e and rerun only the unit suite"], n: 3, at: "09:38", ago: "90 m" } },
  { moa: [<p>Rerunning only the unit suite. Three scenes moved, all on the phone: the capsule grew by one word.</p>, <ActivityLedger rows={LEDGER_3} />] },
  { user: { kind: "attachments", lines: ["Mira esto y dime qué falla respecto al informe"], n: 4, at: "10:02", ago: "66 m", attachments: ATTACHMENTS } },
  { moa: [<p>The hero in the screenshot uses the Q2 figure; the report says 14.2%, the page says 12.8%. Everything else matches.</p>] },
  { user: { kind: "parent", lines: ["Audit pkg/serve/ws.go for the resume race and report the exact line."], n: 5, at: "11:08", ago: "ahora" } },
  { moa: [<p>Reading the reconnect path first to see how the snapshot and the subscription are sequenced.</p>, <ActivityLedger rows={LEDGER_LIVE} />], streaming: true },
];

function Text({ lines, class: cls = "" }) {
  return (
    <div class={`u3-text${cls ? ` ${cls}` : ""}`}>
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

function Mark({ class: cls = "" }) {
  return (
    <button type="button" class={`u3-mark${cls ? ` ${cls}` : ""}`} aria-label="Rewind the conversation to this message" title="Rewind here">
      <RewindIcon aria-hidden="true" />
    </button>
  );
}

// Generic walker: render user messages with `U`, moa turns with the shipped
// document, in one flat column.
function Flow({ U, class: cls }) {
  return (
    <div class={`u3-col ${cls}`}>
      {CONVERSATION.map((t, i) =>
        t.user ? <U key={i} msg={t.user} last={i === CONVERSATION.length - 2} /> : <Moa key={i} streaming={t.streaming}>{t.moa}</Moa>
      )}
    </div>
  );
}

// ── A · Celda ─────────────────────────────────────────────────────────────
// The premise dropped: a message is a thing said. Here it is a thing RUN.
// A notebook input cell: a mono label in the gutter (In [3]), the text on a
// raised slab, moa's answer is the cell's output — unlabelled, flush left,
// hanging off the same gutter. The rewind is "re-run from this cell", so it
// belongs on the gutter, one line below the label, never 311px away.
function CellUser({ msg }) {
  return (
    <div class={`u3a-cell${msg.kind === "steer" ? " is-steer" : ""}${msg.kind === "parent" ? " is-parent" : ""}`}>
      <div class="u3a-gutter">
        <span class="u3a-label">{msg.kind === "steer" ? "In*" : "In"} [{msg.n}]</span>
        <Mark />
      </div>
      <div class="u3a-body">
        <Text lines={msg.lines} />
        <Skirt attachments={msg.attachments} />
        {msg.kind === "parent" && <span class="u3a-src">from parent session</span>}
      </div>
    </div>
  );
}

const DirectionA = () => <Flow U={CellUser} class="u3-a" />;

// ── B · Margen ────────────────────────────────────────────────────────────
// The premise dropped: your words and mine share one axis and one measure.
// Here the prose keeps the 680 column and the user's message steps OUT of
// it, into the left margin: narrow, right-aligned against the column edge,
// set small and bright, like a hand-written note beside a printed document.
// Nothing interrupts the reading flow — the page never breaks. On the phone
// there is no margin, so it becomes a flush-left note with a short rule.
function MarginUser({ msg }) {
  return (
    <div class={`u3b-note${msg.kind === "steer" ? " is-steer" : ""}${msg.kind === "parent" ? " is-parent" : ""}`}>
      <aside class="u3b-margin">
        <Text lines={msg.lines} />
        {msg.kind === "steer" && <span class="u3b-tag">mientras trabajaba</span>}
        {msg.kind === "parent" && <span class="u3b-tag">de la sesión padre</span>}
        <Mark class="u3b-mark" />
      </aside>
      <div class="u3b-skirt"><Skirt attachments={msg.attachments} /></div>
    </div>
  );
}

const DirectionB = () => <Flow U={MarginUser} class="u3-b" />;

// ── C · Prompt ────────────────────────────────────────────────────────────
// The premise dropped: what you write is prose. In a work log it is an
// instruction — so it is set as one: mono, a caret, the clock in the gutter
// (the row that today is empty because the client reads msg.ts and the
// server sends timestamp; this direction wants it, so it gets fixed). moa's
// prose is the output of the command, in the proportional face, so the two
// authors never look alike for a moment.
function PromptUser({ msg }) {
  return (
    <div class={`u3c-cmd${msg.kind === "steer" ? " is-steer" : ""}${msg.kind === "parent" ? " is-parent" : ""}`}>
      <span class="u3c-clock zl-data">{msg.at}</span>
      <span class="u3c-caret" aria-hidden="true">{msg.kind === "steer" ? "»" : "❯"}</span>
      <div class="u3c-body">
        <Text lines={msg.lines} />
        <Skirt attachments={msg.attachments} />
      </div>
      <Mark />
    </div>
  );
}

const DirectionC = () => <Flow U={PromptUser} class="u3-c" />;

// ── D · Papel ─────────────────────────────────────────────────────────────
// The premise dropped: everything lives on the same dark canvas. The user's
// message is a sheet of paper slid into the stream: inverted, full bleed to
// the frame edges, dark ink on light. It cannot be mistaken for anything
// moa produces, at any scroll speed, and it costs no colour at all — peach
// is freed. Cost: the biggest luminance jump of the six; the one to judge
// on comfort, not on identity.
function PaperUser({ msg }) {
  return (
    <div class={`u3d-paper${msg.kind === "steer" ? " is-steer" : ""}${msg.kind === "parent" ? " is-parent" : ""}`}>
      <div class="u3d-inner">
        <Text lines={msg.lines} />
        <Skirt attachments={msg.attachments} />
      </div>
      <div class="u3d-edge">
        {msg.kind === "steer" && <span class="u3d-tag">steer</span>}
        {msg.kind === "parent" && <span class="u3d-tag">padre</span>}
        <span class="u3d-clock zl-data">{msg.at}</span>
        <Mark />
      </div>
    </div>
  );
}

const DirectionD = () => <Flow U={PaperUser} class="u3-d" />;

// ── E · Deriva ────────────────────────────────────────────────────────────
// The premise dropped: a message from two hours ago is painted like the one
// you just sent. Here recency IS the treatment, typographically and with no
// control to press: the live message is large and bright with air around it,
// the previous one a step down, and everything older drifts to a dim trace
// with its age in mono. Nothing is hidden — the text is all there, only
// quieter, so a long session reads as a slope towards now.
function DriftUser({ msg }) {
  const depth = Math.min(4, 5 - msg.n); // 0 = live, 4 = oldest
  return (
    <div class={`u3e-msg u3e-d${depth}${msg.kind === "steer" ? " is-steer" : ""}${msg.kind === "parent" ? " is-parent" : ""}`}>
      <div class="u3e-meta">
        <span class="u3e-age zl-data">{msg.ago}</span>
        {msg.kind === "steer" && <CornerDownRight class="u3e-ico" aria-hidden="true" />}
        <Mark />
      </div>
      <Text lines={msg.lines} />
      <Skirt attachments={msg.attachments} />
    </div>
  );
}

const DirectionE = () => <Flow U={DriftUser} class="u3-e" />;

// ── F · Hunk ──────────────────────────────────────────────────────────────
// The premise dropped: the message is content inside the log. Here it is the
// change applied TO it — a unified-diff hunk: a mono hunk line carrying the
// range and the rewind (revert to before this hunk), then the text with a
// "+" gutter, on a faint added-line wash. moa's work is the resulting file,
// unmarked. Reads as a patch series, which is exactly what a work session is.
function HunkUser({ msg }) {
  return (
    <div class={`u3f-hunk${msg.kind === "steer" ? " is-steer" : ""}${msg.kind === "parent" ? " is-parent" : ""}`}>
      <div class="u3f-at">
        <span class="u3f-range">@@ msg {msg.n} @@</span>
        {msg.kind === "steer" && <span class="u3f-note">durante el trabajo</span>}
        {msg.kind === "parent" && <span class="u3f-note">heredado del padre</span>}
        <span class="u3f-rule" />
        <Mark />
      </div>
      <div class="u3f-lines">
        {msg.lines.map((l, i) => (
          <div class="u3f-line" key={i}>
            <span class="u3f-plus" aria-hidden="true">+</span>
            <span class="u3f-txt">{l}</span>
          </div>
        ))}
        <Skirt attachments={msg.attachments} />
      </div>
    </div>
  );
}

const DirectionF = () => <Flow U={HunkUser} class="u3-f" />;

const DIRECTIONS = [
  { id: "a", label: "A · Celda", wide: false, render: () => <DirectionA />, note: "Tu mensaje no es algo dicho: es algo ejecutado. Celda de entrada de notebook — etiqueta mono en el canalón (In [3]), texto en una losa elevada, y mi respuesta es la salida de esa celda, sin etiqueta, colgando del mismo canalón. El rewind vive en el canalón («re-ejecutar desde aquí»), a 0px de su texto en corto y en largo. Sin melocotón." },
  { id: "b", label: "B · Margen", wide: true, render: () => <DirectionB />, note: "Rompe que tu texto y el mío compartan eje y anchura. Mi prosa se queda en la columna de 680; tu mensaje sale de ella al margen izquierdo: estrecho, alineado a la derecha contra el borde de la columna, como una nota a mano junto a un documento impreso. La lectura nunca se corta. En móvil no hay margen: nota a bandera con regla corta. Sin melocotón." },
  { id: "c", label: "C · Prompt", wide: false, render: () => <DirectionC />, note: "Rompe que lo que escribes sea prosa: en un log de trabajo es una instrucción, y se compone como tal — mono, cursor ❯, y la hora en el canalón (esa fila hoy está vacía por el bug ts/timestamp; esta dirección la quiere, así que se arregla). Mi prosa sigue proporcional: los dos autores no se parecen ni un instante. Sin melocotón." },
  { id: "d", label: "D · Papel", wide: false, render: () => <DirectionD />, note: "Rompe que todo viva sobre el mismo lienzo oscuro. Tu mensaje es una hoja de papel metida en el flujo: invertida, a sangre hasta los bordes, tinta oscura sobre claro. No se confunde con nada mío a ninguna velocidad de scroll y no gasta ni un color — el melocotón queda libre. Coste: el salto de luminancia más grande de las seis; júzgala por comodidad, no por identidad." },
  { id: "e", label: "E · Deriva", wide: false, render: () => <DirectionE />, note: "Rompe que un mensaje de hace dos horas se pinte igual que el que acabas de mandar. La antigüedad ES el tratamiento, sólo tipográfico y sin ningún control que pulsar: el vivo grande y brillante con aire, el anterior un escalón por debajo, y los viejos derivan a un rastro tenue con su edad en mono. No esconde nada: el texto está entero, sólo más callado. Sin melocotón." },
  { id: "f", label: "F · Hunk", wide: false, render: () => <DirectionF />, note: "Rompe que el mensaje sea contenido dentro del log: aquí es el cambio aplicado AL log. Hunk de diff unificado — línea mono con el rango y el rewind (revertir a antes de este hunk), texto con canalón «+» sobre un lavado de línea añadida, y mi trabajo es el fichero resultante, sin marca. Se lee como una serie de parches, que es lo que es una sesión. Sin melocotón." },
];

function Frame({ id, kind, wide, children }) {
  return (
    <div class={`u3-frame u3-${kind}${wide && kind === "desk" ? " is-wide" : ""}`} data-shot={`${id}-${kind === "desk" ? "desktop" : "movil"}`}>
      {children}
    </div>
  );
}

export function U3Lab() {
  return (
    <div class="u3">
      <header class="u3-head">
        <h1>mensaje del usuario · <em>seis tratamientos</em></h1>
        <p>
          Ronda de descarte. La misma conversación entera en las seis, en escritorio (680) y móvil (390),
          con los casos que discriminan: corto de una línea, largo con saltos propios, steer, adjuntos y
          tarea venida del padre. Prosa y ledger son las piezas reales de producción; sólo el mensaje es nuevo.
          Maqueta estática: <code>UserWaypoint</code> de producción no se toca.
        </p>
      </header>
      {DIRECTIONS.map((d) => (
        <section class="u3-dir" id={`u3-${d.id}`} key={d.id}>
          <header class="u3-dir-head">
            <h2>{d.label}</h2>
            <p>{d.note}</p>
          </header>
          <div class="u3-strip">
            <Frame id={d.id} kind="desk" wide={d.wide}>{d.render()}</Frame>
            <Frame id={d.id} kind="phone">{d.render()}</Frame>
          </div>
        </section>
      ))}
    </div>
  );
}
