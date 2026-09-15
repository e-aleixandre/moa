import {
  ArrowLeft, MoreVertical, MousePointerClick, X, Smartphone, Tablet, Monitor, Scan,
  CornerDownLeft, Plus, Globe, History, RotateCw,
} from "lucide-preact";
import { Segmented } from "../components/Segmented/Segmented.jsx";
import { Field, StateDot } from "../primitives/index.js";
import "../components/LivePreview/LivePreview.css";
import "./lp-lab.css";

// lp-lab — CATALOG ONLY. Three directions for the Live Preview EMPTY STATE: the
// screen that shows before there is anything to preview. Nothing here is
// imported by production; the toolbar below is a static replica of
// `.live-preview-bar` (same classes, same stylesheet, same Segmented) because
// what is judged is the WHOLE panel, not a cropped form.
//
// What the screen is today, measured: a title, an input glued to a peach
// button, a hint that repeats the title in other words, "Write to Moa" hanging
// at the bottom with nothing to point at, and the four widths plus Inspect lit
// over an empty stage.
//
// Peach is deliberately absent from all three. It came free with the user-cell
// redesign and stays free; the one dominant action here is mauve, which is
// already what "selected" means in this very toolbar.

const WIDTHS = [
  { value: "390", label: "390", icon: Smartphone, size: 14, ariaLabel: "Phone · 390px" },
  { value: "768", label: "768", icon: Tablet, size: 17, ariaLabel: "Tablet · 768px" },
  { value: "1280", label: "1280", icon: Monitor, size: 18, ariaLabel: "Desktop · 1280px" },
  { value: "fit", label: "Fit", icon: Scan, size: 16, ariaLabel: "Fit to pane" },
];

function renderWidth(opt) {
  const Icon = opt.icon;
  return (
    <>
      <Icon size={opt.size} aria-hidden="true" />
      <span class="live-preview-width-label" aria-hidden="true">{opt.label}</span>
    </>
  );
}

// The real toolbar. `dim` is each direction's answer to the question the owner
// asked: what do controls that cannot do anything look like? They keep their
// place — the bar must not re-flow the instant an app loads — and stop
// inviting the press. `width` is which preset reads as selected.
function PreviewBar({ dim, width = "fit" }) {
  return (
    <div class={`live-preview-bar${dim ? " lp-bar-dim" : ""}`}>
      <div class="live-preview-menu">
        <button type="button" class="live-preview-action" aria-label="Preview options">
          <MoreVertical size={15} />
        </button>
      </div>
      <button type="button" class="live-preview-action" disabled aria-label="Back in preview">
        <ArrowLeft size={16} aria-hidden="true" />
      </button>
      <Segmented
        className="live-preview-widths"
        options={WIDTHS}
        value={dim ? null : width}
        onChange={() => {}}
        disabled={dim}
        renderOption={renderWidth}
        aria-label="Viewport width"
      />
      <div class="live-preview-bar-end">
        <button type="button" class="live-preview-action" disabled={dim} aria-label="Inspect">
          <MousePointerClick size={16} />
          <span class="live-preview-action-label">Inspect</span>
        </button>
        <button type="button" class="live-preview-action" aria-label="Close preview">
          <X size={16} />
        </button>
      </div>
    </div>
  );
}

// The single dominant action of the screen. Not `variant-accent`: that one is
// the peach gradient.
function Go({ children, wide }) {
  return <button type="button" class={`lp-go${wide ? " is-wide" : ""}`}>{children}</button>;
}

function Panel({ id, phone, bar, barSlot, children }) {
  return (
    <div class={`lp-frame${phone ? " is-phone" : ""}`} data-t={id}>
      <div class="live-preview-inline lp-panel">
        {bar}
        {barSlot}
        <div class="live-preview-stage lp-stage">{children}</div>
      </div>
    </div>
  );
}

/* ── A · Tarjeta ────────────────────────────────────────────────────────────
   Premise broken: that this is a form. It is a CARD on moa's own surface, in
   the vocabulary the user cell just settled — slab, gutter, quiet actions. One
   sentence that instructs, one that says what you get, field and button with a
   gutter between them, and the common ports as things you can PRESS instead of
   a placeholder you cannot. Second visit is a different card: the last address
   is a row, typing drops below it.
   The bar goes quiet: with no app, no preset can do anything. */

const PORTS = [
  { port: "5173", what: "Vite" },
  { port: "3000", what: "Next" },
  { port: "8080", what: "" },
];

function DirA({ phone, again }) {
  return (
    <div class="lpa">
      <div class="lpa-card">
        {again ? (
          <>
            <h2 class="lpa-title">Open your app</h2>
            <button type="button" class="lpa-last">
              <StateDot state="running" />
              <span class="lpa-last-url">http://localhost:5173</span>
              <span class="lpa-last-when">12m ago</span>
              <span class="lpa-last-go" aria-hidden="true"><RotateCw size={14} /></span>
            </button>
            <div class="lpa-or"><span>or</span></div>
            <div class="lpa-row">
              <Field
                variant="box" size="lg" mono class="lpa-field"
                type="url" placeholder="http://localhost:3000" aria-label="Preview URL"
              />
              <Go>Load</Go>
            </div>
          </>
        ) : (
          <>
            <h2 class="lpa-title">Paste the address of your running app</h2>
            <p class="lpa-lede">
              It opens here, beside the conversation, and reloads itself while Moa edits.
            </p>
            <div class="lpa-row">
              <Field
                variant="box" size="lg" mono class="lpa-field"
                type="url" placeholder="http://localhost:5173" aria-label="Preview URL"
              />
              <Go>Load</Go>
            </div>
            <div class="lpa-ports">
              {PORTS.map((p) => (
                <button type="button" class="lpa-port" key={p.port}>
                  <span class="lpa-port-n">:{p.port}</span>
                  {p.what && <span class="lpa-port-w">{p.what}</span>}
                </button>
              ))}
            </div>
          </>
        )}
      </div>
    </div>
  );
}

/* ── B · Escenario ──────────────────────────────────────────────────────────
   Premise broken: that the empty state is a screen of its own. It is the STAGE,
   already at the width it will preview at, with the address where an address
   belongs — docked to the chrome of the frame that will hold the app. Nothing
   floats in the middle of a void, and the framing is visible before anything
   loads. The presets stay LIVE: they resize the frame in front of you, which is
   the only thing on this screen that can be tried before there is an app.
   Inspect does not: it needs a document, so it alone goes quiet. */

function DirBDock() {
  return (
    <div class="lpb-dock">
      <Globe size={15} class="lpb-dock-icon" aria-hidden="true" />
      <Field
        variant="box" size="md" mono class="lpb-dock-field"
        type="url" placeholder="localhost:5173" aria-label="Preview URL"
      />
      <span class="lpb-dock-enter"><CornerDownLeft size={13} aria-hidden="true" /> Enter</span>
    </div>
  );
}

function DirB({ phone }) {
  return (
    <div class="lpb">
      <div class={`lpb-ghost${phone ? " is-phone" : ""}`}>
        <span class="lpb-ghost-w">{phone ? "390 × 780" : "1280 × 800"}</span>
        <p class="lpb-ghost-say">Your app opens in this frame</p>
        <p class="lpb-ghost-sub">Type the address your dev server printed and press Enter.</p>
      </div>
    </div>
  );
}

/* ── C · Candidatos ─────────────────────────────────────────────────────────
   Premise broken: that typing is the act. Moa runs on the machine the dev
   server runs on, so it can offer what is already listening; typing becomes the
   last row, not the entrance. The shape is a list — what moa uses for every
   other "pick one of these".
   HYPOTHESIS, not fact: enumerating local listeners (or deriving them from the
   session's folder) is backend work that has to be validated before it is
   built. The mock shows where it would lead, and says so on the screen.
   The bar goes quiet, same as A. */

const FOUND = [
  { url: "localhost:5173", what: "vite · this folder", live: true },
  { url: "localhost:8080", what: "caddy", live: true },
];

function DirC({ phone }) {
  return (
    <div class="lpc">
      <div class="lpc-card">
        <h2 class="lpc-title">Choose what to preview</h2>
        <span class="lpc-group">Listening on this machine</span>
        {FOUND.map((f) => (
          <button type="button" class="lpc-row" key={f.url}>
            <StateDot state="running" />
            <span class="lpc-url">{f.url}</span>
            <span class="lpc-what">{f.what}</span>
            <span class="lpc-go">Open</span>
          </button>
        ))}
        <span class="lpc-group">Used here before</span>
        <button type="button" class="lpc-row is-quiet">
          <History size={14} aria-hidden="true" />
          <span class="lpc-url">localhost:3000</span>
          <span class="lpc-what">2d ago</span>
          <span class="lpc-go">Open</span>
        </button>
        <button type="button" class="lpc-row is-quiet">
          <Plus size={14} aria-hidden="true" />
          <span class="lpc-url is-plain">Another address…</span>
        </button>
      </div>
      <p class="lpc-foot">Not listed? Start the dev server and it appears here.</p>
    </div>
  );
}

const DIRECTIONS = [
  {
    id: "a",
    title: "A · Tarjeta",
    premise: "no es un formulario flotando: es una losa con el vocabulario de la celda de usuario",
    render: (phone) => (
      <Panel id="a" phone={phone} bar={<PreviewBar dim />}>
        <DirA phone={phone} />
      </Panel>
    ),
  },
  {
    id: "a2",
    title: "A · ya usado",
    premise: "la misma losa con historial: abrir lo de siempre es un toque, teclear sigue debajo",
    render: (phone) => (
      <Panel id="a2" phone={phone} bar={<PreviewBar dim />}>
        <DirA phone={phone} again />
      </Panel>
    ),
  },
  {
    id: "b",
    title: "B · Escenario",
    premise: "no es una pantalla aparte: es el escenario, ya a su tamaño, con la dirección en el cromo",
    render: (phone) => (
      <Panel id="b" phone={phone} bar={<PreviewBar width={phone ? "390" : "1280"} />} barSlot={<DirBDock />}>
        <DirB phone={phone} />
      </Panel>
    ),
  },
  {
    id: "c",
    title: "C · Candidatos",
    premise: "teclear no es el acto: moa ofrece lo que ya está escuchando (hipótesis de backend)",
    render: (phone) => (
      <Panel id="c" phone={phone} bar={<PreviewBar dim />}>
        <DirC phone={phone} />
      </Panel>
    ),
  },
];

export function LPLab() {
  const params = typeof location !== "undefined" ? new URLSearchParams(location.search) : new URLSearchParams();
  const phone = params.get("dens") === "phone";
  const only = params.get("t");
  const list = only ? DIRECTIONS.filter((d) => d.id === only) : DIRECTIONS;
  const bare = !!only;
  return (
    <div class={`lp-lab${phone ? " is-phone" : ""}${bare ? " is-bare" : ""}`}>
      {!bare && (
        <header class="lp-lab-head">
          <h1>live preview · <em>el estado inicial</em></h1>
          <p>
            Tres direcciones para la pantalla que se ve <em>antes</em> de que haya app. Cada una responde
            también qué hace la barra de presets cuando no hay nada que previsualizar. Sin melocotón: el
            color sigue libre.
          </p>
        </header>
      )}
      {list.map((d) => (
        <figure class={`lp-fig${phone ? " is-phone" : ""}`} key={d.id}>
          {!bare && (
            <figcaption class="lp-cap">
              <span class="lp-cap-title">{d.title}</span>
              <span class="lp-cap-premise">{d.premise}</span>
            </figcaption>
          )}
          {d.render(phone)}
        </figure>
      ))}
    </div>
  );
}
