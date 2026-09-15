import { useState } from "preact/hooks";
import { Rewind as RewindIcon, Copy as CopyIcon, Check as CheckIcon, GitBranch as ForkIcon } from "lucide-preact";
import { AssistantDocument, Prose } from "../components/AssistantDocument/AssistantDocument.jsx";
import { ActivityLedger } from "../components/ActivityLedger/ActivityLedger.jsx";
import { WaypointAttachments } from "../components/UserWaypoint/WaypointAttachments.jsx";
import { copyToClipboard } from "../data/util/format.js";
import "../components/UserWaypoint/UserWaypoint.css";
import "./u6-lab.css";

// u6-lab — CATALOG ONLY, and the round that was CHOSEN: treatment 2, Racimo,
// is what production now ships (components/AssistantDocument/TurnFoot and the
// cell in components/UserWaypoint). This file is kept as the reference the
// implementation was moved from, and as the place the three treatments can
// still be compared; u3, u4 and u5 were the scaffolding that led here and are
// deleted.
//
// The rail of u5 was rejected: reserving 44px on the right of EVERY turn made
// copy cost the owner width in his own prose, which is the width he had just
// asked to get back. Its replacement is a FOOT: one horizontal line at the very
// end of the turn, carrying the hour and the copy, with room to grow later (he
// named fork).
//
// Three consequences, all of them deliberate here:
//
//   · zero width stolen — no rail, no gutter, no reserved lane anywhere on
//     the turn. The prose runs the full measure, and the foot is a line in
//     flow underneath it, not a column beside it.
//   · minimum height — a line, not a bar. No background, no border box, no
//     padding block. What it costs per turn is measured, not eyeballed.
//   · once per turn — anchored to the end of the LAST message, what the
//     owner calls the final response. Not one per paragraph.
//
// The cell below imports production's `UserWaypoint.css`, so the slab, the
// gutter and the clock are the shipped rules; only this file's own `u6-`
// classes are lab-local.

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
const LEDGER_4 = [
  { id: "e1", tool: "grep", arg: { text: '"fast" — pkg/serve/frontend/src/layout/' }, out: "6 hits", status: "ok" },
  { id: "e2", tool: "bash", arg: { text: "node scripts/check-fields.mjs" }, out: "435 files", status: "ok" },
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

const SNIPPET = `.zl-strip.is-overflow .zl-chip--fast {
  order: 2;
  margin-inline-start: auto;
}`;

function CodeBlock() {
  return (
    <Prose><div class="code-block">
      <div class="code-block-header">
        <span class="code-block-lang">css</span>
        <button type="button" class="code-block-copy">copy</button>
      </div>
      <pre><code>{SNIPPET}</code></pre>
    </div></Prose>
  );
}

// ── THE UNIT ─────────────────────────────────────────────────────────────
// "solo el último de sus mensajes, lo que sería la response final, no todo".
//
// A turn is prose, tool rows, more prose, a code block, more prose. The rule
// implemented here: the foot copies the LAST RUN OF WORDS of the turn — the
// prose (and any code inside it) that comes after the last tool row. That is
// the answer; everything before it is the work of getting there, and the work
// already has its own rows with their own detail.
//
// Each turn therefore carries `blocks` (what is drawn) and `final` (what the
// clipboard gets), and `final` is always a suffix of the turn, never all of
// it. A turn that ends in a tool row has no closing prose, so its last run of
// words is the prose BEFORE that row: the rule stays defined everywhere
// rather than leaving some turns without a foot.

const TURN_A = {
  at: "23:53",
  blocks: [
    <p>I'll hang it on the strip, same word as on the phone.</p>,
    <ActivityLedger rows={LEDGER_1} />,
    <p>Done. The word sits after the permission chip, same colour as on mobile.</p>,
  ],
  final: "Done. The word sits after the permission chip, same colour as on mobile.",
  finalNote: "the closing paragraph — the narration before the rows is not the answer",
};

// Two of my turns back to back: the session continued on its own after the
// first one closed. This is the pair that makes the repetition judgeable —
// two feet within a screen of each other, nothing between them.
const TURN_A2 = {
  at: "23:55",
  blocks: [
    <p>One more thing I noticed while I was in there: the chip had no test at the overflow width, so I added one.</p>,
    <ActivityLedger rows={LEDGER_4} />,
    <p>Green. That covers 1100px and below, which was the only gap.</p>,
  ],
  final: "Green. That covers 1100px and below, which was the only gap.",
};

const TURN_LONG = {
  at: "09:21",
  blocks: [
    <p>The strip wraps at 1100px, so the word cannot simply be appended: past that width the children reflow into the overflow group and <code>fast</code> would land between the model and the permission chip.</p>,
    <ActivityLedger rows={LEDGER_2} />,
    <p>What I did instead was give it an explicit order and push it to the end of its own line, which keeps it adjacent to <code>yolo</code> at every width:</p>,
    <CodeBlock />,
    <p>On the phone it goes in the capsule next to the model, same word and same colour. Three fidelity scenes moved, all of them on the phone, and all three because the capsule grew by exactly one word.</p>,
  ],
  // Everything after the last tool row: two paragraphs AND the fence between
  // them. The fence's own `copy` still copies only the code — two units, two
  // scopes, and the difference is legible because they look nothing alike.
  final: [
    "What I did instead was give it an explicit order and push it to the end of its own line, which keeps it adjacent to `yolo` at every width:",
    "```css\n" + SNIPPET + "\n```",
    "On the phone it goes in the capsule next to the model, same word and same colour. Three fidelity scenes moved, all of them on the phone, and all three because the capsule grew by exactly one word.",
  ].join("\n\n"),
  long: true,
  finalNote: "everything after the last tool row: both paragraphs and the fence between them",
};

// Ends in a tool row. There is no closing prose, so the last run of words is
// the paragraph above the rows — the foot still exists, in the same place,
// with the same two controls.
const TURN_ENDS_LEDGER = {
  at: "09:40",
  blocks: [
    <p>Rerunning only the unit suite. Three scenes moved, all on the phone: the capsule grew by one word.</p>,
    <ActivityLedger rows={LEDGER_3} />,
  ],
  final: "Rerunning only the unit suite. Three scenes moved, all on the phone: the capsule grew by one word.",
  finalNote: "ends in a tool row — copies the prose above it, the last thing actually said",
};

// Ends in a fence. The closing run of words IS the fence, so that is what the
// foot copies; the fence's header keeps its own `copy` for the code alone.
const TURN_ENDS_CODE = {
  at: "10:04",
  blocks: [
    <p>The hero uses the Q2 figure. The report says 14.2%, the page says 12.8%. One line fixes it:</p>,
    <CodeBlock />,
  ],
  final: "The hero uses the Q2 figure. The report says 14.2%, the page says 12.8%. One line fixes it:\n\n```css\n" + SNIPPET + "\n```",
  finalNote: "ends in a fence — the fence is part of the answer, so it comes along",
};

// In progress. A turn that has not ended has no final response and no hour to
// stamp on it, so it has NO FOOT. The foot appearing is how the turn reads as
// finished — and nothing shifts when it does, because it lands under the last
// line rather than inside it.
const TURN_LIVE = {
  blocks: [
    <p>Reading the reconnect path first to see how the snapshot and the subscription are sequenced.</p>,
    <ActivityLedger rows={LEDGER_LIVE} />,
  ],
  streaming: true,
};

const CONVERSATION = [
  { user: { kind: "short", lines: ["Put fast on the desktop status strip, after yolo."], at: "23:52", day: "14 Sept" } },
  { moa: TURN_A },
  { moa: TURN_A2 },
  { user: { kind: "long", lines: LONG, at: "09:14" } },
  { moa: TURN_LONG },
  { user: { kind: "steer", lines: ["Skip the flaky e2e and rerun only the unit suite"], at: "09:38" } },
  { moa: TURN_ENDS_LEDGER },
  { user: { kind: "attachments", lines: ["Mira esto y dime qué falla respecto al informe"], at: "10:02", attachments: ATTACHMENTS } },
  { moa: TURN_ENDS_CODE },
  { user: { kind: "parent", lines: ["Audit pkg/serve/ws.go for the resume race and report the exact line."], at: "11:08" } },
  { moa: TURN_LIVE },
];

function Text({ lines }) {
  return (
    <div class="u6-text">
      {lines.map((l, i) => <p key={i}>{l}</p>)}
    </div>
  );
}

function Skirt({ attachments }) {
  if (!attachments) return null;
  return <WaypointAttachments attachments={attachments} sessionId="demo" onOpenImage={() => {}} />;
}

function Clock({ msg }) {
  return (
    <span class="u6-clock">
      {msg.day && <span class="u6-day">{msg.day}</span>}
      <time class="u6-hhmm zl-data">{msg.at}</time>
    </span>
  );
}

function CopyAction({ text }) {
  const [done, setDone] = useState(false);
  return (
    <button
      type="button"
      class={`u6-act${done ? " is-done" : ""}`}
      aria-label="Copy the final response"
      title={done ? "Copied" : "Copy the final response"}
      onClick={() => copyToClipboard(text).then((ok) => { if (ok) { setDone(true); setTimeout(() => setDone(false), 1400); } })}
    >
      {done ? <CheckIcon aria-hidden="true" /> : <CopyIcon aria-hidden="true" />}
    </button>
  );
}

// The growth slot. The owner named fork as a THING HE MIGHT WANT, not a thing
// he asked for, so it is drawn as what it is: a placeholder, dashed, inert,
// labelled. It exists in these frames to prove the line has room for a third
// and a fourth control without changing shape — delete it and the foot is
// unchanged.
function GrowthSlot() {
  return (
    <span class="u6-slot" title="example only — no action decided">
      <span class="u6-slot-btn" aria-hidden="true"><ForkIcon /></span>
      <span class="u6-slot-tag">ejemplo</span>
    </span>
  );
}

function RewindAction() {
  return (
    <button type="button" class="u6-act" aria-label="Rewind the conversation to this message" title="Rewind here">
      <RewindIcon aria-hidden="true" />
    </button>
  );
}

// ── the cell — settled, not touched ──────────────────────────────────────
// The clock in the gutter (44px on the phone), mono tabular-nums, the short date
// riding above the time, the rewind on the slab's right edge, and nothing
// else. Copy left this cell in u5 and does not come back.
function Cell({ msg }) {
  const cls = `u6-cell${msg.kind === "steer" ? " is-steer" : ""}${msg.kind === "parent" ? " is-parent" : ""}`;
  return (
    <div class={cls}>
      <div class="u6-gutter"><Clock msg={msg} /></div>
      <div class="u6-body">
        <Text lines={msg.lines} />
        <Skirt attachments={msg.attachments} />
        {msg.kind === "parent" && <span class="u6-src">from parent session</span>}
        <span class="u6-rail"><RewindAction /></span>
      </div>
    </div>
  );
}

// ── the foot ─────────────────────────────────────────────────────────────
// One line, three treatments, identical content and identical box: what
// differs is only where the two groups sit and when the line has ink.
function Foot({ t, growth }) {
  return (
    <div class="u6-foot">
      <time class="u6-foot-time zl-data">{t.at}</time>
      <span class="u6-foot-acts">
        <CopyAction text={t.final} />
        {growth && <GrowthSlot />}
      </span>
    </div>
  );
}

function Turn({ t, variant, growth }) {
  return (
    <AssistantDocument streaming={t.streaming} className={`u6-turn u6-f-${variant}`}>
      {t.blocks}
      {/* no foot while the turn is still running: there is no final response
          yet, and no hour to stamp on it. */}
      {!t.streaming && <Foot t={t} growth={growth} />}
    </AssistantDocument>
  );
}

function Flow({ variant, growth = false }) {
  return (
    <div class={`u6-col u6-v-${variant}`}>
      {CONVERSATION.map((t, i) =>
        t.user ? <Cell key={i} msg={t.user} /> : <Turn key={i} t={t.moa} variant={variant} growth={growth} />,
      )}
    </div>
  );
}

const VARIANTS = [
  {
    id: "1",
    variant: "balance",
    label: "1 · Balanza",
    note: "La hora a la izquierda, las acciones a la derecha, y la medida entera entre las dos. La línea siempre está, muy tenue, y sube a contraste pleno con puntero o foco. Las dos anclas caen en los bordes del texto, así que el ojo que baja leyendo encuentra la hora exactamente donde acaba el margen izquierdo de la prosa — la misma vertical en todos mis turnos — y el copiar siempre en la derecha, nunca viajando. Coste: en 390px los dos grupos quedan lejísimos y la línea se lee como una barra vacía; es la que peor aguanta el teléfono.",
  },
  {
    id: "2",
    variant: "cluster",
    label: "2 · Racimo",
    note: "Todo junto a la izquierda: hora, y pegados a ella el copiar y lo que venga después. Una sola mancha pequeña bajo la última línea, del ancho de lo que contiene y no de la medida. Es la que menos ruido repetido produce al hacer scroll —cientos de turnos y cientos de manchitas idénticas en la misma vertical— y la única que se comporta igual en 680 y en 390, porque su ancho no depende del ancho del texto. Coste: hora y acciones comparten vecindad, así que un dedo gordo tiene la hora al lado del copiar (la hora no es pulsable, no hay error posible, pero sí ruido visual).",
  },
  {
    id: "3",
    variant: "reveal",
    label: "3 · Revelado",
    note: "El pie sólo se enciende al pasar o enfocar el turno; en reposo la línea está ahí pero vacía, con su altura ya reservada, así que al aparecer no mueve un píxel de texto. Layout de balanza. El transcript en reposo queda absolutamente limpio: cero repetición visual al hacer scroll, que es justo el criterio que el dueño puso. Coste: un control que no se ve no existe para quien no lo busca, y en el teléfono no hay hover — ahí degrada al pie siempre presente y tenue, es decir, a la variante 1. Dos comportamientos para una misma cosa.",
  },
];

function Frame({ id, kind, children }) {
  return (
    <div class={`u6-frame u6-${kind}`} data-shot={`${id}-${kind === "desk" ? "desktop" : "movil"}`}>
      {children}
    </div>
  );
}

export function U6Lab() {
  return (
    <div class="u6">
      <header class="u6-head">
        <h1>pie de turno · <em>una línea, al final</em></h1>
        <p>
          El riel de u5 queda descartado: reservaba 44px a la derecha de <em>cada</em> turno, así que el copiar
          le costaba ancho a la prosa — el mismo ancho que se acababa de recuperar. En su lugar, un pie: una
          sola línea horizontal al final del turno, con la hora y el copiar, y sitio para crecer. Cero ancho
          robado (la prosa vuelve a la medida completa, medido abajo), coste vertical mínimo, y una sola vez
          por turno, anclado al final de la respuesta final. <code>UserWaypoint</code> de producción no se toca.
        </p>
      </header>

      {VARIANTS.map((v) => (
        <section class="u6-dir" id={`u6-${v.id}`} key={v.id}>
          <header class="u6-dir-head">
            <h2>{v.label}</h2>
            <p>{v.note}</p>
          </header>
          <div class="u6-strip">
            <Frame id={v.id} kind="desk"><Flow variant={v.variant} /></Frame>
            <Frame id={v.id} kind="phone"><Flow variant={v.variant} /></Frame>
          </div>
        </section>
      ))}

      <section class="u6-dir" id="u6-crecer">
        <header class="u6-dir-head">
          <h2>Sitio para crecer — el patrón, no la decisión</h2>
          <p>
            El dueño mencionó un fork. No está decidido, así que aquí se dibuja como lo que es: una casilla de
            ejemplo, punteada, inerte y etiquetada <code>ejemplo</code>. Lo que estas dos vistas prueban es que
            la línea admite un tercer y un cuarto control sin cambiar de forma ni de altura, en escritorio y en
            390px. Quítala y el pie queda exactamente igual. Tratamiento 2 (Racimo).
          </p>
        </header>
        <div class="u6-strip">
          <div class="u6-frame u6-desk" data-shot="crecer-desktop"><Flow variant="cluster" growth /></div>
          <div class="u6-frame u6-phone" data-shot="crecer-movil"><Flow variant="cluster" growth /></div>
        </div>
      </section>

      <section class="u6-dir" id="u6-unidad">
        <header class="u6-dir-head">
          <h2>Qué copia exactamente</h2>
          <p>
            <strong>La última tirada de palabras del turno</strong>: la prosa (y el bloque de código que vaya
            dentro) que viene después de la última fila de herramienta. Eso es la respuesta; lo de antes es el
            trabajo de llegar a ella, y el trabajo ya tiene sus filas con su propio detalle. Si el turno acaba
            en fila de herramienta no hay prosa de cierre, así que copia la prosa que está encima — lo último
            que de verdad dije. El <code>copy</code> de la cabecera de un bloque de código se queda y copia
            sólo el código: dos unidades distintas, y se ven distintas.
          </p>
        </header>
        <div class="u6-cases">
          {[TURN_A, TURN_LONG, TURN_ENDS_LEDGER, TURN_ENDS_CODE].map((t, i) => (
            <div class="u6-case" key={i}>
              <span class="u6-case-h">{t.finalNote}</span>
              <pre class="u6-case-t">{t.final}</pre>
            </div>
          ))}
        </div>
      </section>
    </div>
  );
}
