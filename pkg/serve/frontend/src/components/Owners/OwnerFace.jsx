import { useEffect, useMemo, useRef } from "preact/hooks";
import {
  AVATAR_COLORS, AVATAR_SHAPES, EYE_CENTER, SHAPE_PATHS, eyeStateFor, ownerAvatar,
} from "./avatar-identity.js";
import { facePersonality, faceMotion } from "./faceMotion.js";
import "./OwnerAvatar.css";
import "./OwnerFace.css";

// OwnerFace — the owner's face with eyes that are alive: they blink, look
// around and behave by state, on the owner's own deterministic script
// (faceMotion.js). The product draws "mirada" through OwnerAvatar; the other
// two variants remain for the ?view=faces lab, where the three were compared:
//
//   mirada  — THE PRODUCT'S. A flat ball of the identity colour (optionally a
//             satin sheen) with two short, rounded white strokes for eyes,
//             each leaning its own way. The pair turns like a head.
//   pupilas — today's lit tile, with light sclera and dark pupils.
//   sobria  — today's mark exactly, plus irregular blinks and a rare glance.
//
// Same props as OwnerAvatar (plus `variant`, `follow`, `sheen`, the seed) so
// it can replace it wherever the trial lands.
//
// BEHAVIOUR BY STATE (Mirada and Pupilas) complements the words on the row,
// it never replaces them: idle breathes and looks around, working narrows its
// eyes on a point low and aside with quick saccades, asks looks straight at
// you with a small nudge every few seconds, saved rests with its eyes shut
// and does not move. Sobria keeps today's poses and only blinks and glances.

const COLOR_BY_ID = new Map(AVATAR_COLORS.map((c) => [c.id, c]));

export const FACE_VARIANTS = ["mirada", "pupilas", "sobria"];

/* ── Opt-in shapes ────────────────────────────────────────────────────────
   Selectable only. The default face is `hash % AVATAR_SHAPES.length` on both
   sides (pkg/owner/avatar.go), so appending to AVATAR_SHAPES would silently
   re-face every owner that never chose. These live in a separate list that
   the default never reads; the server would also have to learn them before
   one could be stored. */
export const OPT_IN_SHAPES = ["triangle", "cloud"];
export const FACE_SHAPES = [...AVATAR_SHAPES, ...OPT_IN_SHAPES];

const EXTRA_PATHS = {
  triangle: "M13.2 5.4Q16 .9 18.8 5.4L29.3 23.4Q31.9 28 26.6 28H5.4Q.1 28 2.7 23.4Z",
  cloud: "M9.2 27.2A6.4 6.4 0 0 1 7.7 14.6 8.4 8.4 0 0 1 24 12.1 7.5 7.5 0 0 1 24.4 27.2Z",
};
const EXTRA_EYES = { triangle: [16, 19.4], cloud: [16.4, 19] };

export const facePath = (shape) => SHAPE_PATHS[shape] || EXTRA_PATHS[shape] || SHAPE_PATHS.circle;
const eyeCenter = (shape) => EYE_CENTER[shape] || EXTRA_EYES[shape] || EYE_CENTER.circle;

/* ── Gaze by state ────────────────────────────────────────────────────────
   Where each state rests, and how much of the script's motion is added. For
   Mirada and Pupilas "working" rests low and aside and the saccades move
   around that point; "asks" is pinned on you. Sobria keeps today's pose to
   the unit (3.4 aside) with a third of the wander on top. */
const STATE_GAZE = {
  expressive: {
    idle: { x: 0, y: 0, scale: 1 },
    working: { x: 0.55, y: 0.45, scale: 1 },
    asks: { x: 0, y: 0, scale: 0 },
  },
  sobria: {
    idle: { x: 0, y: 0, scale: 1 },
    working: { x: 1, y: 0, scale: 0.3 },
    asks: { x: 0, y: 0, scale: 1 },
  },
};

const clamp = (n, lo, hi) => Math.min(hi, Math.max(lo, n));
const r3 = (n) => Math.round(n * 1000) / 1000;

export function combineGaze(eyes, gx, gy, variant) {
  const table = variant === "sobria" ? STATE_GAZE.sobria : STATE_GAZE.expressive;
  const s = table[eyes] || table.idle;
  return [clamp(s.x + gx * s.scale, -1, 1), clamp(s.y + gy * s.scale, -1, 1)];
}

// How far the "head" reaches for each outline: the radius of the sphere the
// eyes are projected on. Smaller than the outline so a turned eye never lands
// on the rim — a drop narrows at eye height, a pill is wide.
const HEAD_R = {
  circle: 11, squircle: 11.5, blob: 10.5, hexagon: 10.5, drop: 10, pill: 12.5, triangle: 8, cloud: 10,
};

// poseVars turns a gaze (gx, gy in −1..1, already combined with the state)
// into the custom properties each drawing's CSS reads. Pure, so the lab and
// the tests can check a pose without a DOM.
export function poseVars(variant, p, shape, gx, gy) {
  if (variant === "mirada") {
    // A head turn: each eye sits on a sphere at ±φ from the facing direction,
    // so it moves by R·sin and is foreshortened by cos. The far eye thins and
    // the pair bunches towards the edge, which reads as "turned" rather than
    // as two marks sliding.
    const R = HEAD_R[shape] || 11;
    const phi = Math.asin(Math.min(0.9, p.eyeGap / R));
    const yaw = gx * 0.72;
    const eye = (side) => {
      const th = yaw + side * phi;
      return {
        x: R * Math.sin(th) - side * p.eyeGap,
        s: Math.max(0.3, Math.cos(th) / Math.cos(phi)),
      };
    };
    const l = eye(-1);
    const r = eye(1);
    return {
      "--lx": r3(l.x), "--ls": r3(l.s), "--rx": r3(r.x), "--rs": r3(r.s),
      "--gy": r3(gy * 2.6), "--sy": r3(1 - 0.14 * Math.abs(gy)),
      "--bx": r3(gx * 0.45), "--by": r3(gy * 0.3),
    };
  }
  if (variant === "pupilas") {
    const { rx, ry } = scleraSize(p);
    // At full travel the pupil tucks under the edge of the white (it is
    // clipped to it), which is what a real eye looking hard aside does.
    return {
      "--px": r3(gx * (rx - p.pupil * 0.55)),
      "--py": r3(gy * (ry - p.pupil * 0.75)),
    };
  }
  // sobria: 3.4 is the distance OwnerAvatar's "working" pupils travel.
  return { "--ox": r3(gx * 3.4), "--oy": r3(gy * 1.2) };
}

function scleraSize(p) {
  return p.sclera === "tall" ? { rx: 3.1, ry: 3.8 } : { rx: 3.4, ry: 3.4 };
}

// Mirada's body: the identity hue one step DEEPER. The palette is L 0.80,
// which white eyes cannot stand on (a lilac ball with white strokes is a blank
// at 24px). Same hue, same chroma, lower lightness — computed from the
// palette's own oklch so the eight stay as far apart as they were chosen to be.
// Mixing with black was tried first and turned peach into brown.
export function faceBodyColor(colorId) {
  const c = COLOR_BY_ID.get(colorId) || AVATAR_COLORS[0];
  const [, , h] = c.oklch.split(" ");
  return `oklch(0.68 0.12 ${h})`;
}

let faceSeq = 0;

export function OwnerFace({
  variant = "mirada",
  owner,
  shape,
  color,
  seedKey,
  state = "idle",
  size = 32,
  follow = false,
  gaze,
  sheen = false,
  muted = false,
  title,
}) {
  const av = owner ? ownerAvatar(owner) : { shape: shape || "circle", color: color || "peach" };
  const key = seedKey ?? owner?.codebase_key ?? owner?.name ?? `${av.shape}:${av.color}`;
  const p = useMemo(() => facePersonality(key), [key]);
  const hex = (COLOR_BY_ID.get(av.color) || AVATAR_COLORS[0]).hex;
  const eyes = eyeStateFor(state);
  const s = FACE_SHAPES.includes(av.shape) ? av.shape : "circle";
  const d = facePath(s);
  const uid = useMemo(() => `ofc${++faceSeq}`, []);
  const ref = useRef(null);
  const expressive = variant !== "sobria";

  // `gaze` pins the eyes (the lab's pose sheet and the picker's swatches); a
  // pinned face does not move.
  const [g0x, g0y] = combineGaze(eyes, gaze?.[0] || 0, gaze?.[1] || 0, variant);
  const rest = poseVars(variant, p, s, g0x, g0y);

  useEffect(() => {
    // A shut face is resting on purpose: it does not blink or look anywhere.
    if (eyes === "saved" || gaze) return undefined;
    const el = ref.current;
    const motion = faceMotion();
    if (!el || !motion) return undefined;
    const apply = (ev) => {
      if ("blink" in ev) return void el.classList.toggle("is-blink", ev.blink);
      if ("live" in ev) return void el.classList.toggle("is-live", ev.live);
      if ("nudge" in ev) return void el.classList.toggle("is-nudge", ev.nudge);
      const [x, y] = combineGaze(eyes, ev.gx, ev.gy, variant);
      const vars = poseVars(variant, p, s, x, y);
      for (const k in vars) el.style.setProperty(k, String(vars[k]));
    };
    const off = motion.register({
      el,
      personality: p,
      mode: expressive ? eyes : "calm",
      // Waiting for you means looking at you; the pointer does not steal it.
      follow: follow && eyes !== "asks",
      apply,
    });
    return () => {
      off();
      el.classList.remove("is-blink", "is-live", "is-nudge");
    };
  }, [variant, p, s, eyes, follow, !!gaze]);

  const style = {
    "--ow-av-c": muted ? "#5d5e70" : hex,
    "--of-body": muted ? "#4a4b5c" : faceBodyColor(av.color),
    "--ow-av-fill": `url(#${uid}f)`,
    "--ow-av-edge": `url(#${uid}l)`,
    // One viewBox unit in CSS pixels: Mirada's HTML eyes move in the same
    // units the SVG was drawn in.
    "--of-u": `${size / 32}px`,
    // The breath: each owner its own period and phase, so a column of idle
    // owners never inhales in unison.
    "--of-breath": `${p.breath}ms`,
    "--of-breath-at": `-${p.seed % p.breath}ms`,
    ...rest,
  };

  const breathes = expressive && eyes === "idle" && !gaze;
  // Every variant also answers to `ow-av`, the class the surfaces around the
  // mark already select (StatusStrip's `.zl-st-owner .ow-av`). Sobria and
  // Pupilas draw with that sheet's tile; Mirada only shares its root rules.
  const base = "of ow-av";
  // The root is an HTML <span>, not the <svg>: Chrome never hands an
  // animation on an SVG element (the outer <svg> included) to the compositor,
  // so a breath on the <svg> re-styled and re-painted every face on every
  // frame. On a span it runs off the main thread (measured in the lab).
  const svg = <FaceSvg variant={variant} uid={uid} size={size} sheen={sheen} d={d} p={p} s={s} eyes={eyes} />;
  return (
    <span
      ref={ref}
      class={`${base} of-${variant} is-${size} is-${eyes}${breathes ? " is-breathe" : ""}${sheen ? " has-sheen" : ""}`}
      style={{ ...style, width: `${size}px`, height: `${size}px` }}
      role={title ? "img" : undefined}
      aria-label={title}
      aria-hidden={title ? undefined : "true"}
    >
    {variant === "mirada" && <span class="of-bodybox">{svg}</span>}
    {variant === "mirada" && eyes !== "saved" && <MiradaEyes p={p} shape={s} size={size} />}
    {variant !== "mirada" && svg}
    </span>
  );
}

function FaceSvg({ variant, uid, size, sheen, d, p, s, eyes }) {
  return (
    <svg class="of-svg" viewBox="0 0 32 32" width={size} height={size} aria-hidden="true">
      <defs>
        <linearGradient id={`${uid}f`} x1="0" y1="0" x2="0" y2="1">
          <stop class="ow-av-f0" offset="0" />
          <stop class="ow-av-f1" offset="1" />
        </linearGradient>
        <linearGradient id={`${uid}l`} x1="0" y1="0" x2="0" y2="0.5">
          <stop class="ow-av-l0" offset="0" />
          <stop class="ow-av-l1" offset="1" />
        </linearGradient>
        {variant === "mirada" && sheen && (
          <radialGradient id={`${uid}s`} cx="0.36" cy="0.26" r="0.62">
            <stop class="of-sheen-0" offset="0" />
            <stop class="of-sheen-1" offset="0.55" />
            <stop class="of-sheen-2" offset="1" />
          </radialGradient>
        )}
        {variant === "mirada" && sheen && (
          <linearGradient id={`${uid}d`} x1="0" y1="0.35" x2="0" y2="1">
            <stop class="of-shade-0" offset="0" />
            <stop class="of-shade-1" offset="1" />
          </linearGradient>
        )}
      </defs>
      {variant === "mirada" ? (
        <g class="of-body">
          <path class="of-fill" d={d} />
          {/* Satin: one broad, soft specular from the top-left, drawn inside
              the outline. Not a gradient across the colour — the colour
              stays the colour. */}
          {sheen && <path class="of-sheen" d={d} fill={`url(#${uid}s)`} />}
          {/* …and the underside turning away from it, which is what makes the
              highlight read as a curved surface instead of a smudge. */}
          {sheen && <path class="of-sheen" d={d} fill={`url(#${uid}d)`} />}
        </g>
      ) : (
        <>
          <path class="ow-av-body" d={d} />
          <path class="ow-av-lit" d={d} />
        </>
      )}
      {variant === "mirada" && eyes === "saved" && <MiradaShut p={p} shape={s} />}
      {variant === "pupilas" && <PupilasEyes p={p} shape={s} eyes={eyes} uid={uid} />}
      {variant === "sobria" && <SobriaEyes shape={s} eyes={eyes} />}
    </svg>
  );
}

// OwnerFaceFor mirrors OwnerAvatarFor: the one-argument form.
export function OwnerFaceFor({ owner, ...rest }) {
  return <OwnerFace owner={owner} {...rest} />;
}

/* ── Shut eyes, shared by the three: the same two arcs as today ─────────── */

function ShutEyes({ cx, cy, dx, cls }) {
  return (
    <g class={cls} stroke-linecap="round" fill="none" stroke-width="1.7">
      <path d={`M${r3(cx - dx - 1.9)} ${cy} q1.9 1.7 3.8 0`} />
      <path d={`M${r3(cx + dx - 1.9)} ${cy} q1.9 1.7 3.8 0`} />
    </g>
  );
}

/* ── A · Mirada ──────────────────────────────────────────────────────────
   The eyes are HTML, not SVG. Measured with CDP over 10 s on 8 faces: when
   they were SVG groups every frame of every gaze/blink transition cost a
   style recalc AND a layout on the main thread (~380 layouts per 10 s on a
   desktop sidebar, 11% of a 4×-throttled phone), because Chrome runs no SVG
   animation on the compositor. As HTML boxes the same transform transitions
   are composited, and the main thread only pays for the event that starts
   them.

   Per eye, four boxes sharing one centre, so each transform-origin: center
   is the stroke's centre, exactly like the SVG groups they replace:
     gaze  (head-turn translate + foreshortening, set by faceMotion)
     lean  (the stroke's own tilt, static)
     open  (narrowed when working, wider when asking)
     lid   (blink) — the white pill itself.
   Geometry is in viewBox units turned into % of the face, so one drawing
   serves every size. */

function MiradaEyes({ p, shape, size }) {
  const [cx, cy0] = eyeCenter(shape);
  // A touch lower than the tile's eyes: more forehead reads as a head, and a
  // head is what this drawing turns.
  const cy = cy0 + 1;
  // At chip and row sizes a 2.6 stroke is under 2px; a little heavier keeps
  // the eyes from dissolving into the colour.
  const sw = size <= 24 ? 3 : 2.6;
  // A round-capped line of length L and width w is a w × (L + w) pill.
  const h = p.strokeLen + sw;
  const pct = (n) => `${r3((n / 32) * 100)}%`;
  const eye = (side) => (
    <span
      class={`of-gaze of-gaze-${side < 0 ? "l" : "r"}`}
      style={{
        left: pct(cx + side * p.eyeGap - sw / 2),
        top: pct(cy - h / 2),
        width: pct(sw),
        height: pct(h),
      }}
    >
      <span class="of-lean" style={{ transform: `rotate(${p.tilt + side * p.skew}deg)` }}>
        <span class="of-open"><span class="of-lid" /></span>
      </span>
    </span>
  );
  return (
    <span class="of-m-eyes" aria-hidden="true">
      {eye(-1)}
      {eye(1)}
    </span>
  );
}

// Shut eyes never move, so they stay in the SVG.
function MiradaShut({ p, shape }) {
  const [cx, cy0] = eyeCenter(shape);
  return <ShutEyes cx={cx} cy={cy0 + 1} dx={p.eyeGap - 0.4} cls="of-m-shut" />;
}

/* ── B · Pupilas ─────────────────────────────────────────────────────────
   The sclera is the lid: blinking closes the white, pupil and all. The glint
   is only drawn from 40px up (CSS), where it reads as life instead of noise. */

function PupilasEyes({ p, shape, eyes, uid }) {
  const [cx, cy] = eyeCenter(shape);
  const dx = 4.7;
  if (eyes === "saved") return <ShutEyes cx={cx} cy={cy} dx={4.3} cls="ow-av-eyes" />;
  const { rx, ry } = scleraSize(p);
  // One clip serves both eyes: it is in each eye's own coordinates.
  const eye = (side) => (
    <g transform={`translate(${r3(cx + side * dx)} ${cy})`}>
      <g class="of-open">
        <g class="of-lid">
          <ellipse class="of-p-sclera" rx={rx} ry={ry} />
          <g clip-path={`url(#${uid}c)`}>
            <g class="of-pupil">
              <circle class="of-p-pupil" r={p.pupil} />
              <circle class="of-p-glint" cx={-p.pupil * 0.38} cy={-p.pupil * 0.42} r={p.pupil * 0.34} />
            </g>
          </g>
        </g>
      </g>
    </g>
  );
  return (
    <g class="of-p-eyes">
      <clipPath id={`${uid}c`}>
        <ellipse rx={rx} ry={ry} />
      </clipPath>
      {eye(-1)}
      {eye(1)}
      {eyes === "asks" && (
        <path
          class="of-p-brow"
          d={`M${r3(cx + dx - 2.6)} ${r3(cy - ry - 1.5)} q2.6 -1.6 5.2 -.2`}
        />
      )}
    </g>
  );
}

/* ── C · Sobria ──────────────────────────────────────────────────────────
   OwnerAvatar's own eyes, coordinate for coordinate, inside two groups: one
   that glances, one that blinks. */

function SobriaEyes({ shape, eyes }) {
  const [cx, cy] = eyeCenter(shape);
  const dx = 4.3;
  if (eyes === "saved") return <ShutEyes cx={cx} cy={cy} dx={dx} cls="ow-av-eyes" />;
  const r = eyes === "working" ? 1.7 : 2;
  return (
    <g class="ow-av-eyes">
      <g class="of-s-gaze">
        <g class="of-lid"><ellipse cx={cx - dx} cy={cy} rx={r} ry={2} /></g>
        <g class="of-lid"><ellipse cx={cx + dx} cy={cy} rx={r} ry={2} /></g>
      </g>
      {eyes === "asks" && (
        <path
          d={`M${cx + dx - 2.4} ${cy - 4.4} q2.4 -1.5 4.8 -.2`}
          fill="none"
          stroke-width="1.6"
          stroke-linecap="round"
        />
      )}
    </g>
  );
}
