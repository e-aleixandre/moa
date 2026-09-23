import { useEffect, useMemo, useRef } from "preact/hooks";
import {
  AVATAR_SHAPES, EYE_CENTER, SHAPE_PATHS, avatarColor, eyeStateFor, ownerAvatar,
} from "./avatar-identity.js";
import { facePersonality, faceMotion } from "./faceMotion.js";
import "./OwnerAvatar.css";
import "./OwnerFace.css";

// OwnerFace — the owner's "Mirada" face, drawn through OwnerAvatar: a flat
// ball of the identity colour with two short, rounded white strokes for eyes,
// each leaning its own way. The eyes are alive — they blink, look around and
// behave by state — on the owner's own deterministic script (faceMotion.js).
// The pair turns like a head.
//
// BEHAVIOUR BY STATE complements the words on the row, it never replaces
// them: idle breathes and looks around, working narrows its eyes on a point
// low and aside with quick saccades, asks looks straight at you with a small
// nudge every few seconds, saved rests with its eyes shut and does not move.
//
// The two proposals it was chosen over (Pupilas, Sobria) live in the catalog
// only (src/catalog/owner-faces-alt.jsx), so the product bundle carries none
// of their code.

export const facePath = (shape) => SHAPE_PATHS[shape] || SHAPE_PATHS.circle;
export const eyeCenter = (shape) => EYE_CENTER[shape] || EYE_CENTER.circle;

/* ── Gaze by state ────────────────────────────────────────────────────────
   Where each state rests, and how much of the script's motion is added:
   "working" rests low and aside and the saccades move around that point;
   "asks" is pinned on you. */
const STATE_GAZE = {
  idle: { x: 0, y: 0, scale: 1 },
  working: { x: 0.55, y: 0.45, scale: 1 },
  asks: { x: 0, y: 0, scale: 0 },
};

const clamp = (n, lo, hi) => Math.min(hi, Math.max(lo, n));
export const r3 = (n) => Math.round(n * 1000) / 1000;

export function combineGaze(eyes, gx, gy) {
  const s = STATE_GAZE[eyes] || STATE_GAZE.idle;
  return [clamp(s.x + gx * s.scale, -1, 1), clamp(s.y + gy * s.scale, -1, 1)];
}

// How far the "head" reaches for each outline: the radius of the sphere the
// eyes are projected on. Smaller than the outline so a turned eye never lands
// on the rim — a drop narrows at eye height, a pill is wide.
const HEAD_R = {
  circle: 11, squircle: 11.5, blob: 10.5, hexagon: 10.5, drop: 10, pill: 12.5, triangle: 8, cloud: 10,
};

// poseVars turns a gaze (gx, gy in −1..1, already combined with the state)
// into the custom properties the CSS reads. Pure, so the tests can check a
// pose without a DOM.
//
// A head turn: each eye sits on a sphere at ±φ from the facing direction, so
// it moves by R·sin and is foreshortened by cos. The far eye thins and the
// pair bunches towards the edge, which reads as "turned" rather than as two
// marks sliding.
export function poseVars(p, shape, gx, gy) {
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

// Mirada's body: the identity hue one step DEEPER. The palette is L 0.80,
// which white eyes cannot stand on (a lilac ball with white strokes is a blank
// at 24px). Same hue, same chroma, lower lightness — computed from the
// palette's own oklch so the eight stay as far apart as they were chosen to be.
// Mixing with black was tried first and turned peach into brown.
export function faceBodyColor(colorId) {
  const [, , h] = avatarColor(colorId).oklch.split(" ");
  return `oklch(0.68 0.12 ${h})`;
}

let faceSeq = 0;

// useFaceMotion registers a mounted face with the shared scheduler and
// applies what it sends: blink / live / nudge classes and the gaze, turned
// into custom properties by `pose`. Exported for the catalog's alternative
// drawings, which move on the same clock.
export function useFaceMotion(ref, { p, eyes, mode, follow, pinned, pose }) {
  useEffect(() => {
    // A shut face is resting on purpose: it does not blink or look anywhere.
    if (eyes === "saved" || pinned) return undefined;
    const el = ref.current;
    const motion = faceMotion();
    if (!el || !motion) return undefined;
    const apply = (ev) => {
      if ("blink" in ev) return void el.classList.toggle("is-blink", ev.blink);
      if ("live" in ev) return void el.classList.toggle("is-live", ev.live);
      if ("nudge" in ev) return void el.classList.toggle("is-nudge", ev.nudge);
      const vars = pose(ev.gx, ev.gy);
      for (const k in vars) el.style.setProperty(k, String(vars[k]));
    };
    const off = motion.register({
      el,
      personality: p,
      mode,
      // Waiting for you means looking at you; the pointer does not steal it.
      follow: follow && eyes !== "asks",
      apply,
    });
    return () => {
      off();
      el.classList.remove("is-blink", "is-live", "is-nudge");
    };
  }, [p, eyes, mode, follow, pinned, pose.key]);
}

export function OwnerFace({
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
  const eyes = eyeStateFor(state);
  const s = AVATAR_SHAPES.includes(av.shape) ? av.shape : "circle";
  const d = facePath(s);
  const uid = useMemo(() => `ofc${++faceSeq}`, []);
  const ref = useRef(null);

  // `gaze` pins the eyes (the lab's pose sheet and the picker's swatches); a
  // pinned face does not move.
  const [g0x, g0y] = combineGaze(eyes, gaze?.[0] || 0, gaze?.[1] || 0);
  const rest = poseVars(p, s, g0x, g0y);
  const pose = (gx, gy) => poseVars(p, s, ...combineGaze(eyes, gx, gy));
  pose.key = s;
  useFaceMotion(ref, { p, eyes, mode: eyes, follow, pinned: !!gaze, pose });

  const style = {
    "--of-body": muted ? "#4a4b5c" : faceBodyColor(av.color),
    // One viewBox unit in CSS pixels: the HTML eyes move in the same units
    // the SVG was drawn in.
    "--of-u": `${size / 32}px`,
    // The breath: each owner its own period and phase, so a column of idle
    // owners never inhales in unison.
    "--of-breath": `${p.breath}ms`,
    "--of-breath-at": `-${p.seed % p.breath}ms`,
    ...rest,
  };

  const breathes = eyes === "idle" && !gaze;
  // The root also answers to `ow-av`, the class the surfaces around the mark
  // already select (StatusStrip's `.zl-st-owner .ow-av`). It is an HTML
  // <span>, not the <svg>: Chrome never hands an animation on an SVG element
  // (the outer <svg> included) to the compositor, so a breath on the <svg>
  // re-styled and re-painted every face on every frame. On a span it runs off
  // the main thread (measured in the lab).
  return (
    <span
      ref={ref}
      class={`of ow-av of-mirada is-${size} is-${eyes}${breathes ? " is-breathe" : ""}${sheen ? " has-sheen" : ""}`}
      style={{ ...style, width: `${size}px`, height: `${size}px` }}
      role={title ? "img" : undefined}
      aria-label={title}
      aria-hidden={title ? undefined : "true"}
    >
      <span class="of-bodybox">
        <svg class="of-svg" viewBox="0 0 32 32" width={size} height={size} aria-hidden="true">
          {sheen && (
            <defs>
              <radialGradient id={`${uid}s`} cx="0.36" cy="0.26" r="0.62">
                <stop class="of-sheen-0" offset="0" />
                <stop class="of-sheen-1" offset="0.55" />
                <stop class="of-sheen-2" offset="1" />
              </radialGradient>
              <linearGradient id={`${uid}d`} x1="0" y1="0.35" x2="0" y2="1">
                <stop class="of-shade-0" offset="0" />
                <stop class="of-shade-1" offset="1" />
              </linearGradient>
            </defs>
          )}
          <g class="of-body">
            <path class="of-fill" d={d} />
            {/* Satin: one broad, soft specular from the top-left, drawn inside
                the outline, and the underside turning away from it, which is
                what makes the highlight read as a curved surface. The colour
                stays the colour. */}
            {sheen && <path class="of-sheen" d={d} fill={`url(#${uid}s)`} />}
            {sheen && <path class="of-sheen" d={d} fill={`url(#${uid}d)`} />}
          </g>
          {eyes === "saved" && <MiradaShut p={p} shape={s} />}
        </svg>
      </span>
      {eyes !== "saved" && <MiradaEyes p={p} shape={s} size={size} />}
    </span>
  );
}

// OwnerFaceFor mirrors OwnerAvatarFor: the one-argument form.
export function OwnerFaceFor({ owner, ...rest }) {
  return <OwnerFace owner={owner} {...rest} />;
}

// ShutEyes are two closed arcs, the same drawing as the previous mark's.
export function ShutEyes({ cx, cy, dx, cls }) {
  return (
    <g class={cls} stroke-linecap="round" fill="none" stroke-width="1.7">
      <path d={`M${r3(cx - dx - 1.9)} ${cy} q1.9 1.7 3.8 0`} />
      <path d={`M${r3(cx + dx - 1.9)} ${cy} q1.9 1.7 3.8 0`} />
    </g>
  );
}

/* ── The eyes ────────────────────────────────────────────────────────────
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
