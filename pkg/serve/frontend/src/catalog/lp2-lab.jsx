import {
  ArrowLeft, MoreVertical, MousePointerClick, X, Smartphone, Tablet, Monitor, Scan,
  RotateCw, ChevronDown, PencilLine,
} from "lucide-preact";
import { Field, StateDot } from "../primitives/index.js";
import "./lp2-lab.css";

// lp2-lab — CATALOG ONLY. The Live Preview PANEL, chrome included. Round 1 of
// lp-lab redesigned the middle and left the toolbar as production draws it;
// this file is the toolbar, the panel edge, and the loaded state.
//
// The card in the stage is direction A from that round, already approved, and
// is reproduced here rather than imported so the two rounds can be compared
// side by side without one mutating the other.
//
// Measured problems with today's bar, all visible in /tmp/lp-a-desktop.png:
//
//   · 62px tall on an 860px panel — 7% of the tool spent on its own chrome,
//     over content that belongs to someone else's app.
//   · four width presets, each an icon AND a number inside its own sunken
//     cell: the loudest object in the bar is the least used one.
//   · Back sits apart from ⋮ although both are navigation of the same frame.
//   · Inspect (a mode over the content) and Close (a panel-level action) are
//     the same shape, the same size, side by side, as if they ranked equal.
//   · a border inside a border: the panel is padded, and the stage draws its
//     own rounded rim inside that padding.
//
// Peach appears nowhere. Mauve is the accent; blue/green/yellow/red stay state.

/* ═══════════════════════════════════════════════════════════════════════════
   THE CHROME — proposal 1 · Riel
   One hairline rail, 40px, three zones that mean three different things:

     left    WHAT IS LOADED. The address is the title of the panel, and it is
             the menu: pressing it is where "change URL", "reload" and "copy"
             live, because they are all things you do to that address. Back
             joins it as an icon, since navigating the frame is navigating
             what that line names.
     right   HOW IT IS SHOWN. The four widths lose their sunken box and their
             numbers: bare icons, the active one in mauve with its number.
             Inspect is a mode over the content and stays in this group.
     far     THE PANEL ITSELF. Close, alone, past a real gap and a divider, so
             the control that destroys the panel is never mistaken for one
             that changes it.
   ═══════════════════════════════════════════════════════════════════════════ */

const WIDTHS = [
  { id: "390", n: "390", Icon: Smartphone, size: 14, label: "Phone · 390px" },
  { id: "768", n: "768", Icon: Tablet, size: 16, label: "Tablet · 768px" },
  { id: "1280", n: "1280", Icon: Monitor, size: 17, label: "Desktop · 1280px" },
  { id: "fit", n: "Fit", Icon: Scan, size: 15, label: "Fit to pane" },
];

function WidthRow({ value, quiet }) {
  return (
    <div class={`lp2-widths${quiet ? " is-quiet" : ""}`} role="radiogroup" aria-label="Viewport width">
      {WIDTHS.map((w) => {
        const on = !quiet && w.id === value;
        return (
          <button
            key={w.id}
            type="button"
            class={`lp2-w${on ? " is-on" : ""}`}
            role="radio"
            aria-checked={on}
            aria-label={w.label}
            title={w.label}
            disabled={quiet}
          >
            <w.Icon size={w.size} aria-hidden="true" />
            {on && <span class="lp2-w-n">{w.n}</span>}
          </button>
        );
      })}
    </div>
  );
}

// The address IS the title and the menu. The dot is STATE, so it only exists
// when there is a state to report: with nothing loaded there is no dot (a
// green "idle" dot over "No app loaded" reads as "running fine", which is the
// opposite of true).
function AddressButton({ url, state, empty }) {
  return (
    <button type="button" class={`lp2-addr${empty ? " is-empty" : ""}`} aria-haspopup="menu">
      {!empty && <StateDot state={state} />}
      <span class="lp2-addr-url">{url}</span>
      <ChevronDown size={13} class="lp2-addr-chev" aria-hidden="true" />
    </button>
  );
}

function Rail({ empty, url, width = "fit", inspect, phone }) {
  if (phone) {
    // 390: the preview IS the screen. One 44px row, and only what a thumb
    // needs there — where you are, the inspect mode that makes the preview
    // worth having on a phone, and out. The four widths go into the menu:
    // previewing 1280 inside 390 is a thumbnail, not a viewport.
    return (
      <div class="lp2-rail is-phone">
        <button type="button" class="lp2-icon" disabled={empty} aria-label="Back in preview">
          <ArrowLeft size={18} aria-hidden="true" />
        </button>
        <AddressButton url={url} state={empty ? "idle" : "running"} empty={empty} />
        <button type="button" class={`lp2-icon${inspect ? " is-on" : ""}`} disabled={empty} aria-label="Inspect">
          <MousePointerClick size={18} />
        </button>
        <span class="lp2-div" aria-hidden="true" />
        <button type="button" class="lp2-icon" aria-label="Close preview">
          <X size={18} />
        </button>
      </div>
    );
  }
  return (
    <div class="lp2-rail">
      <div class="lp2-zone lp2-zone-what">
        <button type="button" class="lp2-icon" disabled={empty} aria-label="Back in preview">
          <ArrowLeft size={16} aria-hidden="true" />
        </button>
        <AddressButton url={url} state={empty ? "idle" : "running"} empty={empty} />
        <button type="button" class="lp2-icon" disabled={empty} aria-label="Reload">
          <RotateCw size={15} aria-hidden="true" />
        </button>
      </div>
      <div class="lp2-zone lp2-zone-how">
        <WidthRow value={width} quiet={empty} />
        <button type="button" class={`lp2-icon lp2-inspect${inspect ? " is-on" : ""}`} disabled={empty} aria-label="Inspect" title="Inspect — tap an element in the app">
          <MousePointerClick size={16} />
        </button>
      </div>
      <span class="lp2-div" aria-hidden="true" />
      <button type="button" class="lp2-icon lp2-close" aria-label="Close preview">
        <X size={16} />
      </button>
    </div>
  );
}

/* ═══════════════════════════════════════════════════════════════════════════
   THE STAGE CONTENT
   ═══════════════════════════════════════════════════════════════════════════ */

const PORTS = [{ port: "5173", what: "Vite" }, { port: "3000", what: "Next" }, { port: "8080", what: "" }];

// Direction A, approved. Unchanged except that it now sits in the new panel.
function CardA({ again }) {
  return (
    <div class="lp2-empty">
      <div class="lp2-card">
        {again ? (
          <>
            <h2 class="lp2-title">Open your app</h2>
            <button type="button" class="lp2-last">
              <StateDot state="running" />
              <span class="lp2-last-url">http://localhost:5173</span>
              <span class="lp2-last-when">12m ago</span>
              <span class="lp2-last-go" aria-hidden="true"><RotateCw size={14} /></span>
            </button>
            <div class="lp2-or"><span>or</span></div>
            <div class="lp2-row">
              <Field variant="box" size="lg" mono class="lp2-field" type="url"
                placeholder="http://localhost:3000" aria-label="Preview URL" />
              <button type="button" class="lp2-go">Load</button>
            </div>
          </>
        ) : (
          <>
            <h2 class="lp2-title">Paste the address of your running app</h2>
            <p class="lp2-lede">It opens here, beside the conversation, and reloads itself while Moa edits.</p>
            <div class="lp2-row">
              <Field variant="box" size="lg" mono class="lp2-field" type="url"
                placeholder="http://localhost:5173" aria-label="Preview URL" />
              <button type="button" class="lp2-go">Load</button>
            </div>
            <div class="lp2-ports">
              {PORTS.map((p) => (
                <button type="button" class="lp2-port" key={p.port}>
                  <span class="lp2-port-n">:{p.port}</span>
                  {p.what && <span class="lp2-port-w">{p.what}</span>}
                </button>
              ))}
            </div>
          </>
        )}
      </div>
    </div>
  );
}

// A FAKE app, deliberately nothing like moa: light, its own type, its own
// colour. The point of this shot is that the chrome has to survive somebody
// else's design sitting inside it.
function FakeApp({ phone }) {
  return (
    <div class={`fa${phone ? " is-phone" : ""}`}>
      <header class="fa-top">
        <span class="fa-logo">northwind</span>
        <nav class="fa-nav"><span>Products</span><span>Pricing</span><span>Docs</span></nav>
        <span class="fa-cta">Get started</span>
      </header>
      <section class="fa-hero">
        <h1>Ship your inventory in an afternoon</h1>
        <p>One API for stock, orders and returns. Free while you build.</p>
        <span class="fa-btn">Start free</span>
      </section>
      <section class="fa-grid">
        {["Stock", "Orders", "Returns", "Webhooks", "Reports", "Access"].map((t) => (
          <article class="fa-card" key={t}>
            <span class="fa-card-ic" />
            <h3>{t}</h3>
            <p>Keep every warehouse in step without writing the sync yourself.</p>
          </article>
        ))}
      </section>
    </div>
  );
}

/* ═══════════════════════════════════════════════════════════════════════════
   THE PANEL
   The edge is the second half of this round. Today the panel is padded and the
   stage draws its own rim inside that padding: a border inside a border, two
   radii, and a strip of moa-coloured nothing between them.

   Here the panel is ONE object: one rim, one radius, the rail flush on top of
   the surface and the stage filling everything under it, clipped by the panel's
   own corners. The live edge (the 2px that carries the tool in flight) moves to
   the PANEL's rim, so it is the whole tool that breathes and not a rectangle
   floating inside it.
   ═══════════════════════════════════════════════════════════════════════════ */

/* ═══════════════════════════════════════════════════════════════════════════
   THE CHROME — proposal 2 · Muelle
   Answers the 62px question with zero. There is no bar over the app at all:
   loaded, the app owns the panel edge to edge, and every control lives in one
   floating dock at the BOTTOM — where the thumb is on a phone, where "Write to
   Moa" already was, and out of the way of the header of whatever site is being
   previewed (which is where a top bar always collides).

   Empty, there is nothing to float over, so the dock is not drawn: the panel
   carries only a close affordance and the card has the stage to itself.

   Its cost, stated: the address stops being a permanent title, so "what am I
   looking at" needs a glance down instead of up.
   ═══════════════════════════════════════════════════════════════════════════ */

function Dock({ url, width = "fit", inspect, phone }) {
  return (
    <div class={`lp2-dock${phone ? " is-phone" : ""}`}>
      <button type="button" class="lp2-icon" aria-label="Back in preview">
        <ArrowLeft size={16} aria-hidden="true" />
      </button>
      <button type="button" class="lp2-addr lp2-dock-addr" aria-haspopup="menu">
        <StateDot state="running" />
        <span class="lp2-addr-url">{url}</span>
        <ChevronDown size={13} class="lp2-addr-chev" aria-hidden="true" />
      </button>
      {!phone && (
        <>
          <span class="lp2-div" aria-hidden="true" />
          <WidthRow value={width} />
        </>
      )}
      <span class="lp2-div" aria-hidden="true" />
      <button type="button" class={`lp2-icon${inspect ? " is-on" : ""}`} aria-label="Inspect">
        <MousePointerClick size={16} />
      </button>
      <button type="button" class="lp2-dock-write">
        <PencilLine size={14} aria-hidden="true" />
        {!phone && "Write to Moa"}
      </button>
    </div>
  );
}

// Empty, the dock has nothing to float over. Close is the only thing the panel
// needs before it holds anything, and it floats on the stage rather than
// justifying a whole bar for one glyph.
function GhostClose() {
  return (
    <button type="button" class="lp2-ghost-close" aria-label="Close preview">
      <X size={16} />
    </button>
  );
}

function Panel2({ id, phone, live, children, rail }) {
  return (
    <div class={`lp2-frame${phone ? " is-phone" : ""}`} data-t={id}>
      <div class={`lp2-panel${live ? ` is-${live}` : ""}`}>
        {rail}
        <div class={`lp2-stage${live === "loaded" ? " has-app" : ""}`}>{children}</div>
      </div>
    </div>
  );
}

// Proposal 2's panel: no rail slot at all, the dock floats INSIDE the stage.
function PanelDock({ id, phone, loaded, children, dock }) {
  return (
    <div class={`lp2-frame${phone ? " is-phone" : ""}`} data-t={id}>
      <div class="lp2-panel is-bare">
        <div class={`lp2-stage${loaded ? " has-app" : ""}`}>
          {children}
          {dock}
        </div>
      </div>
    </div>
  );
}

const SHOTS = {
  "1": (phone) => (
    <Panel2 id="1" phone={phone} rail={<Rail empty phone={phone} url="No app loaded" />}>
      <CardA />
    </Panel2>
  ),
  "1b": (phone) => (
    <Panel2 id="1b" phone={phone} rail={<Rail empty phone={phone} url="No app loaded" />}>
      <CardA again />
    </Panel2>
  ),
  "1c": (phone) => (
    <Panel2
      id="1c"
      phone={phone}
      live="loaded"
      rail={<Rail phone={phone} url="localhost:5173" width={phone ? "390" : "1280"} inspect />}
    >
      <FakeApp phone={phone} />
    </Panel2>
  ),

  // ── proposal 2 ──────────────────────────────────────────────────────────
  "2": (phone) => (
    <PanelDock id="2" phone={phone} dock={<GhostClose />}>
      <CardA />
    </PanelDock>
  ),
  "2c": (phone) => (
    <PanelDock
      id="2c"
      phone={phone}
      loaded
      dock={<Dock phone={phone} url="localhost:5173" width={phone ? "390" : "1280"} inspect />}
    >
      <FakeApp phone={phone} />
    </PanelDock>
  ),
};

export function LP2Lab() {
  const params = typeof location !== "undefined" ? new URLSearchParams(location.search) : new URLSearchParams();
  const phone = params.get("dens") === "phone";
  const only = params.get("t");
  const ids = only ? [only] : ["1", "1b", "1c", "2", "2c"];
  return (
    <div class={`lp2-lab${phone ? " is-phone" : ""}${only ? " is-bare" : ""}`}>
      {!only && (
        <header class="lp2-lab-head">
          <h1>live preview · <em>el panel entero</em></h1>
          <p>
            Cromo nuevo sobre la tarjeta A, ya aprobada. Un riel de 40px con tres zonas —qué está cargado,
            cómo se muestra, y el panel mismo—, los cuatro presets sin caja ni números, Inspect junto a lo
            que modifica, y cerrar aislado tras un separador. El panel pasa a ser un solo objeto: un borde,
            un radio, y el escenario a sangre debajo del riel.
          </p>
        </header>
      )}
      {ids.map((id) => (
        <figure class={`lp2-fig${phone ? " is-phone" : ""}`} key={id}>
          {SHOTS[id] ? SHOTS[id](phone) : null}
        </figure>
      ))}
    </div>
  );
}
