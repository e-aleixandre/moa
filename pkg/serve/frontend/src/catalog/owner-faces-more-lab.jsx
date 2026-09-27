import { useState } from "preact/hooks";
import {
  AVATAR_SHAPES, EYE_CENTER, SHAPE_PATHS, avatarColor, defaultAvatar,
} from "../components/Owners/avatar-identity.js";
import { OwnerFace } from "../components/Owners/OwnerFace.jsx";
import "./owner-faces-more-lab.css";

/* Owner faces, more of them (?view=owner-faces-more). Lab only: nothing here
   is imported by the product.

   The problem: 7 owners and the faces already repeat (two mauve owners, one
   pair of eyes for everyone). Three directions for many more faces that stay
   apart at 36–48px in the list and at 20px in the chip, without touching the
   one thing that carries state: the eyes. Every face below is the product's
   own OwnerFace (Mirada eyes, pinned so the page is still); the lab only adds
   what each direction draws around them. */

// ── Geometry shared by the directions ───────────────────────────────────

// Where a trait attaches on each outline, in the 32×32 box: the top of the
// head, the two shoulders (ears, horns) and the two sides at eye height (elf
// ears). Anchors sit ON the contour; traits are drawn BEHIND the body with
// their root pushed inwards, so they grow out of the silhouette on any shape.
const ANCHORS = {
  circle: { top: [16, 1.5], sh: [[5.8, 5.8], [26.2, 5.8]], side: [[1.8, 16], [30.2, 16]] },
  squircle: { top: [16, 1.5], sh: [[5.4, 5.4], [26.6, 5.4]], side: [[1.6, 16], [30.4, 16]] },
  blob: { top: [17, 1.9], sh: [[6.2, 6.8], [27.2, 6.4]], side: [[3.4, 14], [30.2, 13]] },
  hexagon: { top: [16, 1.5], sh: [[6.5, 7], [25.5, 7]], side: [[3.4, 15], [28.6, 15]] },
  drop: { top: [16, 1.8], sh: [[9.6, 8.6], [22.4, 8.6]], side: [[4.4, 17.5], [27.6, 17.5]] },
  pill: { top: [16, 6], sh: [[4.6, 9], [27.4, 9]], side: [[1.6, 16], [30.4, 16]] },
  triangle: { top: [16, 2.2], sh: [[10.8, 10], [21.2, 10]], side: [[6.4, 17], [25.6, 17]] },
  cloud: { top: [15.8, 4.2], sh: [[9.4, 8.2], [22.6, 7.4]], side: [[3, 20.5], [30, 19.5]] },
};

// Outward angle of an anchor, in degrees from "up", measured from the box's
// centre: what a cat ear or an elf ear points along.
const outward = ([x, y]) => (Math.atan2(x - 16, -(y - 16)) * 180) / Math.PI;
const clampDeg = (d, lim) => Math.max(-lim, Math.min(lim, d));

// ── A · Trait: a third axis on the silhouette ───────────────────────────

const TRAITS = [
  { id: "none", label: "None" },
  { id: "antenna", label: "Antenna" },
  { id: "feelers", label: "Feelers" },
  { id: "cat", label: "Cat ears" },
  { id: "bear", label: "Round ears" },
  { id: "elf", label: "Side ears" },
  { id: "sprout", label: "Sprout" },
  { id: "tuft", label: "Tuft" },
];

function Trait({ id, shape }) {
  const a = ANCHORS[shape] || ANCHORS.circle;
  const [tx, ty] = a.top;
  if (id === "antenna") {
    return (
      <g>
        <path d={`M${tx} ${ty + 4}L${tx + 1.2} ${ty - 4}`} class="ofm-stalk" />
        <circle cx={tx + 1.3} cy={ty - 5.2} r="2.5" />
      </g>
    );
  }
  if (id === "feelers") {
    return (
      <g>
        <path d={`M${tx - 2.4} ${ty + 3.5}Q${tx - 3} ${ty - 1.5} ${tx - 6} ${ty - 3.4}`} class="ofm-stalk" />
        <path d={`M${tx + 2.4} ${ty + 3.5}Q${tx + 3} ${ty - 1.5} ${tx + 6} ${ty - 3.4}`} class="ofm-stalk" />
        <circle cx={tx - 6.4} cy={ty - 3.8} r="2" />
        <circle cx={tx + 6.4} cy={ty - 3.8} r="2" />
      </g>
    );
  }
  if (id === "cat" || id === "bear") {
    return (
      <g>
        {a.sh.map((p, i) => {
          const deg = clampDeg(outward(p), 34);
          if (id === "bear") {
            const rad = (deg * Math.PI) / 180;
            return <circle key={i} cx={p[0] + Math.sin(rad) * 1.2} cy={p[1] - Math.cos(rad) * 1.2} r="4.4" />;
          }
          return (
            <path
              key={i}
              class="ofm-join"
              transform={`translate(${p[0]} ${p[1]}) rotate(${deg})`}
              d="M-4 3.2L0 -6.6L4 3.2Z"
            />
          );
        })}
      </g>
    );
  }
  if (id === "elf") {
    return (
      <g>
        {a.side.map((p, i) => (
          <path
            key={i}
            class="ofm-join"
            transform={`translate(${p[0]} ${p[1] - 1}) rotate(${i ? 62 : -62})`}
            d="M-3.2 3L0 -6.4L3.2 3Z"
          />
        ))}
      </g>
    );
  }
  if (id === "sprout") {
    return (
      <g>
        <path d={`M${tx} ${ty + 3}L${tx} ${ty - 2.2}`} class="ofm-stalk" />
        <path d={`M${tx} ${ty - 2}C${tx - 1} ${ty - 6} ${tx - 5} ${ty - 7.4} ${tx - 7.4} ${ty - 6}C${tx - 6.4} ${ty - 2.6} ${tx - 3} ${ty - 1.4} ${tx} ${ty - 2}Z`} />
        <path d={`M${tx} ${ty - 2}C${tx + 1} ${ty - 6} ${tx + 5} ${ty - 7.4} ${tx + 7.4} ${ty - 6}C${tx + 6.4} ${ty - 2.6} ${tx + 3} ${ty - 1.4} ${tx} ${ty - 2}Z`} />
      </g>
    );
  }
  if (id === "tuft") {
    return (
      <path
        class="ofm-join"
        d={`M${tx - 5} ${ty + 3.5}C${tx - 5.5} ${ty - 2} ${tx - 3} ${ty - 5} ${tx + 0.5} ${ty - 6.5}C${tx - 1} ${ty - 3.5} ${tx - 0.5} ${ty - 2} ${tx + 1} ${ty - 1}C${tx + 1.6} ${ty - 4} ${tx + 4} ${ty - 5.4} ${tx + 6.6} ${ty - 5}C${tx + 4.6} ${ty - 3} ${tx + 4.6} ${ty} ${tx + 5} ${ty + 3.5}Z`}
      />
    );
  }
  return null;
}

// ── B · Mark: a pale patch of the same hue, clear of the eyes ───────────

const MARKS = [
  { id: "none", label: "None" },
  { id: "cap", label: "Cap" },
  { id: "belly", label: "Belly" },
  { id: "mask", label: "Mask" },
  { id: "stripe", label: "Stripe" },
  { id: "cheeks", label: "Cheeks" },
  { id: "spots", label: "Spots" },
];

// Marks are drawn inside the body (clipped to it) and never over the eyes'
// band, except `mask`, which is DARKER than the body and makes the white eyes
// stronger rather than weaker.
function Mark({ id, eyeY }) {
  if (id === "cap") return <rect class="ofm-pale" x="-2" y="-2" width="36" height={eyeY - 5.2} />;
  if (id === "belly") return <ellipse class="ofm-pale" cx="16" cy="31.5" rx="11" ry={31.5 - eyeY - 5} />;
  if (id === "mask") return <rect class="ofm-dark" x="-2" y={eyeY - 4.6} width="36" height="9.2" rx="4.6" />;
  if (id === "stripe") return <rect class="ofm-pale" x="13.6" y="-2" width="4.8" height={eyeY - 3.6} rx="2.4" />;
  if (id === "cheeks") {
    return (
      <g class="ofm-pale">
        <circle cx="6.4" cy={eyeY + 6} r="3.3" />
        <circle cx="25.6" cy={eyeY + 6} r="3.3" />
      </g>
    );
  }
  if (id === "spots") {
    return (
      <g class="ofm-pale">
        <circle cx="7" cy="6.5" r="4.6" />
        <circle cx="24.5" cy="27" r="3.6" />
        <circle cx="26" cy={eyeY - 7.5} r="2" />
      </g>
    );
  }
  return null;
}

// ── C · More shapes and a tone axis ─────────────────────────────────────

// A scalloped outline, sampled rather than hand-written.
function flowerPath() {
  const pts = [];
  for (let i = 0; i < 120; i++) {
    const t = (i / 120) * Math.PI * 2;
    const r = 12.6 + 1.9 * Math.cos(7 * t);
    pts.push(`${(16 + r * Math.sin(t)).toFixed(2)} ${(16.4 - r * Math.cos(t)).toFixed(2)}`);
  }
  return `M${pts.join("L")}Z`;
}

const NEW_SHAPES = {
  flower: flowerPath(),
  ghost: "M16 2.5C23.2 2.5 28 7.8 28 15v13.4q-2-2.8-4 0t-4 0-4 0-4 0-4 0-4 0V15C4 7.8 8.8 2.5 16 2.5Z",
  bean: "M10.4 4.3C14.6 2.8 17.4 6 21.6 5.4 26.8 4.8 30.2 9.4 29.8 15.6 29.4 23.4 23.8 28.6 16 28.6 7.6 28.6 2.4 23.2 2.4 15.2 2.4 9.6 5.8 6 10.4 4.3Z",
  diamond: "M13.9 2.9Q16 .8 18.1 2.9L29.1 13.9Q31.2 16 29.1 18.1L18.1 29.1Q16 31.2 13.9 29.1L2.9 18.1Q.8 16 2.9 13.9Z",
  shield: "M4 5.6Q4 3 6.6 3H25.4Q28 3 28 5.6V15C28 22.4 22.2 27.6 16 30.2 9.8 27.6 4 22.4 4 15Z",
  bell: "M16 2.4C22 2.4 24.6 7.4 24.6 13V18.6C24.6 22.4 28.6 23.8 28.6 26.6 28.6 28.3 27.4 29 25.6 29H6.4C4.6 29 3.4 28.3 3.4 26.6 3.4 23.8 7.4 22.4 7.4 18.6V13C7.4 7.4 10 2.4 16 2.4Z",
};
// Which existing outline's eye position each new shape borrows, so the eyes
// stay exactly the product's.
const EYE_PROXY = { flower: "circle", ghost: "circle", bean: "blob", diamond: "hexagon", shield: "squircle", bell: "drop" };
const C_SHAPES = [...AVATAR_SHAPES, ...Object.keys(NEW_SHAPES)];

// Tone: the same eight hues at three lightnesses. `deep` is today's body
// (faceBodyColor: oklch 0.68 0.12 h). `pale` flips the eyes to ink so they
// keep their contrast. No grey tone: a grey owner would read as the muted,
// parked mark.
const TONES = [
  { id: "deep", label: "Deep (today)", l: 0.68, c: 0.12, ink: false },
  { id: "dark", label: "Dark", l: 0.5, c: 0.11, ink: false },
  { id: "pale", label: "Pale", l: 0.87, c: 0.075, ink: true },
];
const toneOf = (id) => TONES.find((t) => t.id === id) || TONES[0];
const hueOf = (colorId) => avatarColor(colorId).oklch.split(" ")[2];
const bodyColor = (colorId, toneId) => {
  const t = toneOf(toneId);
  return `oklch(${t.l} ${t.c} ${hueOf(colorId)})`;
};

// ── The face ───────────────────────────────────────────────────────────

let clipSeq = 0;

// LabFace draws the body (any outline, any tone), the trait behind it and the
// mark inside it, and lays the product's OwnerFace on top with its own body
// made transparent: what is left of it is exactly today's eyes, by state.
export function LabFace({ av, state = "idle", size = 40, seedKey }) {
  const shape = av.shape;
  const isNew = !!NEW_SHAPES[shape];
  const d = isNew ? NEW_SHAPES[shape] : (SHAPE_PATHS[shape] || SHAPE_PATHS.circle);
  const eyeShape = isNew ? EYE_PROXY[shape] : shape;
  const eyeY = (EYE_CENTER[eyeShape] || EYE_CENTER.circle)[1] + 1;
  const tone = toneOf(av.tone);
  const h = hueOf(av.color);
  const [clip] = useState(() => `ofmc${++clipSeq}`);
  const mark = av.mark && av.mark !== "none";
  return (
    <span
      class={`ofm-face is-${state}${tone.ink ? " is-ink" : ""}`}
      style={{
        width: `${size}px`,
        height: `${size}px`,
        "--ofm-body": bodyColor(av.color, av.tone),
        "--ofm-pale": `oklch(0.9 0.06 ${h})`,
        "--ofm-dark": `oklch(0.42 0.09 ${h})`,
      }}
    >
      <svg class="ofm-svg" viewBox="0 0 32 32" width={size} height={size} aria-hidden="true">
        {mark && (
          <defs>
            <clipPath id={clip}><path d={d} /></clipPath>
          </defs>
        )}
        <g class="ofm-body">
          {av.trait && av.trait !== "none" && <Trait id={av.trait} shape={shape} />}
          <path d={d} />
        </g>
        {mark && (
          <g clip-path={`url(#${clip})`}>
            <Mark id={av.mark} eyeY={eyeY} />
          </g>
        )}
      </svg>
      <span class="ofm-eyes">
        <OwnerFace shape={eyeShape} color={av.color} state={state} size={size} seedKey={seedKey} gaze={[0, 0]} />
      </span>
    </span>
  );
}

// ── Fixtures ───────────────────────────────────────────────────────────

// The seven real owners, with the avatar each one has stored today
// (~/.config/moa/codebases/<key>/owner.json on the owner's machine).
const REAL = [
  { name: "Autowow", key: "d534e87d1ccca947", av: { shape: "triangle", color: "sky" } },
  { name: "Glitxapp", key: "8de80385e0fc079d", av: { shape: "drop", color: "mauve" } },
  { name: "Infra", key: "f98930ec401750fb", av: { shape: "cloud", color: "azure" } },
  { name: "Moa", key: "78aca0d1cbd2fedb", av: { shape: "circle", color: "mint" } },
  { name: "Quolli", key: "b6c28c08c5e87b61", av: { shape: "blob", color: "peach" } },
  { name: "Winerim", key: "dc31637259598fc0", av: { shape: "hexagon", color: "mauve" } },
  { name: "iRead", key: "f774c1b4b42dae80", av: { shape: "pill", color: "rose" } },
];
// Owners that do not exist yet, on today's deterministic default — which is
// where the repeats come from once there are a dozen of them.
const NEW = ["Winerim Web", "Facturas API", "Catas app", "Landing 2026", "Sommelier bot", "Pedidos B2B", "Stock alerts"].map(
  (name) => {
    const key = name.toLowerCase().replace(/\s+/g, "-");
    return { name, key, av: defaultAvatar(key), isNew: true };
  },
);
const ALL = [...REAL, ...NEW];

// A believable morning: every state present, lines as the product words them.
const ROW = {
  Autowow: ["idle", "2 working"],
  Glitxapp: ["working", "Running · 4m"],
  Infra: ["idle", ""],
  Moa: ["asks", "Asks you: ¿desplegamos?"],
  Quolli: ["idle", "1 working · 1 waiting on you"],
  Winerim: ["working", "Running · 12m"],
  iRead: ["saved", ""],
  "Winerim Web": ["idle", "3 working"],
  "Facturas API": ["asks", "Asks you: ¿qué IVA aplico?"],
  "Catas app": ["working", "Running · 1m"],
  "Landing 2026": ["idle", ""],
  "Sommelier bot": ["saved", ""],
  "Pedidos B2B": ["idle", "1 working"],
  "Stock alerts": ["working", "Running · 30m"],
};

// What each direction would draw for every owner. The real seven keep their
// stored shape and colour; the extra field is what their owner would pick
// (left out, they look exactly as they do today). The new ones get the
// suggestion New owner would make: the value no sibling uses yet.
const DIRECTIONS = [
  {
    id: "a",
    title: "A · Trait",
    lead: "A third axis on the silhouette: ears, antennae, a sprout, a tuft. Drawn in the body colour, behind it, far from the eyes.",
    format: '"avatar": { "shape", "color", "trait"? } — trait absent = "none"',
    count: "8 shapes × 8 colours × 8 traits = 512",
    axis: TRAITS.map((t) => ({ id: t.id, label: t.label, av: { shape: "circle", color: "sky", trait: t.id } })),
    extra: {
      Autowow: { trait: "feelers" },
      Glitxapp: { trait: "sprout" },
      Infra: { trait: "antenna" },
      Moa: { trait: "bear" },
      Quolli: { trait: "tuft" },
      Winerim: { trait: "cat" },
      iRead: { trait: "elf" },
      "Winerim Web": { trait: "feelers" },
      "Facturas API": { trait: "bear" },
      "Catas app": { trait: "sprout" },
      "Landing 2026": { trait: "none" },
      "Sommelier bot": { trait: "elf" },
      "Pedidos B2B": { trait: "tuft" },
      "Stock alerts": { trait: "cat" },
    },
  },
  {
    id: "b",
    title: "B · Mark",
    lead: "A patch inside the body: a pale cap, belly, stripe, cheeks or spots of the same hue, or a darker mask behind the eyes.",
    format: '"avatar": { "shape", "color", "mark"? } — mark absent = "none"',
    count: "8 shapes × 8 colours × 7 marks = 448",
    axis: MARKS.map((m) => ({ id: m.id, label: m.label, av: { shape: "circle", color: "sky", mark: m.id } })),
    extra: {
      Autowow: { mark: "belly" },
      Glitxapp: { mark: "cap" },
      Infra: { mark: "none" },
      Moa: { mark: "cheeks" },
      Quolli: { mark: "spots" },
      Winerim: { mark: "mask" },
      iRead: { mark: "stripe" },
      "Winerim Web": { mark: "belly" },
      "Facturas API": { mark: "mask" },
      "Catas app": { mark: "cap" },
      "Landing 2026": { mark: "stripe" },
      "Sommelier bot": { mark: "spots" },
      "Pedidos B2B": { mark: "cheeks" },
      "Stock alerts": { mark: "cap" },
    },
  },
  {
    id: "c",
    title: "C · Shapes + tone",
    lead: "Six more outlines and a lightness axis: each hue as today, darker, or pale with ink eyes. No new hue, so no state colour sneaks in.",
    format: 'new ids appended to AvatarShapes (never to the default pool) + "tone"? — absent = "deep"',
    count: "14 shapes × 8 colours × 3 tones = 336",
    axis: [
      ...Object.keys(NEW_SHAPES).map((s) => ({ id: s, label: s[0].toUpperCase() + s.slice(1), av: { shape: s, color: "sky" } })),
      ...TONES.map((t) => ({ id: `tone-${t.id}`, label: t.label, av: { shape: "circle", color: "mauve", tone: t.id } })),
    ],
    extra: {
      Autowow: { tone: "dark" },
      Glitxapp: { tone: "pale" },
      Infra: {},
      Moa: {},
      Quolli: { tone: "dark" },
      Winerim: {},
      iRead: { tone: "pale" },
      "Winerim Web": { shape: "diamond" },
      "Facturas API": { shape: "shield", tone: "dark" },
      "Catas app": { shape: "flower" },
      "Landing 2026": { shape: "ghost", tone: "pale" },
      "Sommelier bot": { shape: "bell" },
      "Pedidos B2B": { shape: "bean", tone: "dark" },
      "Stock alerts": { shape: "flower", tone: "pale" },
    },
  },
];

const withDir = (dir, o) => ({ ...o, av: { ...o.av, ...(dir ? dir.extra[o.name] : {}) } });

const STATES = [
  { id: "idle", word: "Idle" },
  { id: "working", word: "Working" },
  { id: "asks", word: "Waiting" },
  { id: "saved", word: "Saved" },
];

// ── Pieces of the page ─────────────────────────────────────────────────

function PhoneList({ owners, testid }) {
  return (
    <div class="ofm-phone" data-testid={testid}>
      <div class="ofm-phone-head">Owners</div>
      {owners.map((o) => {
        const [state, line] = ROW[o.name] || ["idle", ""];
        return (
          <div class={`ofm-row is-${state}`} key={o.name}>
            <LabFace av={o.av} state={state} size={40} seedKey={o.key} />
            <span class="ofm-row-text">
              <span class="ofm-row-name">
                {o.name}
                {o.isNew && <span class="ofm-new">new</span>}
              </span>
              {line && <span class="ofm-row-line">{line}</span>}
            </span>
          </div>
        );
      })}
    </div>
  );
}

function Chips({ owners }) {
  return (
    <div class="ofm-chips">
      {owners.map((o) => (
        <span class="ofm-chip" key={o.name}>
          <LabFace av={o.av} state={(ROW[o.name] || ["idle"])[0]} size={20} seedKey={o.key} />
          <span>Owner · {o.name}</span>
        </span>
      ))}
    </div>
  );
}

function StatesGrid({ owners }) {
  return (
    <div class="ofm-states">
      <span />
      {STATES.map((s) => <span class="ofm-states-h" key={s.id}>{s.word}</span>)}
      {owners.map((o) => [
        <span class="ofm-states-n" key={`${o.name}-n`}>{o.name}</span>,
        ...STATES.map((s) => (
          <span class="ofm-states-c" key={`${o.name}-${s.id}`}>
            <LabFace av={o.av} state={s.id} size={36} seedKey={o.key} />
            <LabFace av={o.av} state={s.id} size={20} seedKey={o.key} />
          </span>
        )),
      ])}
    </div>
  );
}

function Axis({ items }) {
  return (
    <div class="ofm-axis">
      {items.map((it) => (
        <span class="ofm-axis-c" key={it.id}>
          <LabFace av={it.av} size={40} seedKey="axis" />
          <LabFace av={it.av} size={20} seedKey="axis" />
          <span class="ofm-axis-l">{it.label}</span>
        </span>
      ))}
    </div>
  );
}

function Today() {
  return (
    <section class="ofm-sec" data-testid="ofm-today">
      <h2 class="ofm-h">Today</h2>
      <p class="ofm-p">
        The seven real owners as stored, and seven new ones on today's default. Glitxapp and Winerim are both mauve;
        the default then gives Winerim Web and Facturas API the same mauve drop as Glitxapp, and Pedidos B2B and Stock
        alerts the same sky hexagon.
      </p>
      <PhoneList owners={ALL} testid="ofm-list-today" />
      <Chips owners={ALL} />
    </section>
  );
}

function Direction({ dir }) {
  const owners = ALL.map((o) => withDir(dir, o));
  // The mauve owners first: the pair the owner flagged, and the two new ones
  // today's default would also draw as a mauve drop.
  const stateOwners = ["Glitxapp", "Winerim", "Winerim Web", "Facturas API", "Pedidos B2B", "Stock alerts"]
    .map((n) => owners.find((o) => o.name === n));
  return (
    <section class="ofm-sec" data-testid={`ofm-dir-${dir.id}`}>
      <h2 class="ofm-h">{dir.title}</h2>
      <p class="ofm-p">{dir.lead}</p>
      <dl class="ofm-facts">
        <dt>Format</dt>
        <dd><code>{dir.format}</code></dd>
        <dt>Faces</dt>
        <dd>{dir.count} <span class="ofm-dim">(today 8 × 8 = 64 selectable, 48 in the default pool)</span></dd>
      </dl>
      <h3 class="ofm-h3">The axis · 40px and 20px</h3>
      <Axis items={dir.axis} />
      <h3 class="ofm-h3">The list · 40px</h3>
      <PhoneList owners={owners} testid={`ofm-list-${dir.id}`} />
      <h3 class="ofm-h3">The chip · 20px</h3>
      <Chips owners={owners} />
      <h3 class="ofm-h3">The four states · 36px and 20px</h3>
      <StatesGrid owners={stateOwners} />
    </section>
  );
}

export function OwnerFacesMoreLab() {
  const initial = typeof location !== "undefined" ? new URLSearchParams(location.search).get("dir") : null;
  const [only, setOnly] = useState(["a", "b", "c", "today"].includes(initial) ? initial : "all");
  const pick = (id) => {
    setOnly(id);
    const u = new URL(location.href);
    if (id === "all") u.searchParams.delete("dir");
    else u.searchParams.set("dir", id);
    history.replaceState(null, "", u);
  };
  const tabs = [{ id: "all", label: "All" }, { id: "today", label: "Today" }, ...DIRECTIONS.map((d) => ({ id: d.id, label: d.title }))];
  return (
    <div class="ofm" data-testid="owner-faces-more">
      <header class="ofm-head">
        <h1>More owner faces</h1>
        <p>Three ways to many more faces that stay apart at a glance. The eyes are the product's, and still the only thing that says state.</p>
      </header>
      <div class="ofm-tabs" role="radiogroup" aria-label="Direction">
        {tabs.map((t) => (
          <button
            type="button"
            key={t.id}
            role="radio"
            aria-checked={only === t.id}
            class={`ofm-tab${only === t.id ? " is-on" : ""}`}
            onClick={() => pick(t.id)}
          >
            {t.label}
          </button>
        ))}
      </div>
      {(only === "all" || only === "today") && <Today />}
      {DIRECTIONS.filter((d) => only === "all" || only === d.id).map((d) => <Direction dir={d} key={d.id} />)}
    </div>
  );
}

