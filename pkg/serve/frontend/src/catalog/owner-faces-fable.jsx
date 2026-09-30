import { useMemo, useRef } from "preact/hooks";
import { AVATAR_COLORS, AVATAR_SHAPES, AVATAR_TONES, eyeStateFor } from "../components/Owners/avatar-identity.js";
import {
  OwnerFace, combineGaze, eyeCenter, faceBodyColor, facePath, poseVars, r3, useFaceMotion,
} from "../components/Owners/OwnerFace.jsx";
import { facePersonality } from "../components/Owners/faceMotion.js";
import "./owner-faces-fable.css";

/* Owner faces · a gaze of our own (?view=faces-fable). Lab only: nothing here
   reaches the product bundle.

   Third round. What the owner loves in Mirada is kept whole: the flat matte
   body in the identity colour, the small white marks, the tiny asymmetry per
   owner, the breath, the drop shadow, and the shared scheduler's unhurried,
   never-metronomic rhythm. What makes it read as Grok's bot is replaced — the
   two vertical white capsules, the squash blink, the pair hopping in saccades
   and mood said by narrowing or widening them. Each proposal changes the eye
   GLYPH and the motion GRAMMAR, nothing else:

     1 · Almendra — pointed leaf eyes that close with a lid; the head turns
                    and tilts to look, and cocks itself when it needs you.
     2 · Serena   — eyes that smile (^ ^) while all is well; it peeks now and
                    then; it opens them only when it needs you. The marks never
                    slide on the body: the head does the looking.
     3 · Cara     — two dots and a small mouth, the Finder's kind of face; the
                    mouth says the mood, the eyes and mouth move in parallax.

   THE OWNER CHOSE SERENA (30/09). Its section carries the full sheet: every
   shape, colour and tone at the four real sizes. Almendra and Cara stay for
   the record. See the integration notes at the end of this file.

   All three run on faceMotion (the product's own scheduler), so a column of
   them behaves exactly as the product would: seeded per owner, asleep between
   events, still offscreen or under reduced motion. */

const STATES = ["idle", "working", "asks", "saved"];
const STATE_LABEL = { idle: "At rest", working: "Working", asks: "Waiting on you", saved: "Saved" };

export const PROPOSALS = [
  {
    id: "almendra",
    name: "1 · Almendra",
    idea: "Two white almonds. A lid blinks them from above, a squint rises from below, and the head turns and tilts to look — the eyes never hop on the body.",
    states: {
      idle: "looks around by turning and tilting its head; lid blinks",
      working: "squinting at its work, head bowed",
      asks: "head cocked at you, eyes wide open",
      saved: "lids down",
    },
  },
  {
    id: "serena",
    name: "2 · Serena — chosen",
    idea: "At rest its eyes are content and shut; it opens them when it has something to look at — its work, or you. The head does most of the looking.",
    states: {
      idle: "eyes content; the head wanders; now and then a peek",
      working: "eyes open on the work, head bowed",
      asks: "eyes open on you; a happy blink now and then",
      saved: "off: two level lines",
    },
  },
  {
    id: "cara",
    name: "3 · Cara",
    idea: "Two dots and a small mouth: a face, not a token. The mouth says the mood; eyes and mouth move in parallax, so the head reads as round.",
    states: {
      idle: "looks around; the lid blinks; a small smile",
      working: "eyes down on its work; the mouth goes flat",
      asks: "straight at you, mouth open in an “o”",
      saved: "eyes shut, still smiling",
    },
  },
];

/* ── The shared frame ────────────────────────────────────────────────────
   Body, breath, scheduler and CSS variables are Mirada's. Each proposal
   supplies its eyes (SVG) and its pose (gaze → custom properties). */

let seq = 0;

export function FaceFable({ dir, shape = "circle", color = "peach", tone = "deep", seedKey, state = "idle", size = 32 }) {
  const D = DIRS[dir];
  const key = seedKey ?? `${shape}:${color}`;
  const p = useMemo(() => facePersonality(key), [key]);
  const eyes = eyeStateFor(state);
  const s = AVATAR_SHAPES.includes(shape) ? shape : "circle";
  const uid = useMemo(() => `ffb${++seq}`, []);
  const ref = useRef(null);

  const pose = (gx, gy) => D.pose(p, s, eyes, ...combineGaze(eyes, gx, gy));
  pose.key = `${dir}:${s}:${eyes}`;
  useFaceMotion(ref, { p, eyes, mode: eyes, follow: false, pinned: false, pose });

  const rest = D.pose(p, s, eyes, ...combineGaze(eyes, 0, 0));
  const small = size <= 24;
  const geo = D.geometry(p, s, small);
  const style = {
    width: `${size}px`,
    height: `${size}px`,
    "--ff-body": faceBodyColor(color, tone),
    "--ff-u": `${size / 32}px`,
    "--of-breath": `${p.breath}ms`,
    "--of-breath-at": `-${p.seed % p.breath}ms`,
    ...geo.vars,
    ...rest,
  };
  const breathes = eyes === "idle";
  return (
    <span
      ref={ref}
      class={`ff ow-av ff-${dir} is-${eyes} is-tone-${tone}${small ? " is-small" : ""}${breathes ? " is-breathe" : ""}`}
      style={style}
      aria-hidden="true"
    >
      <span class="ff-head">
        <svg class="ff-svg" viewBox="0 0 32 32" width={size} height={size}>
          <path class="ff-fill" d={facePath(s)} />
          <D.Eyes p={p} shape={s} small={small} uid={uid} geo={geo} />
        </svg>
      </span>
    </span>
  );
}

/* ── 1 · Almendra ────────────────────────────────────────────────────────
   The eye is a full lens, barely slanted. Its lids are rectangles in the
   body colour inside the lens's clip: the upper one blinks from above, the
   lower one rises into a squint while it works — never a squash. The gaze reuses Mirada's sphere projection (the far eye thins,
   the pair bunches), and adds a turn of the whole head about its base, so a
   glance reads as the head moving rather than as two marks sliding. */
const Almendra = {
  geometry(p, shape, small) {
    const a = small ? 3.4 : 3.15;
    const b = small ? 2.35 : 2.2;
    return { a, b, gap: p.eyeGap + (small ? 0.45 : 0.25), vars: { "--ff-b": b } };
  },
  pose(p, shape, eyes, gx, gy) {
    const v = poseVars(p, shape, gx, gy);
    // The head turns with the glance (about its base). Asking cocks it.
    const rot = eyes === "asks" ? 10 : eyes === "working" ? 2.5 + gx * 3 : gx * 5.5;
    v["--rot"] = r3(rot);
    return v;
  },
  Eyes({ p, shape, uid, geo }) {
    const [cx, cy0] = eyeCenter(shape);
    const cy = cy0 + 1.3;
    const { a, b, gap } = geo;
    const lens = `M${r3(-a)} 0Q0 ${r3(-2 * b)} ${a} 0Q0 ${r3(2 * b)} ${r3(-a)} 0Z`;
    const eye = (side) => {
      const lr = side < 0 ? "l" : "r";
      const id = `${uid}${lr}`;
      return (
        <g class={`ff-gaze ff-gaze-${lr}`}>
          <g transform={`translate(${r3(cx + side * gap)} ${cy}) rotate(${r3(side * -5 + p.tilt * 0.35)})`}>
            {/* The lids' clip is the lens grown a hair, so a shut lid also
                covers the white's anti-aliased edge (a clip on the exact
                outline left a ghost ring at the bottom of every blink). */}
            <clipPath id={id}><path d={lens} transform="scale(1.1)" /></clipPath>
            <path class="ff-white" d={lens} />
            <g clip-path={`url(#${id})`}>
              <rect class="ff-lid" x={r3(-a - 1)} y={r3(-b - 8)} width={r3(2 * a + 2)} height="8" />
              {/* The lower lid: the squint of concentration rises from below. */}
              <rect class="ff-lid-low" x={r3(-a - 1)} y={r3(b)} width={r3(2 * a + 2)} height="8" />
            </g>
            <path class="ff-shut" d={`M${r3(-a + 0.3)} 0Q0 ${r3(b * 1.15)} ${r3(a - 0.3)} 0`} />
          </g>
        </g>
      );
    };
    return <g class="ff-eyes">{eye(-1)}{eye(1)}</g>;
  },
};

/* ── 2 · Serena ──────────────────────────────────────────────────────────
   At rest the eyes are content and shut (ᵕ ᵕ); awake they are two round
   dots. Working and asking are the same open eyes — what differs is where
   the head is: bowed on the work, or lifted at you. The scheduler's blink
   is a PEEK at rest (the dot opens fast, closes slowly) and a happy blink
   while awake (the smile for 150 ms). Saved is two level lines: off. */
const Serena = {
  geometry(p, shape, small) {
    return {
      w: small ? 2.6 : 2.3,
      h: small ? 1.9 : 1.7,
      dr: small ? 2.2 : 2.0,
      gap: p.eyeGap + (small ? 0.5 : 0.3),
      vars: {},
    };
  },
  pose(p, shape, eyes, gx, gy) {
    return {
      "--bx": r3(gx * 1.8),
      "--by": r3(gy * 1.6),
      // Open eyes look; shut ones ride the head (the CSS ignores --ex at rest).
      "--ex": r3(gx * 2.4),
      "--ey": r3(gy * 2.4),
      "--rot": r3(eyes === "asks" ? 0 : gx * 5),
    };
  },
  Eyes({ p, shape, geo }) {
    const [cx, cy0] = eyeCenter(shape);
    const cy = cy0 + 1.2;
    const { w, h, dr, gap } = geo;
    const eye = (side) => (
      <g class="ff-eye" transform={`translate(${r3(cx + side * gap)} ${cy}) rotate(${r3(p.tilt * 0.45 + side * p.skew * 0.3)})`}>
        <path class="ff-arc" d={`M${r3(-w)} ${r3(-h * 0.45)}Q0 ${r3(h * 1.55)} ${w} ${r3(-h * 0.45)}`} />
        <circle class="ff-dot" cx="0" cy="0.2" r={dr} />
        <path class="ff-shut" d={`M${r3(-w)} 0.3L${w} 0.3`} />
      </g>
    );
    return <g class="ff-eyes">{eye(-1)}{eye(1)}</g>;
  },
};

/* ── 3 · Cara ────────────────────────────────────────────────────────────
   Dots with a lid (clipped rectangle, like Almendra's) and a mouth under
   them: a smile at rest, flat while working (the smile scaled about its
   corners) with the eyes down on the work, an "o" while asking. Eyes, mouth and body move by different
   amounts on a glance — parallax — which is what makes a flat disc read as a
   round head turning. */
const Cara = {
  geometry(p, shape, small) {
    const r = small ? 2.25 : 2.05;
    return {
      r,
      gap: p.eyeGap + (small ? 0.55 : 0.4),
      mw: small ? 1.9 : 1.7,
      or: small ? 1.2 : 1.05,
      vars: { "--ff-b": r },
    };
  },
  pose(p, shape, eyes, gx, gy) {
    return {
      "--ex": r3(gx * 2.1), "--ey": r3(gy * 1.5),
      "--mx": r3(gx * 1.25), "--my": r3(gy * 0.9),
      "--bx": r3(gx * 0.45), "--by": r3(gy * 0.3),
    };
  },
  Eyes({ p, shape, uid, geo }) {
    const [cx, cy0] = eyeCenter(shape);
    const cy = cy0 + 0.4;
    const { r, gap, mw, or } = geo;
    const my = cy + 5.2;
    const eye = (side) => {
      const lr = side < 0 ? "l" : "r";
      const id = `${uid}${lr}`;
      return (
        <g transform={`translate(${r3(cx + side * gap)} ${cy})`}>
          <clipPath id={id}><circle r={r3(r + 0.25)} /></clipPath>
          <circle class="ff-white" r={r} />
          <g clip-path={`url(#${id})`}>
            <rect class="ff-lid" x={r3(-r - 1)} y={r3(-r - 8)} width={r3(2 * r + 2)} height="8" />
          </g>
          <path class="ff-shut" d={`M${r3(-r)} 0Q0 ${r3(r * 1.1)} ${r} 0`} />
        </g>
      );
    };
    return (
      <g class="ff-face">
        <g class="ff-eyes">{eye(-1)}{eye(1)}</g>
        <g class="ff-mouth">
          <g transform={`translate(${r3(cx + p.tilt * 0.04)} ${r3(my)}) rotate(${r3(p.tilt * 0.3)})`}>
            <path class="ff-smile" d={`M${r3(-mw)} 0Q0 ${r3(mw * 1.05)} ${mw} 0`} />
            <circle class="ff-o" cy="0.5" r={or} />
          </g>
        </g>
      </g>
    );
  },
};

const DIRS = { almendra: Almendra, serena: Serena, cara: Cara };

/* ── The page ──────────────────────────────────────────────────────────── */

const HERO = { name: "Moa", shape: "circle", color: "azure", tone: "deep", key: "moa" };
const ROSTER = [
  { name: "Autowow", shape: "circle", color: "mint", tone: "deep", key: "autowow", state: "working" },
  { name: "Glitxapp", shape: "squircle", color: "mauve", tone: "deep", key: "glitxapp", state: "asks" },
  { name: "Infra", shape: "hexagon", color: "sage", tone: "dark", key: "infra", state: "idle" },
  { name: "Quolli", shape: "blob", color: "rose", tone: "pale", key: "quolli", state: "idle" },
  { name: "Winerim", shape: "drop", color: "peach", tone: "deep", key: "winerim", state: "working" },
  { name: "Pulse", shape: "pill", color: "lilac", tone: "deep", key: "pulse", state: "saved" },
];

const ROW_TEXT = {
  idle: null,
  saved: null,
  working: <span class="ffl-lead is-working">3 working</span>,
  asks: <span class="ffl-lead is-asks">1 waiting on you</span>,
};
const STATUS_TEXT = { idle: "", saved: "", working: "working", asks: "waiting on you" };

function Face({ dir, o, state, size }) {
  if (dir === "today") {
    return <OwnerFace shape={o.shape} color={o.color} tone={o.tone} seedKey={o.key} state={state} size={size} />;
  }
  return <FaceFable dir={dir} shape={o.shape} color={o.color} tone={o.tone} seedKey={o.key} state={state} size={size} />;
}

function Section({ d }) {
  const dir = d.id;
  return (
    <section class="ffl-sec" data-dir={dir}>
      <header class="ffl-sec-head">
        <h2>{d.name}</h2>
        <p>{d.idea}</p>
      </header>
      <div class="ffl-states">
        <div class="ffl-cols" aria-hidden="true">
          <span />
          <span>Hero · 96</span>
          <span>Sidebar row · 32</span>
          <span>Status line · 14</span>
          <span>Phone title · 20</span>
          <span>Grid tile · 40</span>
        </div>
        {STATES.map((st) => (
          <div class={`ffl-state is-${st}`} key={st}>
            <div class="ffl-state-cap">
              <b class={`is-${st}`}>{STATE_LABEL[st]}</b>
              <span>{d.states?.[st]}</span>
            </div>
            <div class="ffl-cell ffl-hero"><Face dir={dir} o={HERO} state={st} size={96} /></div>
            <div class="ffl-cell">
              <div class="ffl-row">
                <Face dir={dir} o={HERO} state={st} size={32} />
                <span class="ffl-row-name">{HERO.name}</span>
                {ROW_TEXT[st]}
              </div>
            </div>
            <div class="ffl-cell">
              <span class="ffl-chip"><Face dir={dir} o={HERO} state={st} size={14} />{HERO.name}</span>
              <span class="ffl-status-tx">{STATUS_TEXT[st]}</span>
            </div>
            <div class="ffl-cell">
              <span class="ffl-chip is-20"><Face dir={dir} o={HERO} state={st} size={20} />{HERO.name}</span>
            </div>
            <div class="ffl-cell">
              <div class="ffl-tile">
                <Face dir={dir} o={HERO} state={st} size={40} />
                <span>{HERO.name}</span>
              </div>
            </div>
          </div>
        ))}
      </div>
      <div class="ffl-roster">
        <h3>Phone grid · other owners</h3>
        <div class="ffl-grid">
          {ROSTER.map((o) => (
            <div class="ffl-tile" key={o.key}>
              <Face dir={dir} o={o} state={o.state} size={40} />
              <span>{o.name}</span>
            </div>
          ))}
        </div>
      </div>
      {dir === "serena" && <SerenaSheet />}
    </section>
  );
}

/* ── Serena's full sheet ─────────────────────────────────────────────────
   Every shape at 32 in the four states; every colour at the three tones
   (pale wears ink eyes) at 40 and 32; every shape at 14 and 20. */

const SHEET_STATES = ["idle", "working", "asks", "saved"];
const cycle = (i) => SHEET_STATES[i % 3];

function SerenaSheet() {
  const dir = "serena";
  const face = (props) => <FaceFable dir={dir} tone="deep" color="azure" shape="circle" {...props} />;
  return (
    <div class="ffl-sheet">
      <h3>Every shape · 32 · four states</h3>
      <div class="ffl-matrix" style={{ "--cols": AVATAR_SHAPES.length }}>
        {SHEET_STATES.map((st) => AVATAR_SHAPES.map((sh) => (
          <div class="ffl-mcell" key={`${st}:${sh}`} title={`${sh} · ${STATE_LABEL[st]}`}>
            {face({ shape: sh, color: AVATAR_COLORS[AVATAR_SHAPES.indexOf(sh) % AVATAR_COLORS.length].id, seedKey: `${sh}:${st}`, state: st, size: 32 })}
          </div>
        )))}
      </div>

      <h3>Every colour × tone · 40 (grid) and 32 (row) · states cycling at rest, working, waiting</h3>
      {AVATAR_TONES.map((tone) => (
        <div class="ffl-tone" key={tone}>
          <span class="ffl-tone-name">{tone}</span>
          <div class="ffl-matrix" style={{ "--cols": AVATAR_COLORS.length }}>
            {AVATAR_COLORS.map((c, i) => (
              <div class="ffl-mcell is-tall" key={c.id} title={`${c.id} · ${tone} · ${STATE_LABEL[cycle(i)]}`}>
                {face({ color: c.id, tone, shape: AVATAR_SHAPES[i % AVATAR_SHAPES.length], seedKey: `${c.id}:${tone}`, state: cycle(i), size: 40 })}
                {face({ color: c.id, tone, shape: AVATAR_SHAPES[(i + 3) % AVATAR_SHAPES.length], seedKey: `${c.id}:${tone}:32`, state: cycle(i + 1), size: 32 })}
              </div>
            ))}
          </div>
        </div>
      ))}

      <h3>Every shape · 20 (phone title) and 14 (status line)</h3>
      <div class="ffl-matrix" style={{ "--cols": AVATAR_SHAPES.length }}>
        {AVATAR_SHAPES.map((sh, i) => (
          <div class="ffl-mcell is-tall" key={sh} title={sh}>
            <span class="ffl-chip is-20">{face({ shape: sh, color: AVATAR_COLORS[i % AVATAR_COLORS.length].id, seedKey: `${sh}:20`, state: cycle(i), size: 20 })}Moa</span>
            <span class="ffl-chip">{face({ shape: sh, color: AVATAR_COLORS[i % AVATAR_COLORS.length].id, seedKey: `${sh}:14`, state: cycle(i + 1), size: 14 })}Moa</span>
          </div>
        ))}
      </div>
      <div class="ffl-matrix" style={{ "--cols": AVATAR_TONES.length }}>
        {AVATAR_TONES.map((tone, i) => (
          <div class="ffl-mcell is-tall" key={tone} title={`${tone} at 14 and 20`}>
            <span class="ffl-chip is-20">{face({ color: "lilac", tone, seedKey: `t20:${tone}`, state: cycle(i), size: 20 })}{tone}</span>
            <span class="ffl-chip">{face({ color: "peach", tone, seedKey: `t14:${tone}`, state: cycle(i + 1), size: 14 })}{tone}</span>
          </div>
        ))}
      </div>
    </div>
  );
}

const TODAY = {
  id: "today",
  name: "Today · Mirada (reference)",
  idea: "What is kept: the flat body, the white marks, the tilt, the breath, the rhythm. What goes: two vertical capsules, a squash blink, saccades of the pair, mood by narrowing.",
  states: {
    idle: "looks around, squash blink",
    working: "narrowed, looking low and aside, saccades",
    asks: "wide open, straight at you, a hop",
    saved: "shut",
  },
};

export function OwnerFacesFableLab() {
  const q = new URLSearchParams(location.search);
  const only = q.get("dir");
  const all = [TODAY, ...PROPOSALS];
  const list = only ? all.filter((d) => d.id === only) : all;
  return (
    <div class={`ffl${only ? " is-solo" : ""}`}>
      {!only && (
        <header class="ffl-head">
          <h1>Owner faces · a gaze of our own</h1>
          <p>
            Three proposals that keep everything Mirada is loved for and replace only what makes it
            read as Grok: the eye glyph and the motion grammar. Same owner in every row; the strip at
            the bottom shows other shapes, colours and tones. <b>Serena is the one chosen</b>; its
            section ends with the full sheet.
          </p>
          <nav class="ffl-nav">
            {all.map((d) => <a key={d.id} href={`?view=faces-fable&dir=${d.id}`}>{d.name}</a>)}
          </nav>
        </header>
      )}
      {list.map((d) => <Section key={d.id} d={d} />)}
    </div>
  );
}

/* ── Taking Serena to the product: notes for the integration session ──────

   WHERE THE AVATAR LIVES TODAY
   - components/Owners/OwnerAvatar.jsx renders OwnerFace variant="mirada";
     OwnerAvatarFor is what every surface calls (sidebar OwnerRow 32, the
     child chip OwnerChipEntry 14, MobileTitleChip 20, the phone grid in
     MobileConversationScreen 40, InboxView 28, Tasks/parts 20–24, Owners.jsx
     24, OwnerIdentityPicker 64 with `follow`).
   - components/Owners/OwnerFace.jsx: the Mirada drawing (MiradaEyes as HTML
     boxes, MiradaShut in SVG), poseVars (the sphere projection), STATE_GAZE
     and useFaceMotion. OwnerFace.css: the `.of-mirada` rules.
   - components/Owners/faceMotion.js: the shared scheduler. UNCHANGED —
     Serena runs on its blink / gaze / nudge streams as they are.
   - components/Owners/avatar-identity.js: shape × colour × tone, EYE_CENTER,
     eyeStateFor. UNCHANGED.

   WHAT IS REPLACED
   - The eye drawing: two arcs (ᵕ) at rest, two dots awake, two level lines
     saved, cross-faded by state and by the blink event (peek at rest, happy
     blink awake). Geometry in Serena.geometry / Serena.Eyes above.
   - The pose: Serena.pose (head --bx/--by/--rot, eyes --ex/--ey) instead of
     poseVars; the eyes only slide when open (`.is-idle .ff-eyes` is fixed).
     poseVars stays only if a test still wants it (faceMotion.test.js:232).
   - The CSS: `.ff-serena` rules above become the product's OwnerFace.css.
     Pale tone: eyes in ink through --ff-eye (arcs, dots, lines alike).
   - avatar-tones.test.jsx:61-67 names MiradaEyes / MiradaShut and the
     `.of-mirada.is-tone-pale` selectors: rewrite for the new parts.
   - Catalog: owner-faces-lab.jsx / owner-faces-alt.jsx compare against
     Mirada; keep them pointing at the old drawing or retire them.

   COST IN LONG LISTS (why the eyes must become HTML boxes, as Mirada's did)
   - Chrome never composites a transform on an SVG element: every transition
     on an SVG eye costs a style recalc AND a layout on the main thread, per
     frame, per face (measured for Mirada with CDP: ~380 layouts per 10 s on
     a desktop sidebar of 8 faces; OwnerFace.jsx, "The eyes"). The lab draws
     Serena's eyes in SVG because it is quicker to judge; the product build
     must draw them as HTML spans, exactly like MiradaEyes:
       dot   — a round span (border-radius 50%), scale 0 ↔ 1;
       arc   — a span with only a bottom border and a 0 0 50% 50% radius,
               opacity 0 ↔ 1 (or scaleY about its top edge);
       lines — a short span, saved only, static;
       head  — already an HTML span (rotate + translate), composited.
     Then a blink or a glance is one style invalidation and the tween runs
     off the main thread.
   - Event rate is Mirada's (same scheduler, same streams). What is new is
     the length of a tween: the peek closes over 380 ms and the dots open
     over 110 ms, against Mirada's 60/95 ms blink — more compositor frames
     per event, no more main-thread work. At rest the peek fires on the
     blink stream (every 2.5–6 s per owner); if a column of eight peeks too
     much, thin it in the apply wrapper (peek on every second blink, or only
     on the scheduler's double blinks) rather than in the scheduler.
   - Everything else that keeps Mirada cheap applies as is: one timer, no
     motion offscreen / hidden tab / reduced motion, the breath paused until
     `.is-live`, one owner one phase.

   ACCEPTANCE (Gherkin, for the integration session)
   - Given an owner at rest, its eyes are two ᵕ arcs and, every few seconds,
     open into dots for under a second and close again.
   - Given an owner working, its eyes are open dots turned low and aside,
     the head slightly bowed; a blink shows the arcs for ~150 ms.
   - Given an owner waiting on you, its eyes are open dots centred on you and
     pulse every few seconds; nothing else moves.
   - Given a saved owner, its eyes are two level lines and it does not move.
   - Given a pale tone, all four drawings are ink, not white.
   - Given prefers-reduced-motion, no transition and no breath.
   - Given 40 faces on screen, no per-frame main-thread layout (CDP trace),
     as with Mirada. */
