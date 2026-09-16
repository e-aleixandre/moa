import { useLayoutEffect, useRef } from "preact/hooks";
import { Mic, Loader2, Paperclip, Monitor, Boxes, ShieldCheck, Gauge, Image as ImageIcon, X, Square } from "lucide-preact";
import { StatusStrip } from "../layout/StatusStrip/StatusStrip.jsx";
import "../layout/Composer/Composer.css";
import "../layout/LiveBar/LiveBar.css";
import "../components/ActionMenu/ActionMenu.css";
import "./c2-lab.css";

/* c2-lab — CATALOG ONLY. The two-row composer, three directions.
 *
 * Nothing here is imported by production: the mock draws the REAL composer
 * classes (.zl-composer, .zl-ta, .zl-attach, .zl-send, .zl-mic, .cache-warn,
 * .attach-preview-strip, .queue-note, .steer-hint) with Composer.css loaded,
 * so what the shots show is the shipped skin re-arranged, not a repaint of it.
 * The only new classes are the `.c2-*` ones that make the second row exist.
 *
 * WHY TWO ROWS. Today the pill has room for exactly one control at its end
 * (Composer.jsx:823-825), so on the phone that button is the mic OR Send. With
 * tap-to-record, the moment there is a draft it becomes Send and dictation is
 * gone — and "dictate, fix a word, dictate some more" is the natural way to
 * use it. Two rows give the mic and Send a permanent seat each, in both
 * densities.
 *
 * `.zl-composer` is already `display:flex; flex-wrap:wrap` (Composer.css:71),
 * so the second row is a `flex: 1 0 100%` child, not a rewrite. The existing
 * full-width children (cache warning, attachment strip, queue note, steer
 * hint) already use that same trick with `order: 5` — the control row takes
 * `order: 6` so it stays last.
 *
 * THE SECOND QUESTION, layered on top: the bottom status line is not a strip
 * of readouts, it is a row of DOORS (MobileStatusLine.jsx:18-45) — model,
 * permission, MCP, and the usage ring. The three directions answer "where does
 * each door go" differently, and each one says what it costs.
 */

/* ── fixtures ─────────────────────────────────────────────────────────────
   One conversation, frozen, so every frame is the same moment seen under a
   different composer. */

const DRAFT = "Check the 0.38 release notes and tell me if the cache warning still fires on a cold session";

const TRANSCRIPT = [
  { who: "me", text: "Where does the prompt cache warning come from?" },
  { who: "moa", text: "It is raised by the composer when the session's cache window has expired: the next message pays a full cache write. The check lives in the session model, not in the view." },
  { who: "me", text: "And on a cold session, does it fire at all?" },
];

const STATUS = {
  model: "Daybreak Blue", thinking: "medium", ctx: 63, spend: "$1.84",
  up: 12400, down: 1800,
};

const SESSION_FIXTURE = {
  permissionMode: "auto",
  mcp: { total: 3, unhealthy: 1, disabled: 0 },
};

/* ── the parts every direction shares ─────────────────────────────────── */

function SendIcon() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true">
      <path d="M8 13V3.5M8 3.5L3.8 7.7M8 3.5l4.2 4.2" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" />
    </svg>
  );
}

function PlusIcon({ size = 18 }) {
  return (
    <svg viewBox="0 0 16 16" width={size} height={size} aria-hidden="true">
      <path d="M8 3.5v9M3.5 8h9" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" />
    </svg>
  );
}

// The text field. A real <textarea> on purpose: 16px is the iOS focus-zoom
// floor and a <div> would not prove it. It grows to its content the way the
// shipped autoResize does, or a two-line draft would be clipped in the shot
// and the row heights being judged would be wrong.
function Field({ text, placeholder }) {
  const ref = useRef(null);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${Math.min(el.scrollHeight, 132)}px`;
  }, [text]);
  return (
    <textarea
      ref={ref}
      class="zl-ta"
      rows={1}
      readOnly
      aria-label="Message moa"
      placeholder={placeholder || "Message moa"}
      value={text || ""}
    />
  );
}

// Mic and Send, the pair that now coexists in every state. `voice` is the
// live voice state: null | "recording" | "transcribing". The recording face is
// Composer.css:240-256's, untouched — solid peach, round, haloed.
function Mic1({ voice, mic = "flat" }) {
  const cls = [
    "zl-attach", "zl-mic",
    voice === "recording" ? "recording" : "",
    voice === "transcribing" ? "transcribing" : "",
    mic === "filled" ? "c2-mic-filled" : "",
  ].filter(Boolean).join(" ");
  return (
    <button type="button" class={cls} aria-label={voice === "recording" ? "Stop recording" : "Dictate"} title="Dictate">
      {voice === "transcribing" ? <Loader2 size={15} class="spin" /> : <Mic size={15} />}
    </button>
  );
}

function Send1({ armed, round }) {
  return (
    <button
      type="button"
      class={`zl-send${round ? " c2-send-round" : ""}`}
      aria-label="Send"
      title="Send"
      disabled={!armed}
    >
      <SendIcon />
    </button>
  );
}

// The model chip, when a direction lifts it out of the status line. It wears
// the line's own vocabulary (word in t2, thinking meter in the accent) so the
// thing that moved is recognisably the same thing.
function ModelChip({ tone = "flat" }) {
  return (
    <button type="button" class={`c2-model is-${tone}`} aria-label="Model: Daybreak Blue, thinking medium">
      <span class="c2-model-name">{STATUS.model}</span>
      <span class="zl-think c2-think" aria-hidden="true">
        {[1, 2, 3, 4].map((k) => <i class={k <= 2 ? "" : "is-off"} key={k} />)}
      </span>
    </button>
  );
}

// Context: the one reading that is watched WITHOUT wanting to go anywhere —
// how much room is left before compaction. Drawn as a ring plus its number,
// the status line's own mark, so moving it does not reteach it.
function CtxMark({ pct = STATUS.ctx, withSpend }) {
  const r = 6.5;
  const c = 2 * Math.PI * r;
  const tone = pct >= 90 ? "is-hot" : pct >= 70 ? "is-warm" : "";
  return (
    <button type="button" class="c2-ctx" aria-label={`Context ${pct}% — show usage`} title={`Context ${pct}% used`}>
      <svg class={`zl-ring ${tone}`} viewBox="0 0 16 16" aria-hidden="true">
        <circle cx="8" cy="8" r={r} class="zl-ring-track" />
        <circle cx="8" cy="8" r={r} class="zl-ring-arc" stroke-dasharray={`${(c * pct) / 100} ${c}`} transform="rotate(-90 8 8)" />
      </svg>
      <span class="zl-data zl-num">{pct}<span class="zl-unit">%</span></span>
      {withSpend && <><span class="c2-ctx-sep" aria-hidden="true" /><span class="zl-data zl-num">{STATUS.spend}</span></>}
    </button>
  );
}

/* The `+` menu, drawn open. Real ActionMenu classes; the morph keyframes read
   two custom properties the component measures at runtime, so a static copy
   sets them by hand or the panel animates to an undefined width. */
function PlusMenu({ items, open }) {
  if (!open) return null;
  const h = items.reduce((n, it) => n + (it.sep ? 13 : 44), 12);
  return (
    <div
      class="action-menu-list action-menu-list--up c2-menu"
      role="menu"
      aria-label="More"
      style={{ "--action-menu-w": "244px", "--action-menu-h": `${h}px` }}
    >
      {items.map((it, i) => it.sep
        ? <div class="c2-menu-sep" key={`s${i}`} role="separator" />
        : (
          <button type="button" role="menuitem" class="action-menu-item" key={it.label}>
            <it.icon size={16} aria-hidden="true" />
            <span>{it.label}</span>
            {it.value && <span class={`c2-menu-val${it.tone ? ` is-${it.tone}` : ""}`}>{it.value}</span>}
          </button>
        ))}
    </div>
  );
}

const MENU_BASE = [
  { icon: Paperclip, label: "Attach files" },
  { icon: Monitor, label: "Live preview" },
  { icon: Boxes, label: "Artifacts" },
];

// What each direction's menu carries BEYOND today's three actions. This list
// is the honest answer to "where did that door go".
const MENU_DOORS = [
  { sep: true },
  { icon: ShieldCheck, label: "Permissions", value: "auto", tone: "green" },
  { icon: Boxes, label: "MCP servers", value: "1/3", tone: "yellow" },
  { icon: Gauge, label: "Usage & cost", value: STATUS.spend },
];

/* The live bar, mocked with LiveBar.css's own classes, for the "agent working"
   state. Stop belongs to this row and NOT to the composer — that is settled
   (Composer.jsx:52-56) and two rows must not reopen it. */
function LiveRow() {
  return (
    <div class="zl-live">
      <div class="zl-live-bar">
        <div class="zl-live-now" role="status">
          <span class="zl-live-dot is-working" aria-hidden="true" />
          <span class="zl-live-txt">Reading Composer.jsx</span>
          <span class="zl-live-el zl-data">1m 12s</span>
        </div>
        <button type="button" class="zl-live-stop" aria-label="Stop the run">
          <Square size={11} fill="currentColor" aria-hidden="true" />
          <span>Stop</span>
        </button>
      </div>
    </div>
  );
}

/* Slab extras: the full-width children the composer already has. They are in
   every direction because two rows must not break them. */
function Extras({ full }) {
  if (!full) return null;
  return (
    <>
      <div class="cache-warn">
        <span class="cache-warn-dot" />
        Prompt cache expired · your next message pays a cache write
      </div>
      <div class="attach-preview-strip">
        <div class="attach-chip">
          <span class="attach-chip-name">📎 trace-0.38.log <span class="attach-chip-size">(84 kB)</span></span>
          <button type="button" class="attach-chip-remove" aria-label="Remove"><X size={12} /></button>
        </div>
      </div>
    </>
  );
}

function QueueNote({ busy, text }) {
  if (!busy) return null;
  return (
    <button type="button" class="queue-note">
      <span class="chip is-sm is-mono c2-chip">2 queued</span>
      <span><ImageIcon size={13} aria-hidden="true" /> “and check the cold-start path too”</span>
    </button>
  );
}

function SteerHint({ busy, text }) {
  if (!busy || !text) return null;
  return <span class="steer-hint" aria-hidden="true">⏎ steers — won't interrupt</span>;
}

/* ══════════════════════════════════════════════════════════════════════════
   DIRECTION A — "Dos filas, nada más"
   ────────────────────────────────────────────────────────────────────────
   The minimum change that fixes the reported bug, and the reference the other
   two are judged against.

   Shape   one slab, two rows. The control row FLOATS: no fill of its own, no
           divider, it is the same plane as the field. `+` at the left, mic and
           Send at the right with a plain 6px gap — two separate buttons, two
           separate meanings, Send carrying the accent when armed.
   Doors   NOTHING MOVES. Model, permission, MCP and the usage ring stay on the
           status line, which is untouched.
   Cost    the dock grows ~44px (one control row) and the status line still
           costs its 36 below it — three horizontal bands under the transcript
           on a phone. Nothing is lost; the screen is just busier.
   ══════════════════════════════════════════════════════════════════════════ */
function ComposerA({ text, voice, busy, full }) {
  const armed = !!text || !!full;
  return (
    <div class={`zl-composer c2 c2-a${armed ? " is-armed" : ""}${busy ? " is-busy" : ""}`}>
      <Extras full={full} />
      <Field text={text} />
      <QueueNote busy={busy} />
      <SteerHint busy={busy} text={text} />
      <div class="c2-row">
        <button type="button" class="zl-attach" aria-label="More"><PlusIcon /></button>
        <span class="c2-spring" />
        <Mic1 voice={voice} />
        <Send1 armed={armed} />
      </div>
    </div>
  );
}

/* ══════════════════════════════════════════════════════════════════════════
   DIRECTION B — "El modelo baja a la fila, la línea adelgaza"
   ────────────────────────────────────────────────────────────────────────
   Shape   one slab, and the control row is a FOOTER: its own recessed fill and
           a hairline above it, so the row reads as the instrument's panel
           rather than as empty space under the text. Mic and Send are one
           segmented capsule at the right — they are the two ways of finishing
           the same message, so they are one object with two halves.
   Doors   model → INTO the composer, as a chip beside `+` (same sheet/popover
           it opens today).
           permission → the `+` menu.
           MCP → the `+` menu.
           usage/cost → the `+` menu, which opens the session panel's Usage.
           context → SURVIVES on the line, which shrinks to the gauges alone:
           one right-aligned ctx% + spend and nothing else.
   Cost    the permission COLOUR stops being visible at a glance. Today the
           chip is the safety reading as much as the door (yolo is not a thing
           you want to discover by opening a menu). Mitigated here by tinting
           the `+` glyph with the permission colour — a weaker signal than a
           worded chip, and the owner should decide whether that is enough.
   ══════════════════════════════════════════════════════════════════════════ */
function ComposerB({ text, voice, busy, full, menu }) {
  const armed = !!text || !!full;
  return (
    <div class={`zl-composer c2 c2-b${armed ? " is-armed" : ""}${busy ? " is-busy" : ""}`}>
      <Extras full={full} />
      <Field text={text} />
      <QueueNote busy={busy} />
      <SteerHint busy={busy} text={text} />
      <div class="c2-row c2-row-solid">
        <div class="c2-menu-host">
          <button type="button" class={`zl-attach c2-plus is-perm-auto${menu ? " is-open" : ""}`} aria-label="More"><PlusIcon /></button>
          <PlusMenu open={menu} items={[...MENU_BASE, ...MENU_DOORS]} />
        </div>
        <ModelChip tone="flat" />
        <span class="c2-spring" />
        <div class="c2-pair">
          <Mic1 voice={voice} />
          <Send1 armed={armed} />
        </div>
      </div>
    </div>
  );
}

/* ══════════════════════════════════════════════════════════════════════════
   DIRECTION C — "Sin línea de estado"
   ────────────────────────────────────────────────────────────────────────
   Shape   TWO OBJECTS, not one slab: the field keeps its pill, and the
           controls float bare on the dock underneath it, with no surface of
           their own. The dock is what holds them together. Send is the only
           round, filled thing on the row — it is the end of the sentence.
   Doors   model → the composer row, left.
           context → the composer row, right, immediately before mic+Send: the
           reading you watch without wanting to go anywhere sits where your eye
           already is when you write.
           permission → the `+` menu. The trigger carries the permission colour
           as a ring, so the safety state is still answerable at a glance
           without a line to carry it.
           MCP, usage, cost, per-run tokens → the `+` menu and the session
           panel. THE STATUS LINE IS GONE.
   Cost    spend and the token heartbeat stop being ambient. Cost is the one I
           would flag: it is currently the cheapest way to notice a run getting
           expensive, and in C you only see it if you go looking. MCP going
           unhealthy also loses its ambient yellow — here it is a badge on `+`,
           which is a smaller alarm than a worded chip.
   Gain    the phone gets 36px + the line's margin back, and the dock is two
           bands instead of three.
   ══════════════════════════════════════════════════════════════════════════ */
function ComposerC({ text, voice, busy, full, menu }) {
  const armed = !!text || !!full;
  return (
    /* `is-armed` has to be on the STACK as well as on the pill: the accent
       rule is `.zl-composer.is-armed .zl-send` (Composer.css:134), and in this
       direction Send is not inside the pill. Found by looking at the first
       shot — the arrow stayed grey with a full draft. It is the first real
       finding of splitting the slab in two. */
    <div class={`c2-stack${armed ? " is-armed" : ""}${busy ? " is-busy" : ""}`}>
      <div class={`zl-composer c2 c2-c-pill${armed ? " is-armed" : ""}`}>
        <Extras full={full} />
        <Field text={text} />
        <QueueNote busy={busy} />
        <SteerHint busy={busy} text={text} />
      </div>
      <div class="c2-row c2-row-bare">
        <div class="c2-menu-host">
          <button type="button" class={`zl-attach c2-plus c2-plus-ring is-perm-auto${menu ? " is-open" : ""}`} aria-label="More">
            <PlusIcon />
            <span class="c2-plus-badge" aria-hidden="true">1</span>
          </button>
          <PlusMenu open={menu} items={[...MENU_BASE, ...MENU_DOORS]} />
        </div>
        <ModelChip tone="quiet" />
        <span class="c2-spring" />
        <CtxMark />
        <Mic1 voice={voice} />
        <Send1 armed={armed} round />
      </div>
    </div>
  );
}

/* ── the status line each direction leaves behind ─────────────────────── */

function LineA() {
  return (
    <StatusStrip
      compact
      ctxPercent={STATUS.ctx}
      tokensUp={STATUS.up}
      tokensDown={STATUS.down}
      spend={STATUS.spend}
      session={SESSION_FIXTURE}
      onOpenUsage={() => {}}
      onOpenMcp={() => {}}
      onPerm={() => {}}
      onModel={() => {}}
      showTokens
      modelName={STATUS.model}
      thinking={STATUS.thinking}
      thinkingPosition={STATUS.thinking}
    />
  );
}

// B's remnant: the gauges tier alone, right-aligned. Same marks, one door.
function LineB() {
  return (
    <div class="c2-thin">
      <CtxMark withSpend />
    </div>
  );
}

const DIRS = {
  a: { id: "a", title: "A · Dos filas, nada más", Composer: ComposerA, Line: LineA, menu: MENU_BASE },
  b: { id: "b", title: "B · Modelo en el composer, línea adelgazada", Composer: ComposerB, Line: LineB, menu: [...MENU_BASE, ...MENU_DOORS] },
  c: { id: "c", title: "C · Sin línea de estado", Composer: ComposerC, Line: null, menu: [...MENU_BASE, ...MENU_DOORS] },
};

const STATES = {
  empty: { label: "Vacío" },
  text: { label: "Con texto · micro disponible", text: DRAFT },
  recording: { label: "Grabando con texto escrito", text: DRAFT, voice: "recording" },
  working: { label: "Agente trabajando · Stop", text: "and check the cold path", busy: true },
  menu: { label: "Menú + abierto", text: DRAFT, menu: true },
  full: { label: "Aviso de caché · adjunto · cola", text: DRAFT, busy: true, full: true },
};

/* ── hosts ──────────────────────────────────────────────────────────────── */

function Fake({ dense }) {
  return (
    <div class={`c2-stream${dense ? " is-dense" : ""}`} aria-hidden="true">
      {TRANSCRIPT.map((m, i) => (
        <div class={`c2-msg is-${m.who}`} key={i}><p>{m.text}</p></div>
      ))}
    </div>
  );
}

// One screen: transcript, optional live bar, dock. `density` only changes the
// host — the composer itself is the same component in both, which is the
// owner's first decision (phone and desktop behave alike).
export function C2Screen({ dir = "a", state = "empty", density = "phone" }) {
  const D = DIRS[dir] || DIRS.a;
  const S = STATES[state] || STATES.empty;
  const Composer = D.Composer;
  const Line = D.Line;
  const menuItems = D.menu;
  return (
    <div class={`c2-screen is-${density} c2-dir-${D.id}`}>
      <Fake dense={density === "phone"} />
      <div class={`zl-dock c2-dock${density === "desk" ? " c2-dock-desk" : ""}`}>
        {S.busy && <LiveRow />}
        <Composer text={S.text} voice={S.voice} busy={S.busy} full={S.full} menu={S.menu} />
        {/* Direction A never moved a door, so its `+` menu is today's three
            actions; B and C carry the doors they took. */}
        {S.menu && D.id === "a" && (
          <div class="c2-menu-float">
            <PlusMenu open items={menuItems} />
          </div>
        )}
        {Line && <Line />}
      </div>
    </div>
  );
}

/* ── the contact sheet ──────────────────────────────────────────────────── */

const NOTES = {
  a: [
    ["Modelo", "se queda en la línea de estado"],
    ["Permiso", "se queda en la línea (color visible de un vistazo)"],
    ["MCP", "se queda en la línea"],
    ["Uso / contexto", "anillo en la línea → panel Usage"],
    ["Coste", "tres bandas bajo el transcript en el móvil"],
  ],
  b: [
    ["Modelo", "chip dentro del composer"],
    ["Permiso", "menú +  · color sólo como tinte del glifo +"],
    ["MCP", "menú +"],
    ["Uso / coste", "menú + → panel Usage"],
    ["Contexto", "línea adelgazada: ctx% + gasto, a la derecha"],
  ],
  c: [
    ["Modelo", "chip dentro del composer"],
    ["Permiso", "menú + · anillo de color en el propio +"],
    ["MCP", "menú + · insignia cuando algo falla"],
    ["Uso / coste", "menú + → panel Usage (el gasto deja de ser ambiental)"],
    ["Contexto", "anillo en la fila de controles, junto a micro/enviar"],
  ],
};

function Frame({ dir, state, density }) {
  return (
    <div class={`c2-frame is-${density}`} data-shot={`${dir}-${state}-${density}`}>
      <C2Screen dir={dir} state={state} density={density} />
    </div>
  );
}

export function C2Lab() {
  const params = typeof location !== "undefined" ? new URLSearchParams(location.search) : new URLSearchParams();
  const one = params.get("dir");
  if (one) {
    return (
      <div class="c2-solo">
        <C2Screen dir={one} state={params.get("state") || "empty"} density={params.get("d") || "phone"} />
      </div>
    );
  }
  return (
    <div class="c2-lab">
      <header class="c2-head">
        <h1>Composer de <em>dos filas</em></h1>
        <p>
          El texto ocupa su fila; los controles viven en la de abajo. Micro y enviar coexisten en
          todos los estados, en móvil y en escritorio. Las tres direcciones se diferencian además
          en dónde acaba cada <strong>puerta</strong> de la línea de estado.
        </p>
      </header>
      {Object.values(DIRS).map((d) => (
        <section class="c2-dir" key={d.id}>
          <header class="c2-dir-head">
            <h2>{d.title}</h2>
            <dl class="c2-doors">
              {NOTES[d.id].map(([k, v]) => (
                <div key={k}><dt>{k}</dt><dd>{v}</dd></div>
              ))}
            </dl>
          </header>
          {["phone", "desk"].map((density) => (
            <div class="c2-strip" key={density}>
              {Object.keys(STATES).map((state) => (
                <figure class="c2-cell" key={state}>
                  <Frame dir={d.id} state={state} density={density} />
                  <figcaption>{STATES[state].label}</figcaption>
                </figure>
              ))}
            </div>
          ))}
        </section>
      ))}
    </div>
  );
}
