import { useMemo } from "preact/hooks";
import { EYE_CENTER, SHAPE_PATHS, bodyOklch } from "../components/Owners/avatar-identity.js";
import { OwnerFace } from "../components/Owners/OwnerFace.jsx";
import "./owner-faces-identity.css";

/* Owner faces · identity (?view=faces-id). Lab only: nothing here reaches the
   product bundle.

   Why Mirada reads as Grok's bot: the gaze, not the outline — two vertical
   white capsules, a blink that squashes them, the pair sliding in saccades,
   and mood said by narrowing or widening them. None of that is used here.

   Second round. The first five (visor, core, dots, stroke, bird) were turned
   down as ugly; the bar now is product quality — charm, simplicity, beauty —
   so there are three proposals, each with its own material:
     Canica  — a glossy ball with ink-dot eyes that moves like a physical toy;
     Aura    — living light, calm eyes that close to concentrate;
     Lente   — dark glass with luminous ring eyes whose pupils do the looking.
   Drawn with CSS keyframes and SVG gradients to judge the motion; the chosen
   one would be rebuilt on faceMotion's compositor-friendly scheduler. */

export const PROPOSALS = [
  {
    id: "canica",
    name: "1 · Canica",
    idea: "A glossy ball with a light of its own and two ink eyes set low. It moves like a physical toy: it rolls to look, reads when it works, hops when it needs you.",
    states: {
      idle: "looks around by rolling; now and then a contented squint",
      working: "eyes down, reading line after line",
      asks: "faces you, eyes bright, a little hop",
    },
  },
  {
    id: "aura",
    name: "2 · Aura",
    idea: "Living light in the owner's colours, with a calm face. The colour does the work; the eyes say whether it needs you.",
    states: {
      idle: "the colours drift slowly; the eyes rest open",
      working: "eyes closed in concentration, the light swirls",
      asks: "eyes open wide at you; the light ripples out",
    },
  },
  {
    id: "lente",
    name: "3 · Lente",
    idea: "Dark glass lit from the edge in the owner's colour. The eyes are rings; a pupil inside each one does the looking.",
    states: {
      idle: "the pupils wander inside the rings",
      working: "the rings turn into two small spinners",
      asks: "the pupils dilate and centre on you; the rings brighten",
    },
  },
];

const STATES = ["idle", "working", "asks"];
const STATE_LABEL = { idle: "At rest", working: "Working", asks: "Waiting on you" };

function seedOf(key) {
  let h = 2166136261;
  for (const ch of String(key)) h = Math.imul(h ^ ch.charCodeAt(0), 16777619);
  return (h >>> 0) % 10000;
}

const ok = (l, c, h, a = 1) => `oklch(${Math.max(0, Math.min(1, l)).toFixed(3)} ${Math.max(0, c).toFixed(3)} ${h}${a < 1 ? ` / ${a}` : ""})`;

let uidSeq = 0;

export function FaceV2({ dir, shape = "circle", color = "peach", tone = "deep", state = "idle", size = 32, seedKey }) {
  const uid = useMemo(() => `fv${++uidSeq}`, []);
  const d = SHAPE_PATHS[shape] || SHAPE_PATHS.circle;
  const [cx, cy0] = EYE_CENTER[shape] || EYE_CENTER.circle;
  const seed = seedOf(seedKey ?? `${shape}:${color}`);
  const [l, c, h] = bodyOklch(color, tone);
  const small = size <= 20;
  const style = {
    width: `${size}px`,
    height: `${size}px`,
    "--fv-at": `-${(seed % 9000) / 1000}s`,
    "--fv-cx": `${cx}px`,
    "--fv-cy": `${cy0}px`,
  };
  const Body = { canica: Canica, aura: Aura, lente: Lente }[dir];
  return (
    <span class={`fv fv-${dir} is-${state}${small ? " is-small" : ""}${size >= 64 ? " is-large" : ""}`} style={style} aria-hidden="true">
      <svg viewBox="0 0 32 32" width={size} height={size}>
        <Body uid={uid} d={d} cx={cx} cy={cy0} l={l} c={c} h={h} tone={tone} small={small} size={size} />
      </svg>
    </span>
  );
}

/* ── 1 · Canica ───────────────────────────────────────────────────────────
   The light is fixed (top-left) while the face moves under it, which is what
   makes the ball read as rolling rather than as eyes sliding on a sticker. */
function Canica({ uid, d, cx, cy, l, c, h, small, size }) {
  const ink = ok(0.23, 0.045, h);
  const er = small ? 3.1 : 2.65;
  const gap = small ? 5.4 : 5;
  const ey = cy + 2.2;
  const eye = (side) => (
    <g class={`fv-c-eye fv-c-eye-${side < 0 ? "l" : "r"}`}>
      <g class="fv-c-open">
        <circle cx={cx + side * gap} cy={ey} r={er} fill={ink} />
        {!small && <circle class="fv-c-glint" cx={cx + side * gap - 0.8} cy={ey - 0.85} r="0.78" fill="#fff" />}
      </g>
      <path class="fv-c-squint" d={`M${cx + side * gap - er} ${ey + 0.3} q${er} ${-er * 1.3} ${er * 2} 0`}
        fill="none" stroke={ink} stroke-width={small ? 1.9 : 1.5} stroke-linecap="round" />
    </g>
  );
  return (
    <g>
      <defs>
        <linearGradient id={`${uid}b`} x1="0.2" y1="0" x2="0.6" y2="1">
          <stop offset="0" stop-color={ok(l + 0.1, c * 0.95, h - 6)} />
          <stop offset="0.55" stop-color={ok(l, c, h)} />
          <stop offset="1" stop-color={ok(l - 0.13, c * 1.05, h + 8)} />
        </linearGradient>
        <radialGradient id={`${uid}s`} cx="0.34" cy="0.2" r="0.5">
          <stop offset="0" stop-color="#fff" stop-opacity="0.7" />
          <stop offset="0.4" stop-color="#fff" stop-opacity="0.18" />
          <stop offset="1" stop-color="#fff" stop-opacity="0" />
        </radialGradient>
        <radialGradient id={`${uid}r`} cx="0.62" cy="1.05" r="0.55">
          <stop offset="0" stop-color={ok(l + 0.16, c * 0.7, h + 25)} stop-opacity="0.55" />
          <stop offset="1" stop-color={ok(l + 0.16, c * 0.7, h + 25)} stop-opacity="0" />
        </radialGradient>
        <radialGradient id={`${uid}k`}>
          <stop offset="0" stop-color={ok(0.72, 0.16, h + 10)} stop-opacity="0.55" />
          <stop offset="1" stop-color={ok(0.72, 0.16, h + 10)} stop-opacity="0" />
        </radialGradient>
        <radialGradient id={`${uid}sh`}>
          <stop offset="0" stop-color="#000" stop-opacity="0.5" />
          <stop offset="1" stop-color="#000" stop-opacity="0" />
        </radialGradient>
        <radialGradient id={`${uid}hl`}>
          <stop offset="0" stop-color="#fff" stop-opacity="0.8" />
          <stop offset="1" stop-color="#fff" stop-opacity="0" />
        </radialGradient>
        <clipPath id={`${uid}c`}><path d={d} /></clipPath>
      </defs>
      <ellipse class="fv-c-shadow" cx="16" cy="31.2" rx="10" ry="1.9" fill={`url(#${uid}sh)`} />
      <g class="fv-c-hop">
        <path d={d} fill={`url(#${uid}b)`} />
        <g clip-path={`url(#${uid}c)`}>
          <path d={d} fill={`url(#${uid}r)`} />
          <g class="fv-c-face">
            {size >= 28 && (
              <g class="fv-c-cheeks">
                <ellipse cx={cx - gap - 2.6} cy={ey + 3.2} rx="2" ry="1.15" fill={`url(#${uid}k)`} />
                <ellipse cx={cx + gap + 2.6} cy={ey + 3.2} rx="2" ry="1.15" fill={`url(#${uid}k)`} />
              </g>
            )}
            {eye(-1)}
            {eye(1)}
          </g>
          <path d={d} fill={`url(#${uid}s)`} />
          {size >= 28 && (
            <ellipse cx="10.4" cy="7.4" rx="3.4" ry="1.7" transform="rotate(-32 10.4 7.4)" fill={`url(#${uid}hl)`} />
          )}
        </g>
      </g>
    </g>
  );
}

/* ── 2 · Aura ─────────────────────────────────────────────────────────────
   Three soft lights in neighbouring hues move inside the outline; glass on
   top. The eyes are small, round and white: open, closed, or wide. */
function Aura({ uid, d, cx, cy, l, c, h, small }) {
  const cc = Math.max(c, 0.12);
  const blob = (id, hue, dl) => (
    <radialGradient id={`${uid}${id}`}>
      <stop offset="0" stop-color={ok(l + dl, cc + 0.05, hue)} stop-opacity="1" />
      <stop offset="0.5" stop-color={ok(l + dl, cc + 0.04, hue)} stop-opacity="0.55" />
      <stop offset="1" stop-color={ok(l + dl, cc + 0.03, hue)} stop-opacity="0" />
    </radialGradient>
  );
  const er = small ? 2.5 : 2.05;
  const gap = small ? 5 : 4.5;
  const ey = cy + 1.4;
  const eye = (side) => (
    <g class="fv-a-eye">
      <circle class="fv-a-open" cx={cx + side * gap} cy={ey} r={er} fill="#fff" />
      <path class="fv-a-shut" d={`M${cx + side * gap - er * 1.05} ${ey - 0.4} q${er * 1.05} ${er * 1.2} ${er * 2.1} 0`}
        fill="none" stroke="#fff" stroke-width={small ? 1.9 : 1.45} stroke-linecap="round" />
    </g>
  );
  return (
    <g>
      <defs>
        {blob("1", h, 0.1)}
        {blob("2", (h + 48) % 360, 0.06)}
        {blob("3", (h + 322) % 360, 0.02)}
        <linearGradient id={`${uid}g`} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0" stop-color="#fff" stop-opacity="0.38" />
          <stop offset="0.42" stop-color="#fff" stop-opacity="0.04" />
          <stop offset="1" stop-color="#fff" stop-opacity="0" />
        </linearGradient>
        <radialGradient id={`${uid}h`}>
          <stop offset="0.55" stop-color={ok(l + 0.12, cc, h)} stop-opacity="0.55" />
          <stop offset="1" stop-color={ok(l + 0.12, cc, h)} stop-opacity="0" />
        </radialGradient>
        <clipPath id={`${uid}c`}><path d={d} /></clipPath>
      </defs>
      <circle class="fv-a-halo" cx="16" cy="16" r="17" fill={`url(#${uid}h)`} />
      <path d={d} fill={ok(l - 0.14, cc, h)} />
      <g clip-path={`url(#${uid}c)`}>
        <g class="fv-a-swirl">
          <circle class="fv-a-b1" cx="10" cy="10" r="14" fill={`url(#${uid}1)`} />
          <circle class="fv-a-b2" cx="23" cy="13" r="13" fill={`url(#${uid}2)`} />
          <circle class="fv-a-b3" cx="15" cy="25" r="13" fill={`url(#${uid}3)`} />
        </g>
        <path d={d} fill={`url(#${uid}g)`} />
        <g class="fv-a-face">
          {eye(-1)}
          {eye(1)}
        </g>
      </g>
      <path d={d} fill="none" stroke="#fff" stroke-opacity="0.16" stroke-width="0.6" />
    </g>
  );
}

/* ── 3 · Lente ────────────────────────────────────────────────────────────
   The ring is the eye and it never changes shape; only the pupil moves. */
function Lente({ uid, d, cx, cy, h, small }) {
  const edge = ok(0.8, 0.14, h);
  const lit = ok(0.93, 0.07, h);
  const rr = small ? 3.7 : 3.2;
  const sw = small ? 2.1 : 1.55;
  const pr = small ? 1.3 : 1.05;
  const gap = small ? 5.6 : 5.3;
  const ey = cy + 1.2;
  const circ = 2 * Math.PI * rr;
  const eye = (side) => (
    <g class={`fv-l-eye fv-l-eye-${side < 0 ? "l" : "r"}`}>
      <circle class="fv-l-ring" cx={cx + side * gap} cy={ey} r={rr} fill={ok(0.16, 0.02, h)} stroke={lit} stroke-width={sw} />
      <circle class="fv-l-spin" cx={cx + side * gap} cy={ey} r={rr} fill="none" stroke={lit} stroke-width={sw}
        stroke-linecap="round" stroke-dasharray={`${circ * 0.62} ${circ}`} />
      <circle class="fv-l-pupil" cx={cx + side * gap} cy={ey} r={pr} fill={lit} />
    </g>
  );
  return (
    <g>
      <defs>
        <linearGradient id={`${uid}b`} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0" stop-color={ok(0.34, 0.035, h)} />
          <stop offset="1" stop-color={ok(0.2, 0.025, h)} />
        </linearGradient>
        <linearGradient id={`${uid}e`} x1="0" y1="0" x2="0.4" y2="1">
          <stop offset="0" stop-color={edge} stop-opacity="1" />
          <stop offset="0.6" stop-color={edge} stop-opacity="0.35" />
          <stop offset="1" stop-color={edge} stop-opacity="0.7" />
        </linearGradient>
        <radialGradient id={`${uid}g`} cx="0.5" cy="0.05" r="0.7">
          <stop offset="0" stop-color={edge} stop-opacity="0.35" />
          <stop offset="1" stop-color={edge} stop-opacity="0" />
        </radialGradient>
        <clipPath id={`${uid}c`}><path d={d} /></clipPath>
      </defs>
      <path class="fv-l-body" d={d} fill={`url(#${uid}b)`} />
      <g clip-path={`url(#${uid}c)`}>
        <path d={d} fill={`url(#${uid}g)`} />
      </g>
      <path d={d} fill="none" stroke={`url(#${uid}e)`} stroke-width={small ? 1.6 : 1.1} />
      <g class="fv-l-face">
        {eye(-1)}
        {eye(1)}
      </g>
    </g>
  );
}

/* ── The page ──────────────────────────────────────────────────────────── */

const HERO = { name: "Moa", shape: "circle", color: "azure", tone: "deep", key: "moa" };
const ROSTER = [
  { name: "Autowow", shape: "circle", color: "mint", tone: "deep", key: "autowow", state: "working" },
  { name: "Glitxapp", shape: "squircle", color: "mauve", tone: "deep", key: "glitxapp", state: "asks" },
  { name: "Infra", shape: "hexagon", color: "sage", tone: "dark", key: "infra", state: "idle" },
  { name: "Moa", shape: "circle", color: "azure", tone: "deep", key: "moa", state: "working" },
  { name: "Quolli", shape: "blob", color: "rose", tone: "deep", key: "quolli", state: "idle" },
  { name: "Winerim", shape: "drop", color: "peach", tone: "deep", key: "winerim", state: "working" },
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
  return <FaceV2 dir={dir} shape={o.shape} color={o.color} tone={o.tone} seedKey={o.key} state={state} size={size} />;
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
  idea: "Two vertical white capsules, a squash blink, the pair sliding in saccades, narrowed when working and wide at you.",
  states: { idle: "looks around, blinks", working: "narrowed, looking low and aside", asks: "wide open, straight at you" },
};

export function OwnerFacesIdentityLab() {
  const q = new URLSearchParams(location.search);
  const only = q.get("dir");
  const all = [TODAY, ...PROPOSALS];
  const list = only ? all.filter((d) => d.id === only) : all;
  return (
    <div class={`fi-lab${only ? " is-solo" : ""}`}>
      {!only && (
        <header class="fi-lab-head">
          <h1>Owner faces · a gaze of our own</h1>
          <p>
            What makes Mirada read as Grok is the gaze: two vertical white capsules, a squash blink,
            saccades, and mood said by resizing them. Three proposals that use none of it, each with its
            own material. Same owner in every hero; the grid shows other shapes and colours.
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
