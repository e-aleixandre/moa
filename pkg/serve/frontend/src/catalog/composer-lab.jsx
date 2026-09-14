import { Mic, Square, Loader2 } from "lucide-preact";
import "../layout/Composer/Composer.css";
import "../layout/LiveBar/LiveBar.css";
import "./composer-lab.css";

// composer-lab — how Stop, Send and Dictate should share the composer.
//
// A MOCKUP for a decision, not the implementation: the slab, the live bar and
// every control are static markup wearing the production class names
// (Composer.css, LiveBar.css), so form, size, colour and position are the
// real ones, while nothing is wired (no store, no voice machine, no cancel).
// The three alternatives live here as lab classes (`.cl-alt-*`) and a little
// extra markup, never as branches in the shipped Composer.
//
// The state that matters is the last column: the desktop, agent working AND
// recording, where today two red squares sit side by side and mean opposite
// things (stop the AGENT / stop MY microphone). The other columns are there so
// each alternative can be judged in repose too.

function AttachIcon() {
  return (
    <svg viewBox="0 0 16 16" width={18} height={18} aria-hidden="true">
      <path d="M8 3.5v9M3.5 8h9" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" />
    </svg>
  );
}

function SendIcon() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true">
      <path d="M8 13V3.5M8 3.5L3.8 7.7M8 3.5l4.2 4.2" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" />
    </svg>
  );
}

function Chev() {
  return (
    <svg class="zl-live-chev is-up" viewBox="0 0 12 12" aria-hidden="true">
      <path d="M2.5 4.5L6 8l3.5-3.5" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" />
    </svg>
  );
}

// The four columns. `busy` = the parent run is working; `recording` = the mic
// is live (desktop: the mic button; phone: the send button, locked).
export const STATES = [
  { id: "idle", label: "Reposo", busy: false, text: false, recording: false },
  { id: "typing", label: "Escribiendo", busy: false, text: true, recording: false },
  { id: "busy", label: "Agente trabajando", busy: true, text: false, recording: false },
  { id: "busy-rec", label: "Trabajando + grabando", busy: true, text: false, recording: true },
];

export const VARIANTS = [
  {
    id: "actual",
    label: "Actual",
    note: "Referencia. Stop (■ borde rojo) entra en el composer cuando el agente trabaja; el mic grabando es ■ relleno rojo pulsando. En escritorio, trabajando + grabando, son dos cuadrados rojos seguidos con acciones opuestas.",
  },
  {
    id: "a",
    label: "A · Stop sale del composer",
    note: "Parar al agente vive en la LiveBar, la fila que ya dice que trabaja (dos toques, rojo sólo al armar). El composer sólo tiene mi entrada: +, texto, mic, enviar. El mic grabando es un mic con anillo, no un cuadrado ni rojo.",
  },
  {
    id: "b",
    label: "B · Stop como palabra",
    note: "Stop se queda en el composer pero escrito (“Stop” / “sure?”), neutro, y separado del par mic/enviar: a la izquierda, junto al +. El mic grabando deja de ser cuadrado.",
  },
  {
    id: "c",
    label: "C · Sólo forma y color",
    note: "Nada se mueve. El mic grabando deja de ser un cuadrado rojo (mic con anillo pulsante); Stop sigue siendo ■ rojo. Lo mínimo.",
  },
];

// The live bar the three densities put above the composer while the agent
// works. `stop` adds alternative A's control at the end of the row.
function Live({ stop, phone }) {
  return (
    <div class="zl-live">
      <div class="zl-live-bar">
        <div class="zl-live-now" role="status">
          <span class="zl-live-dot is-working" aria-hidden="true" />
          <span class="zl-live-txt">Running the test suite after the resume() fix…</span>
          <span class="zl-live-el zl-data">1m 12s</span>
        </div>
        <button type="button" class="zl-live-tally" aria-label="2 in the background">
          <span class="zl-live-dots" aria-hidden="true">
            <span class="zl-live-id is-agent" style="--zl-live-accent: var(--sky)" />
            <span class="zl-live-id is-bash zl-data">$</span>
          </span>
          <span class="zl-live-n zl-data">2</span>
          <Chev />
        </button>
        {stop && (
          <button type="button" class={`cl-live-stop${phone ? " is-phone" : ""}`} aria-label="Stop the run">
            <Square size={11} fill="currentColor" aria-hidden="true" />
            <span>Stop</span>
          </button>
        )}
      </div>
    </div>
  );
}

// The composer's Stop as it ships: a red-rimmed square.
function StopGhost() {
  return (
    <button type="button" class="composer-stop-ghost" aria-label="Stop the run (Esc)">
      <Square size={13} />
    </button>
  );
}

// Alternative B: the same control as a word, neutral until armed.
function StopWord() {
  return (
    <button type="button" class="cl-stop-word" aria-label="Stop the run">
      <Square size={11} fill="currentColor" aria-hidden="true" />
      <span>Stop</span>
    </button>
  );
}

// The desktop's dictation button. `square` is today's recording face; the
// alternatives replace it with a mic inside a pulsing ring (`.cl-rec`).
function MicButton({ recording, square }) {
  const cls = `zl-attach zl-mic${recording ? (square ? " recording" : " cl-rec") : ""}`;
  return (
    <button type="button" class={cls} aria-label={recording ? "Stop recording" : "Dictate"}>
      {recording && square ? <Square size={13} /> : <Mic size={15} />}
    </button>
  );
}

function Composer({ variant, state, phone }) {
  const { busy, text, recording } = state;
  const armed = text;
  const square = variant === "actual";
  // Who holds Stop inside the slab: the ghost (actual, C), the word (B), or
  // nobody (A — it moved to the live bar).
  const stopInSlab = busy && (variant === "actual" || variant === "c");
  const stopWord = busy && variant === "b";
  const placeholder = busy
    ? (phone ? "Steer — it keeps working…" : "Steer the agent — ⏎ sends while it works, it won't stop it…")
    : "Message moa";

  // Phone: voice owns the single button at the end of the pill. Recording
  // there is the locked (hands-free) face, which today is a red square.
  let send;
  if (phone) {
    let cls = "zl-send gesture";
    let icon = <SendIcon />;
    if (recording) {
      cls += square ? " recording locked" : " cl-rec locked";
      icon = square ? <Square size={14} /> : <Mic size={16} />;
    } else if (!armed) {
      cls += " mic-mode";
      icon = <Mic size={16} />;
    }
    send = (
      <div class="zl-send-wrap">
        <button type="button" class={cls} aria-label={recording ? "Stop recording" : armed ? "Send" : "Record"}>{icon}</button>
      </div>
    );
  } else {
    send = (
      <div class="zl-send-wrap">
        <button type="button" class="zl-send" aria-label="Send" disabled={!armed}><SendIcon /></button>
      </div>
    );
  }

  return (
    <div class={`zl-composer${busy ? " is-busy" : ""}${armed ? " is-armed" : ""}`}>
      <button type="button" class="zl-attach" aria-label="Attach"><AttachIcon /></button>
      {stopWord && <StopWord />}
      <textarea
        rows={1}
        class="zl-ta"
        aria-label="Message moa"
        placeholder={placeholder}
        value={text ? "Skip the flaky e2e and rerun only the unit suite" : ""}
        readOnly
      />
      {stopInSlab && <StopGhost />}
      {!phone && <MicButton recording={recording} square={square} />}
      {send}
    </div>
  );
}

function Dock({ variant, state, phone }) {
  return (
    <div class={`cl-frame ${phone ? "cl-phone" : "cl-desk"}`}>
      <div class="cl-state-label">{state.label}</div>
      <div class="zl-dock">
        {state.busy && <Live stop={variant === "a"} phone={phone} />}
        <Composer variant={variant} state={state} phone={phone} />
      </div>
    </div>
  );
}

function VariantRow({ variant }) {
  return (
    <section class="cl-variant" id={`composer-${variant.id}`} data-variant={variant.id}>
      <header class="cl-variant-head">
        <h2>{variant.label}</h2>
        <p>{variant.note}</p>
      </header>
      <div class={`cl-strip cl-alt-${variant.id}`} data-density="desktop">
        {STATES.map((s) => <Dock key={s.id} variant={variant.id} state={s} phone={false} />)}
      </div>
      <div class={`cl-strip cl-alt-${variant.id}`} data-density="mobile">
        {STATES.map((s) => <Dock key={s.id} variant={variant.id} state={s} phone />)}
      </div>
    </section>
  );
}

export function ComposerLab() {
  return (
    <div class="cl">
      <header class="cl-head">
        <h1>composer · <em>parar, enviar, dictar</em></h1>
        <p>
          Cuatro filas (actual + tres alternativas), cuatro estados, dos densidades. La columna que
          decide es la última: escritorio, agente trabajando y grabando. Maqueta estática con las
          clases reales del Composer y la LiveBar; nada está cableado.
        </p>
      </header>
      {VARIANTS.map((v) => <VariantRow key={v.id} variant={v} />)}
    </div>
  );
}
