import { ChevronLeft } from "lucide-preact";
import { StateWord, CopyAction } from "../layout/WorkChrome/WorkChrome.jsx";
import { UserWaypoint } from "../components/UserWaypoint/UserWaypoint.jsx";
import { AssistantDocument, Prose } from "../components/AssistantDocument/AssistantDocument.jsx";
import { ActivityLedger } from "../components/ActivityLedger/ActivityLedger.jsx";
import "../layout/WorkChrome/WorkChrome.css";
import "../layout/SubagentView/SubagentView.css";
import "./sub-lab.css";

/* sub-lab — CATALOG ONLY. The FINISHED subagent, three directions.
 *
 * Nothing here is imported by production. SubagentView, WorkChrome and
 * Composer are untouched; this sheet only adds the `.sb-*` classes the three
 * arrangements need. Everything else on screen is the SHIPPED component —
 * UserWaypoint for the parent's message, ActivityLedger for the tool call,
 * AssistantDocument/Prose for the answer, StateWord and CopyAction from
 * WorkChrome — so the shots show the real skin rearranged, not a repaint.
 *
 * WHAT IS BEING FIXED. Today a finished run renders SubagentReport
 * (SubagentView.jsx:236-320): a headline, the result, then "Work log" and
 * "Run details" inside `Disclosure`, both CLOSED (WorkChrome.jsx:139). The
 * record of what the subagent actually did — the parent's instruction, the
 * tools it ran, what it replied — is two taps away, and it is the thing you
 * opened the screen to read. The owner's words: "esa forma de ver la
 * conversación como con desplegables no me gusta".
 *
 * WHAT IS NOT BEING REMOVED. The figures. "Es verdad que viene bien ese
 * informe": model, mode, duration, tokens and cost stay reachable in all
 * three. What moves is WHERE they live, and that is the axis the three
 * directions differ on:
 *
 *   A  report is a FOOT, pinned, always on screen. Conversation plain.
 *   B  report is a RIBBON under the head, scrolls away. Conversation plain.
 *   C  report is a TABLE that closes the page. Conversation split into
 *      "the result" and "how it went".
 *
 * THE HEAD IS ONE ROW in all three, because that is already decided. It is
 * drawn here rather than mounted from WorkHead (which is two rows today) so
 * the three arrangements are judged against the head they will actually get.
 * Stop is absent for the same reason it is absent in production's terminal
 * state — there is nothing left to stop — and the decided move of Stop to the
 * bottom bar only concerns a RUNNING errand.
 *
 * The other two decided changes are drawn as they will be: the parent's
 * message is an ordinary user message carrying a discreet mark (tone="parent"
 * + label, which is `.zl-user.is-parent` — already in UserWaypoint.css:109),
 * and there is no steer composer on a finished run, so the mic decision does
 * not reach this screen.
 */

/* ── fixtures ─────────────────────────────────────────────────────────────
   One finished errand, and one that failed, frozen. Every direction draws
   the same two, so the comparison is of arrangement and nothing else. */

const OK = {
  errand: "Audit the attachment store for blob leaks",
  parent: "Check whether a crash between the index write and the transcript save can leave a blob owned by nobody. Read the store, not the tests.",
  ask: "audit pkg/session/attachments for orphaned blobs after a crash",
  answer: [
    "Yes, there is a window. attachments.go:214 writes the index entry and returns; the transcript that names the blob is saved by the caller afterwards (session.go:588). A crash between the two leaves the blob on disk with a refcount of 1 and no transcript referencing it, so the sweeper never collects it.",
    "The concurrent-delete race you asked about last week is NOT this: that one is fixed by the lock at attachments.go:301. This is an ordering problem, not a locking one.",
  ],
  rows: [{
    id: "r1",
    tool: "bash",
    arg: "rg -n 'writeIndex|saveTranscript' pkg/session",
    out: "9 hits",
    status: "ok",
  }],
  state: { tone: "neutral", word: "Completed" },
  details: [
    ["Model", "terra · high"],
    ["Mode", "sync"],
    ["Duration", "2m 14s"],
    ["Tokens", "↑18.4k ↓3.1k"],
    ["Cost", "$0.21"],
    ["Actions", "1"],
  ],
  marks: [
    { k: "terra · high", mono: false },
    { k: "sync", mono: false },
    { k: "2m 14s", mono: true },
    { k: "↑18.4k ↓3.1k", mono: true },
    { k: "$0.21", mono: true },
  ],
};

const FAIL = {
  errand: "Run the race sweep on pkg/serve",
  parent: "Run the full sweep with -race and report only the failures, with the goroutine that wrote and the one that read.",
  ask: "go test -race ./pkg/serve/...",
  error: `WARNING: DATA RACE
Write at 0x00c0004a2118 by goroutine 214:
  moa/pkg/serve.(*hub).attach()
      /src/pkg/serve/hub.go:88 +0x94

Previous read at 0x00c0004a2118 by goroutine 97:
  moa/pkg/serve.(*hub).broadcast()
      /src/pkg/serve/hub.go:143 +0x1f0

FAIL	moa/pkg/serve	18.442s
exit status 1`,
  rows: [{
    id: "f1",
    tool: "bash",
    arg: "go test -race ./pkg/serve/...",
    out: "exit 1",
    status: "err",
  }],
  state: { tone: "failed", word: "Failed" },
  details: [
    ["Model", "sol · high"],
    ["Mode", "background"],
    ["Duration", "47s"],
    ["Tokens", "↑9.2k ↓0.4k"],
    ["Cost", "$0.06"],
    ["Actions", "1"],
  ],
  marks: [
    { k: "sol · high", mono: false },
    { k: "background", mono: false },
    { k: "47s", mono: true },
    { k: "↑9.2k ↓0.4k", mono: true },
    { k: "$0.06", mono: true },
  ],
};

/* ── the pieces every direction shares ────────────────────────────────── */

/* The head, ONE row — the decided shape. Back, the errand, the state. The
   errand is the title and the thing that must survive 390px, so the state
   word is `flex: none` and the title is what ellipses. On a phone every
   target is 44px. */
function Head({ fx, phone }) {
  return (
    <header class={`wk-head sb-head${phone ? " is-phone" : ""}`}>
      <div class="wk-head-top">
        <button type="button" class={`wk-home${phone ? " is-phone" : ""}`} aria-label="Back to release 0.38">
          <ChevronLeft size={16} aria-hidden="true" />
        </button>
        <h2 class="sb-title">{fx.errand}</h2>
        <StateWord tone={fx.state.tone} word={fx.state.word} />
      </div>
    </header>
  );
}

/* The parent's instruction, as DECIDED: an ordinary user message with a
   discreet mark naming where it came from. `tone="parent"` is production's
   own class (`.zl-user.is-parent`), and the label is the mark. */
function ParentMessage({ fx }) {
  return (
    <UserWaypoint time="14:02" label="From the parent agent" tone="parent" accent="mauve">
      {fx.parent}
    </UserWaypoint>
  );
}

function ToolCall({ fx }) {
  return (
    <div class="sb-tools">
      <ActivityLedger rows={fx.rows} />
    </div>
  );
}

/* The answer, as prose, in the shipped turn. */
function Answer({ fx }) {
  if (fx.error) {
    return (
      <AssistantDocument>
        <Prose>
          <p>The sweep failed. The race is in the hub, verbatim:</p>
        </Prose>
        <pre class="sa-error">{fx.error}</pre>
      </AssistantDocument>
    );
  }
  return (
    <AssistantDocument>
      <Prose>{fx.answer.map((p, i) => <p key={i}>{p}</p>)}</Prose>
    </AssistantDocument>
  );
}

/* The whole record, unfolded. This is the rule none of the three may break:
   you read it without opening anything. */
function Conversation({ fx, phone, withAnswer = true }) {
  return (
    <div class={`zl-transcript sb-conv${phone ? " is-dense" : ""}`}>
      <ParentMessage fx={fx} />
      <ToolCall fx={fx} />
      {withAnswer && <Answer fx={fx} />}
    </div>
  );
}

function CopyIt({ fx, phone }) {
  return fx.error
    ? <CopyAction phone={phone} text={fx.error} label="Copy error" />
    : <CopyAction phone={phone} text={fx.answer.join("\n\n")} label="Copy result" />;
}

/* The audit as PRINTED ROWS, never folded. WorkChrome's own `.wk-rd` table,
   lifted out of the disclosure it sits in today. */
function DetailTable({ fx }) {
  return (
    <dl class="wk-rd sb-table">
      {fx.details.map(([k, v]) => (
        <div key={k}><dt>{k}</dt><dd>{v}</dd></div>
      ))}
    </dl>
  );
}

/* ══════════════════════════════════════════════════════════════════════════
   DIRECTION A — "Pie de datos, siempre a la vista"
   ────────────────────────────────────────────────────────────────────────
   Shape   head (one row), then the conversation, plain and whole, scrolling
           under a FIXED foot. The foot is the report, compacted to the five
           figures that matter, plus Copy result. Nothing else is on the page:
           there is no headline, no result panel, no fold. The screen is the
           conversation, and the run's numbers are furniture at its bottom
           edge — the same place the parent conversation keeps its own status.
   Report  pinned, always legible, never scrolled away. Job ID and parent
           session are not on it; tapping the foot would open them.
   Gains   the record reads on arrival, at full width, with zero ceremony.
           The figures are answerable at any scroll position, which is the one
           thing the current disclosure cannot do even when opened.
   Costs   the foot spends ~72px of a 780px phone permanently, on data you
           consult once. And the ANSWER is the last thing in the scroller, so
           on a long run you scroll to reach the conclusion — the same
           complaint the old bottom-pinned result banner earned.
   ══════════════════════════════════════════════════════════════════════════ */
function DirA({ fx, phone }) {
  return (
    <div class="sb-screen sb-a">
      <Head fx={fx} phone={phone} />
      <div class="sb-body">
        <Conversation fx={fx} phone={phone} />
      </div>
      <div class={`sb-foot${phone ? " is-phone" : ""}`}>
        <div class="sb-marks">
          {fx.marks.map((m) => (
            <span class={`sb-mark${m.mono ? " zl-data" : ""}`} key={m.k}>{m.k}</span>
          ))}
        </div>
        <div class="sb-foot-acts"><CopyIt fx={fx} phone={phone} /></div>
      </div>
    </div>
  );
}

/* ══════════════════════════════════════════════════════════════════════════
   DIRECTION B — "Cinta de datos arriba, conversación normal debajo"
   ────────────────────────────────────────────────────────────────────────
   Shape   head (one row), then a single dense BAND carrying the whole report
           on one line — model, mode, duration, tokens, cost — with Copy result
           at its end. Under it, the conversation, plain and whole. The band
           belongs to the head's block: it is what this run WAS, said once, at
           the top, where provenance goes.
   Report  scrolls away with the content. It is read on arrival and then stops
           costing anything, which is the honest shape for a figure you check
           once.
   Gains   the cheapest of the three: one 40px band and the screen is
           otherwise an ordinary conversation, identical to the parent's. No
           new vocabulary to learn. On the phone it recovers the foot's 72px.
   Costs   on 390px five figures do not fit on one line, so the band scrolls
           HORIZONTALLY — cost and tokens sit off the right edge until you
           push them, which is a real discoverability loss and the thing to
           judge in the phone shot. And like A, the answer is at the end of
           the scroller.
   ══════════════════════════════════════════════════════════════════════════ */
function DirB({ fx, phone }) {
  return (
    <div class="sb-screen sb-b">
      <Head fx={fx} phone={phone} />
      <div class={`sb-ribbon${phone ? " is-phone" : ""}`}>
        <div class="sb-ribbon-data">
          {fx.marks.map((m, i) => (
            <>
              {i > 0 && <span class="sb-ribbon-sep" aria-hidden="true">·</span>}
              <span class={`sb-mark${m.mono ? " zl-data" : ""}`} key={m.k}>{m.k}</span>
            </>
          ))}
        </div>
        <div class="sb-ribbon-act"><CopyIt fx={fx} phone={phone} /></div>
      </div>
      <div class="sb-body">
        <Conversation fx={fx} phone={phone} />
      </div>
    </div>
  );
}

/* ══════════════════════════════════════════════════════════════════════════
   DIRECTION C — "El resultado, y cómo se hizo"
   ────────────────────────────────────────────────────────────────────────
   Shape   head (one row), then THE RESULT — what the parent actually
           received — as the first thing on the page, with Copy beside it.
           Then a labelled hairline, "How it went", and under it the record:
           the parent's instruction and the tools, unfolded. The audit table
           closes the page, printed flat, no disclosure.
   Report  at the END, as rows, because audit is what you read last and never
           twice. Nothing is pinned and nothing is hidden.
   Gains   it answers the two different questions in the order they are asked
           — "what did it conclude" then "how do I know" — without folding
           anything. The result gets the top of the screen, so on the phone
           the conclusion is legible before a single swipe. It is also the
           only one of the three where the answer is not buried at the bottom
           of a long record.
   Costs   the most new furniture: a result panel and a section label that the
           other two do not need, and a page with three distinct bands rather
           than one conversation. The final assistant turn is PROMOTED into
           the result panel and therefore does not appear again in the record
           below — the record is the parent's instruction and the tools only.
           That avoids printing the same paragraph twice, but it does mean the
           "conversation" here is not literally the transcript: it is the
           transcript minus its last turn. The owner should decide whether
           that edit is acceptable or whether he wants the record whole.
   ══════════════════════════════════════════════════════════════════════════ */
function DirC({ fx, phone }) {
  return (
    <div class="sb-screen sb-c">
      <Head fx={fx} phone={phone} />
      <div class="sb-body">
        <div class={`sb-col${phone ? " is-phone" : ""}`}>
          <section class={`sb-result${fx.error ? " is-failed" : ""}`}>
            <div class="sb-result-body">
              {fx.error
                ? <pre class="sa-error">{fx.error}</pre>
                : fx.answer.map((p, i) => <p key={i}>{p}</p>)}
            </div>
            <div class="sb-result-acts"><CopyIt fx={fx} phone={phone} /></div>
          </section>

          <div class="sb-sec">
            <span class="sb-sec-t">How it went</span>
            <span class="sb-sec-rule" aria-hidden="true" />
          </div>

          <Conversation fx={fx} phone={phone} withAnswer={false} />

          <div class="sb-sec">
            <span class="sb-sec-t">Run details</span>
            <span class="sb-sec-rule" aria-hidden="true" />
          </div>
          <DetailTable fx={fx} />
        </div>
      </div>
    </div>
  );
}

/* ── hosts ──────────────────────────────────────────────────────────────── */

const DIRS = {
  a: { id: "a", title: "A · Pie de datos, siempre a la vista", Screen: DirA },
  b: { id: "b", title: "B · Cinta de datos arriba, conversación debajo", Screen: DirB },
  c: { id: "c", title: "C · El resultado, y cómo se hizo", Screen: DirC },
};

const CASES = { ok: { label: "Completado", fx: OK }, fail: { label: "Falló", fx: FAIL } };

export function SubScreen({ dir = "a", kase = "ok", density = "phone" }) {
  const D = DIRS[dir] || DIRS.a;
  const fx = (CASES[kase] || CASES.ok).fx;
  const Screen = D.Screen;
  return (
    <div class={`sb-host is-${density}`}>
      <Screen fx={fx} phone={density === "phone"} />
    </div>
  );
}

/* ── the contact sheet ──────────────────────────────────────────────────── */

const NOTES = {
  a: {
    gains: [
      "La conversación se lee entera, a ancho completo, nada más entrar.",
      "Duración, tokens y coste son legibles en cualquier posición del scroll.",
      "Ninguna pieza nueva: es la conversación normal más una barra de estado.",
    ],
    costs: [
      "El pie cuesta ~72px permanentes de 780 en el móvil.",
      "La respuesta es lo último del scroller: en un run largo hay que bajar hasta el final para leer la conclusión.",
      "Job ID y sesión padre no caben en el pie; harían falta detrás de un toque.",
    ],
    where: "Pie fijo",
  },
  b: {
    gains: [
      "La más barata: una banda de 40px y el resto es una conversación idéntica a la del padre.",
      "El informe se lee al llegar y luego deja de ocupar sitio.",
      "En el móvil recupera los 72px que gasta el pie de A.",
    ],
    costs: [
      "En 390px las cinco cifras no caben: la banda hace scroll horizontal y coste y tokens quedan fuera de pantalla.",
      "La respuesta también queda al final del scroller.",
      "Una banda que se va con el scroll no responde “¿cuánto costó?” a mitad de lectura.",
    ],
    where: "Cinta bajo la cabecera",
  },
  c: {
    gains: [
      "Responde en el orden en que se pregunta: qué concluyó, y luego cómo lo sabe.",
      "La única en la que la conclusión se lee sin deslizar, también en el móvil.",
      "El informe cierra la página como tabla plana: sin plegar, sin fijar.",
    ],
    costs: [
      "Es la que más mobiliario nuevo introduce: panel de resultado y dos rótulos de sección.",
      "El último turno sube al panel de resultado, así que el registro de abajo es el transcript MENOS su último turno.",
      "Tres bandas distintas en vez de una conversación: más que leer antes de empezar a leer.",
    ],
    where: "Tabla al pie de la página",
  },
};

function Cell({ dir, kase, density }) {
  return (
    <figure class="sb-cell">
      <div class={`sb-frame is-${density}`} data-shot={`${dir}-${kase}-${density}`}>
        <SubScreen dir={dir} kase={kase} density={density} />
      </div>
      <figcaption>{CASES[kase].label} · {density === "phone" ? "390×780" : "escritorio"}</figcaption>
    </figure>
  );
}

export function SubLab() {
  const params = typeof location !== "undefined" ? new URLSearchParams(location.search) : new URLSearchParams();
  const one = params.get("dir");
  if (one) {
    return (
      <div class="sb-solo">
        <SubScreen dir={one} kase={params.get("k") || "ok"} density={params.get("d") || "phone"} />
      </div>
    );
  }
  return (
    <div class="sb-lab">
      <header class="sb-lab-head">
        <h1>El subagente <em>terminado</em></h1>
        <p>
          Hoy el registro de lo que hizo el subagente vive dentro de dos <strong>desplegables
          cerrados</strong>: “Work log” y “Run details”. Justo lo que se va a ver está a dos
          toques. Las tres direcciones abren la conversación del todo y se diferencian en
          <strong> dónde vive el informe</strong> y en cuánta ceremonia se le pone alrededor.
          La cabecera es de <strong>una fila</strong> en las tres, porque ya está decidido.
        </p>
      </header>

      <section class="sb-sheet">
        <h2>Hoja de contacto</h2>
        <table class="sb-compare">
          <thead>
            <tr>
              <th />
              {Object.values(DIRS).map((d) => <th key={d.id}>{d.title}</th>)}
            </tr>
          </thead>
          <tbody>
            <tr>
              <th scope="row">Dónde vive el informe</th>
              {Object.values(DIRS).map((d) => <td key={d.id}>{NOTES[d.id].where}</td>)}
            </tr>
            <tr>
              <th scope="row">Qué gana</th>
              {Object.values(DIRS).map((d) => (
                <td key={d.id}><ul>{NOTES[d.id].gains.map((g) => <li key={g}>{g}</li>)}</ul></td>
              ))}
            </tr>
            <tr>
              <th scope="row">Qué cuesta</th>
              {Object.values(DIRS).map((d) => (
                <td key={d.id}><ul>{NOTES[d.id].costs.map((c) => <li key={c}>{c}</li>)}</ul></td>
              ))}
            </tr>
          </tbody>
        </table>
      </section>

      {Object.values(DIRS).map((d) => (
        <section class="sb-dir" key={d.id}>
          <header class="sb-dir-head">
            <h2>{d.title}</h2>
          </header>
          <div class="sb-strip">
            <Cell dir={d.id} kase="ok" density="phone" />
            <Cell dir={d.id} kase="fail" density="phone" />
          </div>
          <div class="sb-strip">
            <Cell dir={d.id} kase="ok" density="desk" />
          </div>
        </section>
      ))}
    </div>
  );
}
