import { useState } from "preact/hooks";
import { UserWaypoint } from "../components/UserWaypoint/UserWaypoint.jsx";
import { AssistantDocument, Prose } from "../components/AssistantDocument/AssistantDocument.jsx";
import { TurnFoot } from "../components/AssistantDocument/TurnFoot.jsx";
import { ActivityLedger } from "../components/ActivityLedger/ActivityLedger.jsx";
import "./u7-lab.css";

// u7-lab — CATALOG ONLY. The user's message, rebuilt.
//
// The complaint is not about the bubble: it is that the two things you read
// one after the other follow DIFFERENT GRAMMARS in the same column.
//
//   · the assistant: full measure, and its hour lives BELOW the words, on a
//     foot line, beside the copy.
//   · the user (shipped): a narrower slab with a 44px lane on its right for
//     rewind, and its hour OUTSIDE the slab, in a side gutter to the left.
//
// Two placement systems for two things that are read as one exchange. The eye
// registers it even when it cannot name it.
//
// Two families are laid out here, both asked for:
//
//   A · SAME GRAMMAR — the user's message behaves like the assistant's: full
//       measure, hour below on a foot. What tells them apart moves to another
//       channel (glyph, tone, type, depth).
//   B · STILL DIFFERENT — the message keeps a shape of its own, but the hour
//       leaves the side gutter and lands where the assistant's hour is: on a
//       foot line under the words.
//
// Everything below mounts the SHIPPED components — UserWaypoint,
// AssistantDocument, ActivityLedger, TurnFoot — untouched. Each variant is a
// wrapper class in this file's own CSS. Nothing in production changes, which
// is also why the goldens do not move.

const SHOT =
  "iVBORw0KGgoAAAANSUhEUgAAACAAAAAgCAYAAABzenr0AAACRElEQVR4AbXBUUEYQAxAsby2OlCCFCTsb1ZmYeJQASou6ePj82dlZWVlZWVlZWVlZWVlZWVlZWVlZWVlZWVlZWVlZWXlTAghhBBCCCGEEEIIIYQQQgghhBByJoQQQgghhBBCCCGEEEIIIYQQQgg5E0IIIYQQQgghhBBCCCGEEEIIIYScCSGEEEIIIYQQQgghhBBCCCGEEELOhBBCCCGEEEIIIYQQQgghhBBCCCFnQgghhBBCCCGEEEIIIYQQQgghhJAzIYQQQgghhBBCCCGEEEIIIYQQQsiZEEIIIYQQQgghhBBCCCGEEEIIIeRMCCGEEEIIIYQQQgghhBBCCCGEkDMhhBBCCCGEEEIIIYQQQgghhBBCzoQQQgghhBBCCCGEEEIIIYQQQggh5EwIIYQQQgghhBBCCCGEEEIIIYQQciaEEEIIIYQQQgghhBBCCCGEEEIIORNCCCGEEEIIIYQQQgghhBBCCCGEnAkhhBBCCCGEEEIIIYQQQgghhBBCzoQQQgghhBBCCCGEEEIIIYQQQggh5EwIIYQQQgghhBBCCCGEEEIIIYQQciaEEEIIIYQQQgghhBBCCCGEEEIIORNCCCGEEEIIIYQQQgghhBBCCCGEnAkhhBBCvv7898r3v78IIWdCCCFPTQghZ0IIIU9NCCFnQgghT00IIWdCCCFPTQghZ0IIIU9NCCFnQgghT00IIWdCCCFPTQghZ0IIIU9NCCFPTQghv6MTCQIFDyYKAAAAAElFTkSuQmCC";

const ATTACHMENTS = [
  { type: "image", data: SHOT, attachment_size: 862208, mime_type: "image/png", filename: "hero-desktop.png" },
  { type: "document", attachment_id: "att-informe", attachment_size: 1468006, mime_type: "application/pdf", filename: "informe-q3.pdf" },
];

const LEDGER_1 = [
  { id: "a1", tool: "read", arg: { text: "pkg/serve/frontend/src/components/UserWaypoint/UserWaypoint.css" }, out: "188 lines", status: "ok" },
  { id: "a2", tool: "edit", arg: { text: "pkg/serve/frontend/src/components/UserWaypoint/UserWaypoint.css" }, out: "+6 −3", status: "ok" },
];
const LEDGER_2 = [
  { id: "b1", tool: "grep", arg: { text: '"zl-user-gutter" — pkg/serve/frontend/src/' }, out: "9 hits", status: "ok" },
  { id: "b2", tool: "bash", arg: { text: "node scripts/fidelity.mjs" }, out: "35 scenes", status: "ok" },
];
const LEDGER_3 = [
  { id: "c1", tool: "bash", arg: { text: "bun test src/components/UserWaypoint" }, out: "18 pass", status: "ok" },
];
const LEDGER_LIVE = [
  { id: "d1", tool: "read", arg: { text: "pkg/serve/frontend/src/layout/Stream/ConversationStream.jsx" }, out: "412 lines", status: "ok" },
  { id: "d2", tool: "grep", arg: { text: '"clockHHMM(" — src/' }, live: true, startedAt: Date.now() - 4000 },
];

// ── the exchange ─────────────────────────────────────────────────────────
// The defect is a CONTRAST between two things read one after the other, so a
// variant shown alone proves nothing: every frame runs user → assistant →
// tool rows → user, with short messages and long ones.

const TURN_1 = {
  at: "09:14",
  blocks: [
    <Prose><p>The hour is in a side gutter because the cell was indented against it. I can move it under the words instead.</p></Prose>,
    <ActivityLedger rows={LEDGER_1} />,
    <Prose><p>Done — the gutter is gone and the hour rides a foot line, the same one the copy is on.</p></Prose>,
  ],
  final: "Done — the gutter is gone and the hour rides a foot line, the same one the copy is on.",
};

const TURN_2 = {
  at: "09:22",
  blocks: [
    <Prose><p>Three things share that 64px gutter today: the hour, the indent of the slab, and the vertical the rewind measures itself from. Removing it touches all three, so I checked each one before moving anything.</p></Prose>,
    <ActivityLedger rows={LEDGER_2} />,
    <Prose>
      <p>Nine call sites, all of them inside the waypoint itself — nothing outside the component reads the gutter, so the column is free to change shape.</p>
      <p>The measure grows by the full 64px on the desktop and by 44px on the phone, which is where it is most worth having.</p>
    </Prose>,
  ],
  final: "Nine call sites, all of them inside the waypoint itself — nothing outside the component reads the gutter, so the column is free to change shape.",
};

const TURN_3 = {
  at: "09:41",
  blocks: [
    <Prose><p>Only the unit suite, then. Eighteen green, nothing moved in fidelity.</p></Prose>,
    <ActivityLedger rows={LEDGER_3} />,
  ],
  final: "Only the unit suite, then. Eighteen green, nothing moved in fidelity.",
};

const TURN_LIVE = {
  blocks: [
    <Prose><p>Reading the stream first, to see where the hour is handed to the waypoint.</p></Prose>,
    <ActivityLedger rows={LEDGER_LIVE} />,
  ],
  streaming: true,
};

const LONG_LINES = [
  "Dos cosas antes de que toques nada:",
  "1. La hora tiene que quedar donde la del asistente. Si sigue viviendo en un canalón lateral, da igual lo bonita que quede la celda: se van a seguir leyendo como dos cosas distintas.",
  "2. El rewind se queda pegado a SU mensaje. Estuvo a 311px una vez y fue un defecto.",
  "Y acuérdate de que el melocotón es sólo mío: si lo quitas de aquí, no puede aparecer en ningún otro sitio.",
];

const EXCHANGE = [
  { user: { key: "u1", text: "Mueve la hora del mensaje del usuario debajo, como la del asistente.", at: "09:13" } },
  { moa: TURN_1 },
  { user: { key: "u2", lines: LONG_LINES, at: "09:18" } },
  { moa: TURN_2 },
  { user: { key: "u3", text: "Salta el e2e flaky y corre sólo los unitarios", at: "09:40", label: "You — steer" } },
  { moa: TURN_3 },
  { user: { key: "u4", text: "Mira esto y dime qué falla respecto al informe", at: "10:02", attachments: ATTACHMENTS } },
  { user: { key: "u5", text: "Audit the resume race in pkg/serve/ws.go and report the exact line.", at: "11:08", label: "From the parent agent", tone: "parent", accent: "sky" } },
  { moa: TURN_LIVE },
];

function UserMessage({ msg }) {
  return (
    <UserWaypoint
      time={msg.at}
      label={msg.label}
      tone={msg.tone}
      accent={msg.accent}
      attachments={msg.attachments}
      sessionId="demo"
      onRewind={() => {}}
      rewindPreview={msg.text || (msg.lines || []).join("\n")}
    >
      {msg.lines
        ? msg.lines.map((l, i) => <p key={i}>{l}</p>)
        : <p>{msg.text}</p>}
    </UserWaypoint>
  );
}

function Turn({ t }) {
  return (
    <AssistantDocument streaming={t.streaming}>
      {t.blocks}
      {!t.streaming && <TurnFoot time={t.at} text={t.final} />}
    </AssistantDocument>
  );
}

function Exchange() {
  return (
    <>
      {EXCHANGE.map((row, i) =>
        row.user ? <UserMessage key={row.user.key} msg={row.user} /> : <Turn key={`t${i}`} t={row.moa} />,
      )}
    </>
  );
}

// ── the variants ─────────────────────────────────────────────────────────
// Each one is a channel, not a nudge: if two of them differ only in where the
// clock sits by a few pixels, one of them has no reason to exist.

const VARIANTS = [
  {
    id: "espejo",
    family: "A",
    label: "A1 · Espejo",
    claim: "Idéntico al asistente. Lo distingue un glifo.",
    note: "El mensaje ocupa la medida entera, sin superficie, sin caja, exactamente como un turno del asistente, y la hora cae debajo en el mismo pie, junto al rewind. Lo único que dice quién habla es un chevrón melocotón colgado en el margen, fuera de la medida: no se lee, se ve. Es la variante que más gramática comparte y la que menos ruido añade al scroll. Coste: si el chevrón se te escapa, dos voces seguidas pueden confundirse en un vistazo rápido.",
  },
  {
    id: "bano",
    family: "A",
    label: "A2 · Baño",
    claim: "Misma forma, distinto tono.",
    note: "Medida entera y pie debajo, igual que el asistente, pero el bloque entero va bañado en melocotón al 7% con un filo del mismo color arriba. La diferencia es de TONO, no de anchura ni de colocación: tus mensajes son las franjas cálidas del transcript y el resto es papel. Aguanta igual un mensaje de una línea que uno de cinco párrafos. Coste: es la que más color mete en una pantalla larga; con muchos mensajes seguidos el transcript se raya.",
  },
  {
    id: "titular",
    family: "A",
    label: "A3 · Titular",
    claim: "Tu mensaje es el titular del tramo.",
    note: "Sin superficie y a medida entera, pero el texto sube de escala y de peso: lo que tú pides encabeza el tramo y lo que responde el agente es el cuerpo. La hora y el rewind viven en una regla melocotón finísima que cierra el titular por debajo — mismo sitio que el pie del asistente, distinto material. Coste: un mensaje tuyo largo pesa mucho a tamaño de titular, y hay mensajes tuyos que son de todo menos titulares.",
  },
  {
    id: "hendido",
    family: "A",
    label: "A4 · Hendido",
    claim: "Misma anchura; el tuyo está hundido en la página.",
    note: "Medida entera y pie debajo, pero el bloque está HUNDIDO: superficie más oscura que el lienzo con una sombra interior arriba, mientras que el asistente flota sin caja. La distinción es de profundidad, un canal que no gasta ni color ni anchura ni tipografía. El melocotón se reduce a un punto en el pie. Coste: sobre fondos oscuros la profundidad se lee peor que el tono; en el móvil con brillo bajo puede desaparecer.",
  },
  {
    id: "losa",
    family: "B",
    label: "B1 · Losa",
    claim: "La celda de hoy, con la hora bajada al pie.",
    note: "El cambio mínimo honesto: se mantiene la losa elevada que ya existe, pero muere el canalón lateral y la hora baja al pie, con el rewind a su lado. La celda recupera los 64px de escritorio (44 en móvil) y el rewind deja de necesitar un carril de 44px a su derecha. Sirve de referencia: es lo mismo que hay hoy con la gramática de la hora corregida y nada más. Coste: sigue habiendo dos anchuras en la columna, que es justo lo que chirría.",
  },
  {
    id: "capsula",
    family: "B",
    label: "B2 · Cápsula",
    claim: "Ceñida al texto, no a la columna.",
    note: "La celda deja de ser una banda y se ciñe a su contenido: un mensaje de cinco palabras mide cinco palabras, con un tope del 85% de la medida. Muy redondeada y alineada a la izquierda. El pie queda debajo, empezando en el mismo borde que el texto. Lo que gana: se ve de un golpe cuánto dijiste, y los mensajes cortos dejan de ocupar una banda entera. Coste: el borde derecho baila de un mensaje a otro; la columna deja de tener un canto limpio.",
  },
  {
    id: "burbuja",
    family: "B",
    label: "B3 · Burbuja",
    claim: "A la derecha, como en una app de mensajes.",
    note: "La gramática más conocida del mundo: tú a la derecha, el agente a la izquierda a medida entera. Ceñida al contenido, tope del 85%, y el pie debajo alineado a la derecha, con hora y rewind. Ninguna duda posible sobre quién habla, cero esfuerzo de aprendizaje. Coste: importa una convención de chat a una herramienta que no es un chat, desperdicia el lado derecho en escritorio y rompe el canto izquierdo que ordena hoy toda la columna.",
  },
  {
    id: "filo",
    family: "B",
    label: "B4 · Filo",
    claim: "Sin caja: un filo melocotón y sangría.",
    note: "Ninguna superficie: sólo un filo melocotón de 3px a la izquierda y una sangría, como una cita. El texto respira dentro de la medida y el pie va debajo, alineado con el texto y no con el filo. Es la variante más ligera de las ocho — cero superficies nuevas en el transcript — y la única que marca al hablante con una línea vertical continua, que es lo que hace un margen de libro. Coste: el filo vertical y la regla del ledger conviven a poca distancia y ambas son líneas finas.",
  },
];

const FAMILIES = {
  A: {
    title: "Familia A · misma gramática que el asistente",
    lead: "Medida entera y hora debajo, en un pie. Lo que distingue a quien habla se muda a otro canal: un glifo, un tono, la tipografía o la profundidad.",
  },
  B: {
    title: "Familia B · siguen distinguiéndose, misma hora",
    lead: "El mensaje conserva una forma propia, pero la hora abandona el canalón lateral y aparece donde está la del asistente: en un pie, bajo las palabras, con el rewind a su lado.",
  },
};

function Frame({ variant, kind }) {
  return (
    <div class={`u7-frame u7-${kind}`} data-shot={`${variant.id}-${kind}`}>
      <div class={`u7-col u7-v-${variant.id}`}>
        <Exchange />
      </div>
    </div>
  );
}

export function U7Lab() {
  const [current, setCurrent] = useState(VARIANTS[0].id);
  // On the phone the desktop frame is 680px of sideways scrolling before the
  // first pixel of the design, so the narrow window opens on the phone frame.
  const [dens, setDens] = useState(
    typeof window !== "undefined" && window.innerWidth < 900 ? "phone" : "both",
  );
  const v = VARIANTS.find((x) => x.id === current) || VARIANTS[0];

  return (
    <div class="u7">
      {/* The selector is sticky and thumb-sized on purpose: the owner judges
          this on the phone, and reloading between eight variants is not
          judging, it is bookkeeping. */}
      <div class="u7-bar">
        <div class="u7-chips" role="tablist" aria-label="Variantes">
          {VARIANTS.map((x) => (
            <button
              key={x.id}
              type="button"
              role="tab"
              aria-selected={x.id === current}
              class={`u7-chip${x.id === current ? " is-on" : ""}`}
              onClick={() => setCurrent(x.id)}
            >
              {x.label}
            </button>
          ))}
        </div>
        <div class="u7-dens">
          {[["phone", "Móvil"], ["desk", "Escritorio"], ["both", "Ambos"]].map(([k, l]) => (
            <button key={k} type="button" class={`u7-chip is-dens${dens === k ? " is-on" : ""}`} onClick={() => setDens(k)}>
              {l}
            </button>
          ))}
        </div>
      </div>

      {/* The frames come FIRST and the prose after them: the owner opens this
          on a phone to LOOK, and three paragraphs before the first frame is
          how the previous round of this lab wasted his time. */}
      <section class="u7-current">
        <div class={`u7-strip is-${dens}`}>
          {dens !== "desk" && <Frame variant={v} kind="phone" />}
          {dens !== "phone" && <Frame variant={v} kind="desk" />}
        </div>
        <header class="u7-dir-head">
          <span class="u7-fam">{FAMILIES[v.family].title}</span>
          <h2>{v.label} — <em>{v.claim}</em></h2>
          <p class="u7-fam-lead">{FAMILIES[v.family].lead}</p>
          <p>{v.note}</p>
        </header>
      </section>

      <header class="u7-head">
        <h1>el mensaje del usuario · <em>ocho gramáticas</em></h1>
        <p>
          El defecto no es la burbuja: es que en la misma columna conviven dos sistemas de colocación. El
          asistente ocupa la medida entera y lleva la hora <strong>debajo</strong>, en un pie junto al copiar;
          el mensaje del usuario es más estrecho, reserva 44px a su derecha para el rewind y lleva la hora
          <strong> fuera</strong>, en un canalón lateral. Se leen seguidos y no se colocan igual.
        </p>
        <p>
          Abajo, las dos vías: <strong>A</strong>, que el tuyo se comporte como el del asistente; <strong>B</strong>,
          que sigan distinguiéndose pero con la hora en el mismo sitio. Todo monta los componentes de producción
          (<code>UserWaypoint</code>, <code>AssistantDocument</code>, <code>ActivityLedger</code>,{" "}
          <code>TurnFoot</code>) sin tocarlos: cada variante es una clase envoltorio de esta maqueta.
        </p>
      </header>

      {/* Every variant, one after the other, for the desktop read-through and
          for the screenshots. The selector above is for the phone. */}
      <section class="u7-all">
        <h2 class="u7-all-h">Las ocho seguidas</h2>
        {VARIANTS.map((x) => (
          <div class="u7-one" key={x.id} id={`u7-${x.id}`}>
            <header class="u7-dir-head">
              <span class="u7-fam">{FAMILIES[x.family].title}</span>
              <h2>{x.label} — <em>{x.claim}</em></h2>
              <p>{x.note}</p>
            </header>
            <div class="u7-strip is-both">
              <Frame variant={x} kind="phone" />
              <Frame variant={x} kind="desk" />
            </div>
          </div>
        ))}
      </section>
    </div>
  );
}
