import { useMemo } from "preact/hooks";
import { EYE_CENTER, SHAPE_PATHS } from "../components/Owners/avatar-identity.js";
import { OwnerFace, faceBodyColor } from "../components/Owners/OwnerFace.jsx";
import "./owner-faces-identity.css";

/* Owner faces · identity (?view=faces-id). Lab only: nothing here reaches the
   product bundle.

   The owner's complaint: the Mirada face reads as Grok's bot. Not the outline
   — the gaze. What Grok and Mirada share is exactly:
     1. two short vertical white capsules as eyes, symmetric, high on a flat body;
     2. a vertical squash blink every few seconds;
     3. the pair sliding together in saccades to "look around";
     4. mood said by resizing the capsules (narrow = focused, wide = at you).
   None of the five directions below uses any of the four. Each keeps the
   identity system (shape × colour × tone) and replaces only the gaze, so one
   face per direction is enough to judge it. They are drawn with plain CSS and
   SMIL loops, which is fine for judging motion; the chosen one would then be
   rebuilt on faceMotion's compositor-friendly scheduler. */

export const DIRECTIONS = [
  {
    id: "visor",
    name: "A · Visor",
    idea: "One horizontal slot with a light inside. Attention is where the light is; no eyes, no blink.",
    states: {
      idle: "the light drifts slowly, half dimmed",
      working: "the light scans the slot end to end, leaving a trail",
      asks: "the light stops in the middle, widens and pulses at you",
    },
  },
  {
    id: "nucleo",
    name: "B · Núcleo",
    idea: "No features: a glowing core under the skin. The gaze is made of light.",
    states: {
      idle: "the glow wanders under the surface and breathes",
      working: "the glow orbits fast with a comet tail",
      asks: "the glow comes to the front and beats like a heart",
    },
  },
  {
    id: "puntos",
    name: "C · Tres puntos",
    idea: "Three dots, the chat's own sign. The face is a conversation, not a pair of eyes.",
    states: {
      idle: "three dots float loose, each on its own",
      working: "the dots line up and ripple, like someone writing",
      asks: "the dots knock twice together: your turn",
    },
  },
  {
    id: "trazo",
    name: "D · Trazo",
    idea: "One expressive line, no eyes. It says everything with its shape.",
    states: {
      idle: "a quiet smile that breathes",
      working: "the line becomes a travelling wave",
      asks: "the line closes into an “o” that calls you",
    },
  },
  {
    id: "moa",
    name: "E · Moa",
    idea: "The moa is a bird: seen in profile, one round eye and a beak. Moves like a bird, not like a screen.",
    states: {
      idle: "slow bob; a sideways blink, like a bird's third eyelid",
      working: "pecks at its work",
      asks: "cocks its head and looks straight at you",
    },
  },
];

const STATES = ["idle", "working", "asks"];
const STATE_LABEL = { idle: "At rest", working: "Working", asks: "Waiting on you" };

// A small deterministic hash so each owner gets its own phase and a column of
// faces never moves in unison.
function seedOf(key) {
  let h = 2166136261;
  for (const ch of String(key)) h = Math.imul(h ^ ch.charCodeAt(0), 16777619);
  return (h >>> 0) % 10000;
}

let uidSeq = 0;

export function IdFace({ dir, shape = "circle", color = "peach", tone = "deep", state = "idle", size = 32, seedKey }) {
  const uid = useMemo(() => `fi${++uidSeq}`, []);
  const d = SHAPE_PATHS[shape] || SHAPE_PATHS.circle;
  const [cx, cy0] = EYE_CENTER[shape] || EYE_CENTER.circle;
  const cy = cy0 + 1;
  const seed = seedOf(seedKey ?? `${shape}:${color}`);
  const body = faceBodyColor(color, tone);
  const style = {
    width: `${size}px`,
    height: `${size}px`,
    "--fi-body": body,
    "--fi-at": `-${(seed % 7000) / 1000}s`,
    "--fi-cx": `${cx}px`,
    "--fi-cy": `${cy}px`,
  };
  const small = size <= 20;
  return (
    <span class={`fi fi-${dir} is-${state}${small ? " is-small" : ""}`} style={style} aria-hidden="true">
      <svg viewBox="0 0 32 32" width={size} height={size}>
        <defs>
          <clipPath id={`${uid}b`}><path d={d} /></clipPath>
        </defs>
        <g class="fi-head">
          <path class="fi-body" d={d} />
          <g clip-path={`url(#${uid}b)`}>
            {dir === "visor" && <Visor uid={uid} cx={cx} cy={cy} state={state} small={small} />}
            {dir === "nucleo" && <Nucleo uid={uid} cx={cx} cy={cy} state={state} />}
            {dir === "puntos" && <Puntos cx={cx} cy={cy} state={state} small={small} />}
            {dir === "trazo" && <Trazo uid={uid} cx={cx} cy={cy} state={state} small={small} />}
            {dir === "moa" && <Moa uid={uid} cx={cx} cy={cy} state={state} small={small} />}
          </g>
        </g>
      </svg>
    </span>
  );
}

/* A · Visor — a slot across the face; the light inside is the attention. */
function Visor({ uid, cx, cy, small }) {
  // A band, square-ended, not a pill: with a round knob in a capsule it read
  // as a toggle switch.
  const w = 23;
  const h = small ? 7.4 : 6.2;
  return (
    <g>
      <defs>
        <radialGradient id={`${uid}l`}>
          <stop offset="0" stop-color="#fff" stop-opacity="1" />
          <stop offset="0.45" stop-color="#fff" stop-opacity="0.85" />
          <stop offset="1" stop-color="#fff" stop-opacity="0" />
        </radialGradient>
      </defs>
      <rect class="fi-v-slot" x={cx - w / 2} y={cy - h / 2} width={w} height={h} rx="1.3" />
      <clipPath id={`${uid}v`}>
        <rect x={cx - w / 2} y={cy - h / 2} width={w} height={h} rx="1.3" />
      </clipPath>
      <g clip-path={`url(#${uid}v)`}>
        <ellipse class="fi-v-trail" cx={cx} cy={cy} rx="6" ry={h * 0.6} fill={`url(#${uid}l)`} />
        <ellipse class="fi-v-light" cx={cx} cy={cy} rx="4.2" ry={h * 0.62} fill={`url(#${uid}l)`} />
      </g>
    </g>
  );
}

/* B · Núcleo — a light under the skin. */
function Nucleo({ uid, cx, cy }) {
  return (
    <g>
      <defs>
        <radialGradient id={`${uid}g`}>
          <stop offset="0" stop-color="#fff" stop-opacity="0.95" />
          <stop offset="0.35" stop-color="#fff" stop-opacity="0.55" />
          <stop offset="1" stop-color="#fff" stop-opacity="0" />
        </radialGradient>
      </defs>
      <g class="fi-n-orbit fi-n-o2"><g class="fi-n-pos"><circle class="fi-n-tail" cx={cx} cy={cy} r="1.6" /></g></g>
      <g class="fi-n-orbit fi-n-o1"><g class="fi-n-pos"><circle class="fi-n-tail" cx={cx} cy={cy} r="2.1" /></g></g>
      <g class="fi-n-orbit">
        <g class="fi-n-pos">
          <circle class="fi-n-halo" cx={cx} cy={cy} r="9" fill={`url(#${uid}g)`} />
          <circle class="fi-n-core" cx={cx} cy={cy} r="2.8" />
        </g>
      </g>
    </g>
  );
}

/* C · Tres puntos — the chat's own sign. */
function Puntos({ cx, cy, small }) {
  const r = small ? 2.6 : 2.2;
  const gap = small ? 6.4 : 5.6;
  return (
    <g class="fi-p">
      {[-1, 0, 1].map((i) => (
        <g class={`fi-p-slot fi-p-${i + 1}`} key={i}>
          <circle class="fi-p-dot" cx={cx + i * gap} cy={cy} r={r} />
        </g>
      ))}
    </g>
  );
}

/* D · Trazo — one line. SMIL for the smile (Safari animates `d` only that
   way); the wave is a long sine slid under a fading window. */
function Trazo({ uid, cx, cy, state, small }) {
  const sw = small ? 3.6 : 2.8;
  const y = cy + 1.5;
  if (state === "asks") {
    return <circle class="fi-t-ring" cx={cx} cy={y} r="3.8" stroke-width={sw} />;
  }
  if (state === "working") {
    const L = 7; // wavelength
    const a = small ? 2 : 1.8;
    let p = `M${cx - 21} ${y}`;
    for (let x = cx - 21; x < cx + 21; x += L) {
      p += ` q${L / 4} ${-a * 1.3} ${L / 2} 0 q${L / 4} ${a * 1.3} ${L / 2} 0`;
    }
    return (
      <g>
        <defs>
          <linearGradient id={`${uid}f`} x1="0" x2="1">
            <stop offset="0" stop-color="#fff" stop-opacity="0" />
            <stop offset="0.25" stop-color="#fff" stop-opacity="1" />
            <stop offset="0.75" stop-color="#fff" stop-opacity="1" />
            <stop offset="1" stop-color="#fff" stop-opacity="0" />
          </linearGradient>
          <mask id={`${uid}m`} maskUnits="userSpaceOnUse" x="0" y="0" width="32" height="32">
            <rect x={cx - 10} y={y - 6} width="20" height="12" fill={`url(#${uid}f)`} />
          </mask>
        </defs>
        <g mask={`url(#${uid}m)`}>
          <path class="fi-t-wave" d={p} stroke-width={sw} />
        </g>
      </g>
    );
  }
  const a = `M${cx - 6.5} ${y - 0.6} Q${cx} ${y + 4.2} ${cx + 6.5} ${y - 0.6}`;
  const b = `M${cx - 6.5} ${y} Q${cx} ${y + 2.6} ${cx + 6.5} ${y}`;
  return (
    <path class="fi-t-line" d={a} stroke-width={sw}>
      <animate attributeName="d" dur="5.6s" repeatCount="indefinite" values={`${a};${b};${a}`}
        calcMode="spline" keySplines="0.45 0 0.55 1;0.45 0 0.55 1" />
    </path>
  );
}

/* E · Moa — a bird in profile: one round eye, a beak, bird manners. */
function Moa({ uid, cx, cy, small }) {
  const ex = cx + 3.2;
  const ey = cy - 1.8;
  const er = small ? 4.6 : 4.1;
  const pr = small ? 2.3 : 1.9;
  return (
    <g>
      <path class="fi-m-beak" d={`M${cx + 9} ${cy + 0.6} L${cx + 16} ${cy + 2.6} L${cx + 9} ${cy + 4.6} Z`} />
      <clipPath id={`${uid}e`}><circle cx={ex} cy={ey} r={er} /></clipPath>
      <g class="fi-m-eye">
        <circle class="fi-m-white" cx={ex} cy={ey} r={er} />
        <circle class="fi-m-pupil" cx={ex} cy={ey} r={pr} />
        <circle class="fi-m-glint" cx={ex - 0.9} cy={ey - 1.1} r={small ? 0 : 0.7} />
        <g clip-path={`url(#${uid}e)`}>
          <rect class="fi-m-lid" x={ex - er} y={ey - er} width={er * 2} height={er * 2} />
        </g>
      </g>
    </g>
  );
}

/* ── The page ──────────────────────────────────────────────────────────── */

// One owner stands for every direction, so the only thing that changes from
// section to section is the gaze. The roster is only for the phone grid.
const HERO = { name: "Moa", shape: "squircle", color: "azure", tone: "deep", key: "moa" };
const ROSTER = [
  { name: "Autowow", shape: "blob", color: "mint", tone: "deep", key: "autowow", state: "working" },
  { name: "Glitxapp", shape: "hexagon", color: "mauve", tone: "deep", key: "glitxapp", state: "asks" },
  { name: "Infra", shape: "shield", color: "sage", tone: "dark", key: "infra", state: "idle" },
  { name: "Moa", shape: "squircle", color: "azure", tone: "deep", key: "moa", state: "working" },
  { name: "Quolli", shape: "circle", color: "rose", tone: "deep", key: "quolli", state: "idle" },
  { name: "Winerim", shape: "drop", color: "lilac", tone: "pale", key: "winerim", state: "working" },
];

const ROW_TEXT = {
  idle: null,
  working: <span class="fi-lead is-working">3 working</span>,
  asks: <span class="fi-lead is-asks">1 waiting on you</span>,
};

function Face({ dir, o, state, size }) {
  if (dir === "today") {
    return <OwnerFace shape={o.shape} color={o.color} tone={o.tone} seedKey={o.key} state={state} size={size} />;
  }
  return <IdFace dir={dir} shape={o.shape} color={o.color} tone={o.tone} seedKey={o.key} state={state} size={size} />;
}

function Section({ d }) {
  const dir = d.id;
  return (
    <section class="fi-sec" data-dir={dir}>
      <header class="fi-sec-head">
        <h2>{d.name}</h2>
        <p>{d.idea}</p>
      </header>
      <div class="fi-hero">
        {STATES.map((st) => (
          <figure class="fi-hero-cell" key={st}>
            <Face dir={dir} o={HERO} state={st} size={112} />
            <figcaption>
              <b class={`is-${st}`}>{STATE_LABEL[st]}</b>
              <span>{d.states?.[st]}</span>
            </figcaption>
          </figure>
        ))}
      </div>
      <div class="fi-real">
        <div class="fi-real-col">
          <h3>Sidebar row · 32</h3>
          {STATES.map((st) => (
            <div class="fi-row" key={st}>
              <Face dir={dir} o={HERO} state={st} size={32} />
              <span class="fi-row-name">{HERO.name}</span>
              {ROW_TEXT[st]}
            </div>
          ))}
        </div>
        <div class="fi-real-col">
          <h3>Status line · 14 &nbsp;/&nbsp; phone title · 20</h3>
          {STATES.map((st) => (
            <div class="fi-status" key={st}>
              <span class="fi-chip"><Face dir={dir} o={HERO} state={st} size={14} />{HERO.name}</span>
              <span class="fi-chip is-20"><Face dir={dir} o={HERO} state={st} size={20} />{HERO.name}</span>
              <span class="fi-status-tx">{st === "idle" ? "" : st === "working" ? "working" : "waiting on you"}</span>
            </div>
          ))}
        </div>
        <div class="fi-real-col">
          <h3>Phone empty state · 40</h3>
          <div class="fi-grid">
            {ROSTER.map((o) => (
              <div class="fi-tile" key={o.key}>
                <Face dir={dir} o={o} state={o.state} size={40} />
                <span>{o.name}</span>
              </div>
            ))}
          </div>
        </div>
      </div>
    </section>
  );
}

const TODAY = {
  id: "today",
  name: "Today · Mirada (the Grok look)",
  idea: "Two vertical white capsules, squash blink, the pair sliding in saccades, narrowed when working and wide at you.",
  states: { idle: "looks around, blinks", working: "narrowed, looking low and aside", asks: "wide open, straight at you" },
};

export function OwnerFacesIdentityLab() {
  const q = new URLSearchParams(location.search);
  const only = q.get("dir");
  const all = [TODAY, ...DIRECTIONS];
  const list = only ? all.filter((d) => d.id === only) : all;
  return (
    <div class={`fi-lab${only ? " is-solo" : ""}`}>
      {!only && (
        <header class="fi-lab-head">
          <h1>Owner faces · a gaze of our own</h1>
          <p>
            What makes Mirada read as Grok is the gaze, not the outline: two vertical white capsules,
            a squash blink, the pair sliding in saccades, and mood said by resizing them. None of the
            five directions uses any of the four. Same owner, same shape and colour in every section;
            only the gaze changes.
          </p>
          <nav class="fi-nav">
            {all.map((d) => <a key={d.id} href={`?view=faces-id&dir=${d.id}`}>{d.name}</a>)}
          </nav>
        </header>
      )}
      {list.map((d) => <Section key={d.id} d={d} />)}
    </div>
  );
}
