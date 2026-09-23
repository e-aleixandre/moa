import { createContext } from "preact";
import { useContext, useEffect, useState } from "preact/hooks";
import { AVATAR_COLORS, AVATAR_SHAPES, OwnerAvatarFor } from "../components/Owners/OwnerAvatar.jsx";
import { OwnerFace, FACE_VARIANTS, OPT_IN_SHAPES, faceBodyColor } from "../components/Owners/OwnerFace.jsx";
import { facePersonality, faceMotion } from "../components/Owners/faceMotion.js";
import { MOA_OWNER, WINERIM, WINERIM_WEB } from "./owners3-fixtures.js";
import "./owner-faces-lab.css";

/* Owner faces — ways of giving the owner's mark eyes that are alive.
   Catalog only: the product still draws OwnerAvatar everywhere. Every face
   here is the candidate component itself (components/Owners/OwnerFace.jsx),
   on the shared scheduler (faceMotion.js). A · Mirada is the main proposal;
   B and C stay as the alternatives. */

const own = (name, codebase_key, extra = {}) => ({ id: `own_${codebase_key}`, name, codebase_key, ...extra });

// Eight owners: the three fixture owners (with their chosen avatars) and five
// on their deterministic default, so both paths are on the page. The Winerim
// pair is the one the mark exists to tell apart.
const SAMPLE = [
  WINERIM,
  WINERIM_WEB,
  MOA_OWNER,
  own("Catas app", "catas-app"),
  own("Facturas API", "facturas-api"),
  own("Etiquetas PDF", "etiquetas-pdf"),
  own("Landing 2026", "landing-2026"),
  own("Sommelier bot", "sommelier-bot"),
];

// The phone grid "with state": a believable morning, every state present.
const GRID_STATES = ["asks", "working", "idle", "working", "idle", "saved", "idle", "asks"];

const STRESS_KEYS = [
  "winerim-backend", "winerim-web", "moa", "facturas-api", "portal-clientes", "moa-mobile",
  "tpv-bodega", "dotfiles", "sommelier-bot", "almacen-sync", "etiquetas-pdf", "crm-bodegas",
  "infra-terraform", "landing-2026", "stock-alerts", "pedidos-b2b", "blog-vinos",
  "api-gateway", "ml-maridajes", "backoffice",
];
const STRESS = STRESS_KEYS.map((k) => own(k, k));

const SIZES = [24, 32, 40, 96];

const ROWS = [
  { owner: WINERIM, state: "idle", line: "3 live · 2 waiting on you" },
  { owner: WINERIM_WEB, state: "working", line: "Running · 4m" },
  { owner: MOA_OWNER, state: "idle", line: "Answered · not read yet" },
  { owner: SAMPLE[3], state: "asks", line: "Asks you: ¿migramos hoy?" },
  { owner: SAMPLE[4], state: "idle", line: "2 live" },
  { owner: SAMPLE[7], state: "saved", line: "Saved" },
];

const STATES = [
  { id: "idle", word: "Idle" },
  { id: "working", word: "Working" },
  { id: "asks", word: "Waiting for you" },
  { id: "saved", word: "Saved" },
];
const WORD = Object.fromEntries(STATES.map((s) => [s.id, s.word]));

// The travel of the eyes, pinned: where the idle script can take them (±0.6)
// and the rest of the two states that hold a gaze.
const GAZES = [
  { id: "left", g: [-0.6, 0] },
  { id: "up", g: [0, -0.6] },
  { id: "centre", g: [0, 0] },
  { id: "down", g: [0, 0.6] },
  { id: "right", g: [0.6, 0] },
  { id: "working", g: [0, 0], state: "working" },
  { id: "waiting", g: [0, 0], state: "asks" },
];

const LABEL = { mirada: "A · Mirada", pupilas: "B · Pupilas", sobria: "C · Sobria" };
const BLURB = {
  mirada: "Main. A flat ball, two white strokes. Turns like a head.",
  pupilas: "Today's lit tile. Pupils move inside the whites.",
  sobria: "Today's mark exactly. Blinks and a rare glance.",
};

// The lab's switches every face reads (only the sheen, for now).
const Lab = createContext({ sheen: false });

function Face(props) {
  const { sheen } = useContext(Lab);
  return <OwnerFace sheen={props.variant === "mirada" && sheen} {...props} />;
}

function Seg({ label, value, options, onChange }) {
  return (
    <div class="ofl-seg" role="radiogroup" aria-label={label}>
      {options.map((o) => (
        <button
          type="button"
          key={o.id}
          role="radio"
          aria-checked={o.id === value}
          class={`ofl-opt${o.id === value ? " is-on" : ""}`}
          onClick={() => onChange(o.id)}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}

function Toggle({ label, on, onChange, testid }) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={on}
      data-testid={testid}
      class={`ofl-opt ofl-toggle${on ? " is-on" : ""}`}
      onClick={() => onChange(!on)}
    >
      <span class="ofl-knob" aria-hidden="true" />
      {label}
    </button>
  );
}

function Section({ id, title, hint, children }) {
  return (
    <section class="ofl-sec" data-testid={`faces-${id}`}>
      <h2 class="ofl-h">{title}</h2>
      {hint && <p class="ofl-hint">{hint}</p>}
      {children}
    </section>
  );
}

function SizesColumn({ variant }) {
  const before = variant === "before";
  return (
    <div class={`ofl-col${before ? " is-before" : ""}`} data-testid={before ? "faces-before" : `faces-sizes-${variant}`}>
      <div class="ofl-col-h">
        <span class="ofl-col-t">{before ? "Antes · OwnerAvatar" : LABEL[variant]}</span>
        <span class="ofl-col-s">{before ? "Today's static mark." : BLURB[variant]}</span>
      </div>
      {SAMPLE.map((o) => (
        <div class="ofl-sizes" key={o.id}>
          {SIZES.map((s) => (
            <span class="ofl-cell" key={s} style={{ width: `${s}px` }}>
              {before
                ? <OwnerAvatarFor owner={o} size={s} />
                : <Face variant={variant} owner={o} size={s} title={o.name} />}
            </span>
          ))}
          {before && <span class="ofl-name">{o.name}</span>}
        </div>
      ))}
    </div>
  );
}

function SidebarMock({ variant }) {
  return (
    <div class="ofl-side" data-testid={`faces-sidebar-${variant}`}>
      <div class="ofl-side-label">{LABEL[variant]}</div>
      <div class="ofl-side-head">Owners</div>
      {ROWS.map(({ owner, state, line }) => (
        <div class={`ofl-row is-${state}`} key={owner.id}>
          <Face variant={variant} owner={owner} state={state} size={32} />
          <span class="ofl-row-text">
            <span class="ofl-row-name">{owner.name}</span>
            <span class="ofl-row-line">{line}</span>
          </span>
        </div>
      ))}
    </div>
  );
}

// The phone's empty-state grid. `withState` is the owner's open question:
// the same grid, all idle, or every face behaving as its owner is.
function GridPhone({ variant, withState }) {
  return (
    <div class="ofl-phone" data-testid={`faces-grid-${variant}-${withState ? "state" : "plain"}`}>
      <div class="ofl-phone-cap">{withState ? "With state" : "Without state"}</div>
      <div class="ofl-grid">
        {SAMPLE.map((o, i) => {
          const state = withState ? GRID_STATES[i] : "idle";
          return (
            <div class={`ofl-grid-cell is-${state}`} key={o.id}>
              <Face variant={variant} owner={o} state={state} size={40} />
              <span class="ofl-pill">{o.name}</span>
              {withState && state !== "idle" && <span class="ofl-grid-word">{WORD[state]}</span>}
            </div>
          );
        })}
      </div>
    </div>
  );
}

function Personality({ owner }) {
  const p = facePersonality(owner.codebase_key);
  return (
    <dl class="ofl-pers">
      <dt>blink</dt><dd>{(p.blinkMean / 1000).toFixed(1)}s · ×2 {Math.round(p.doubleBlink * 100)}%</dd>
      <dt>gaze</dt><dd>rest {(p.dwellMean / 1000).toFixed(1)}s · range {p.gazeRange}</dd>
      <dt>eyes</dt><dd>lean {p.tilt}° ± {p.skew}° · len {p.strokeLen} · gap {p.eyeGap}</dd>
      <dt>body</dt><dd>breath {(p.breath / 1000).toFixed(1)}s · nudge {(p.nudgeEvery / 1000).toFixed(1)}s</dd>
      <dt>B</dt><dd>pupil {p.pupil} · {p.sclera}</dd>
    </dl>
  );
}

/* ── Edit owner — character ──────────────────────────────────────────────
   The existing OwnerIdentityPicker, redrawn around the main proposal: the
   face large and alive on top, the name, then colour and shape as two
   scrolling rows. Unchosen shapes are grey so the one in colour is the
   choice. The opt-in shapes sit after a divider: they are never a default
   (see OPT_IN_SHAPES) and the server does not store them yet. */
function CharacterEditor({ phone }) {
  const [name, setName] = useState("Winerim");
  const [color, setColor] = useState("peach");
  const [shape, setShape] = useState("squircle");
  const { sheen } = useContext(Lab);
  const seedKey = "winerim-backend";
  const shapeBtn = (s) => (
    <button
      type="button"
      role="radio"
      aria-checked={s === shape}
      aria-label={s}
      key={s}
      class={`ofl-ce-shape${s === shape ? " is-on" : ""}`}
      onClick={() => setShape(s)}
    >
      <OwnerFace variant="mirada" shape={s} color={color} seedKey={seedKey} muted={s !== shape} sheen={sheen} gaze={[0, 0]} size={36} />
      {OPT_IN_SHAPES.includes(s) && <span class="ofl-ce-new">new</span>}
    </button>
  );
  return (
    <div class={`ofl-ce${phone ? " is-phone" : ""}`} data-testid={`faces-character-${phone ? "phone" : "desktop"}`}>
      <div class="ofl-ce-head">
        {phone && <span class="ofl-ce-back" aria-hidden="true">‹</span>}
        <span class="ofl-ce-title">Edit owner</span>
      </div>
      <div class="ofl-ce-preview">
        <Face variant="mirada" shape={shape} color={color} seedKey={seedKey} size={phone ? 112 : 128} follow />
      </div>
      <input
        class="ofl-ce-name"
        value={name}
        onInput={(e) => setName(e.currentTarget.value)}
        aria-label="Name"
        placeholder="Name"
      />
      <div class="ofl-ce-h">Character</div>
      <div class="ofl-ce-row" role="radiogroup" aria-label="Colour">
        {AVATAR_COLORS.map((c) => (
          <button
            type="button"
            role="radio"
            aria-checked={c.id === color}
            aria-label={c.id}
            key={c.id}
            class={`ofl-ce-colour${c.id === color ? " is-on" : ""}`}
            // The colour as Mirada wears it (one step deeper), not the token.
            style={{ "--c": faceBodyColor(c.id) }}
            onClick={() => setColor(c.id)}
          />
        ))}
      </div>
      <div class="ofl-ce-row" role="radiogroup" aria-label="Shape">
        {AVATAR_SHAPES.map(shapeBtn)}
        <span class="ofl-ce-div" aria-hidden="true" />
        {OPT_IN_SHAPES.map(shapeBtn)}
      </div>
      <p class="ofl-ce-foot">Pick a colour and a shape. {name || "This owner"} will look like this everywhere.</p>
      <div class="ofl-ce-actions">
        <button type="button" class="ofl-ce-btn">Cancel</button>
        <button type="button" class="ofl-ce-btn is-primary">Save</button>
      </div>
    </div>
  );
}

export function OwnerFacesLab() {
  const [only, setOnly] = useState("all");
  const [follow, setFollow] = useState(true);
  const [reduced, setReduced] = useState(false);
  const [frozen, setFrozen] = useState(false);
  const [sheen, setSheen] = useState(false);
  const [bigIdx, setBigIdx] = useState(0);
  const big = SAMPLE[bigIdx];

  useEffect(() => { faceMotion()?.setForcedReduced(reduced); }, [reduced]);
  useEffect(() => { faceMotion()?.setFrozen(frozen); }, [frozen]);
  // Leaving the lab must not leave the shared clock paused.
  useEffect(() => () => {
    faceMotion()?.setFrozen(false);
    faceMotion()?.setForcedReduced(false);
  }, []);

  const variants = only === "all" ? FACE_VARIANTS : [only];

  return (
    <Lab.Provider value={{ sheen }}>
      <div class="ofl" data-testid="faces-lab">
        <header class="ofl-head">
          <h1>Owner faces</h1>
          <p>Each owner has its own deterministic rhythm. The face behaves by state; the words on the row still say it.</p>
        </header>

        <div class="ofl-ctl" data-testid="faces-controls">
          <Seg
            label="Proposal"
            value={only}
            onChange={setOnly}
            options={[{ id: "all", label: "All" }, ...FACE_VARIANTS.map((v) => ({ id: v, label: LABEL[v] }))]}
          />
          <div class="ofl-toggles">
            <Toggle label="Sheen (A)" on={sheen} onChange={setSheen} testid="faces-toggle-sheen" />
            <Toggle label="Pointer follow" on={follow} onChange={setFollow} testid="faces-toggle-follow" />
            <Toggle label="Reduced motion" on={reduced} onChange={setReduced} testid="faces-toggle-reduced" />
            <Toggle label="Freeze" on={frozen} onChange={setFrozen} testid="faces-toggle-freeze" />
          </div>
        </div>

        <Section id="big" title="128 px" hint={follow ? "Move the pointer: the eyes follow it, then go back to their own script." : "Idle script only."}>
          <div class="ofl-big-pick">
            <Seg
              label="Owner"
              value={String(bigIdx)}
              onChange={(i) => setBigIdx(Number(i))}
              options={SAMPLE.map((o, i) => ({ id: String(i), label: o.name }))}
            />
          </div>
          <div class="ofl-big">
            {variants.map((v) => (
              <figure class="ofl-big-cell" key={v} data-testid={`faces-big-${v}`}>
                <Face variant={v} owner={big} size={128} follow={follow} title={big.name} />
                <figcaption>
                  <span class="ofl-col-t">{LABEL[v]}</span>
                  <span class="ofl-col-s">{BLURB[v]}</span>
                </figcaption>
              </figure>
            ))}
          </div>
        </Section>

        <Section id="states" title="By state" hint="Animated. Idle breathes and looks around · Working narrows its eyes on the work · Waiting looks at you and nudges · Saved rests.">
          <div class="ofl-poses">
            {variants.map((v) => (
              <div class="ofl-pose-line" key={v} data-testid={`faces-states-${v}`}>
                <span class="ofl-col-t">{LABEL[v]}</span>
                {STATES.map(({ id, word }) => (
                  <figure class="ofl-pose" key={id}>
                    <Face variant={v} owner={big} state={id} size={64} />
                    <Face variant={v} owner={WINERIM_WEB} state={id} size={32} />
                    <Face variant={v} owner={MOA_OWNER} state={id} size={24} />
                    <figcaption class={`is-${id}`}>{word}</figcaption>
                  </figure>
                ))}
              </div>
            ))}
          </div>
        </Section>

        <Section id="grid" title="Mobile grid: two versions" hint="40 px, names in pills. Left: every face idle. Right: each face behaves as its owner is; the word stays under it.">
          {variants.map((v) => (
            <div class="ofl-grid-pair" key={v}>
              <div class="ofl-side-label">{LABEL[v]}</div>
              <div class="ofl-sides">
                <GridPhone variant={v} withState={false} />
                <GridPhone variant={v} withState />
              </div>
            </div>
          ))}
        </Section>

        <Section id="character" title="Edit owner — character" hint="Mirada in the picker. Click a colour or a shape; the preview follows the pointer.">
          <div class="ofl-sides is-character">
            <div>
              <div class="ofl-side-label">Desktop · sheet</div>
              <CharacterEditor />
            </div>
            <div>
              <div class="ofl-side-label">Phone · page in the sheet · 390</div>
              <div class="ofl-phone-frame"><CharacterEditor phone /></div>
            </div>
          </div>
        </Section>

        <Section id="sizes" title="Eight owners · 24 32 40 96">
          <div class="ofl-cols">
            <SizesColumn variant="before" />
            {variants.map((v) => <SizesColumn key={v} variant={v} />)}
          </div>
        </Section>

        <Section id="gaze" title="Where the eyes go" hint="Pinned poses: the idle script's reach (±0.6) and the rest of working and waiting.">
          <div class="ofl-poses">
            {variants.map((v) => (
              <div class="ofl-pose-line" key={v} data-testid={`faces-gaze-${v}`}>
                <span class="ofl-col-t">{LABEL[v]}</span>
                {GAZES.map(({ id, g, state }) => (
                  <figure class="ofl-pose" key={id}>
                    <Face variant={v} owner={big} state={state} gaze={g} size={64} />
                    <Face variant={v} owner={big} state={state} gaze={g} size={24} />
                    <figcaption>{id}</figcaption>
                  </figure>
                ))}
              </div>
            ))}
          </div>
        </Section>

        <Section id="sidebar" title="Sidebar · 32 px rows">
          <div class="ofl-sides">
            {variants.map((v) => <SidebarMock key={v} variant={v} />)}
          </div>
        </Section>

        <Section id="stress" title="Twenty at once" hint="One timer for the whole page; offscreen faces are skipped.">
          {variants.map((v) => (
            <div class="ofl-stress" key={v} data-testid={`faces-stress-${v}`}>
              <span class="ofl-col-t">{LABEL[v]}</span>
              <div class="ofl-stress-row">
                {STRESS.map((o) => <Face key={o.id} variant={v} owner={o} size={32} title={o.name} />)}
              </div>
            </div>
          ))}
        </Section>

        <Section id="personality" title="Personality per owner" hint="Derived from codebase_key; the same on every load.">
          <div class="ofl-pers-grid">
            {SAMPLE.map((o) => (
              <div class="ofl-pers-card" key={o.id}>
                <Face variant="mirada" owner={o} size={40} />
                <div>
                  <div class="ofl-row-name">{o.name} <span class="ofl-key">{o.codebase_key}</span></div>
                  <Personality owner={o} />
                </div>
              </div>
            ))}
          </div>
        </Section>
      </div>
    </Lab.Provider>
  );
}
