import { useEffect, useMemo, useRef } from "preact/hooks";
import {
  AVATAR_SHAPES, EYE_CENTER, SHAPE_PATHS, avatarTone, bodyOklch, eyeStateFor, ownerAvatar,
} from "./avatar-identity.js";
import { facePersonality, faceMotion } from "./faceMotion.js";
import "./OwnerAvatar.css";
import "./OwnerFace.css";

// OwnerFace — the owner's "Serena" face, drawn through OwnerAvatar: a flat
// ball of the identity colour whose eyes are content and shut (ᵕ ᵕ) while all
// is well, open dots when it has something to look at, and two level lines
// when it is saved. The HEAD does the looking — body and eyes turn and lean
// together — so the marks never slide on a body that is at rest. The eyes
// run on the owner's own deterministic script (faceMotion.js).
//
// BEHAVIOUR BY STATE complements the words on the row, it never replaces
// them: idle breathes, wanders its head and now and then peeks (the dots open
// fast and close slowly); working keeps the dots open, low and aside, head
// bowed, and blinks into the arcs for a moment; asks looks straight at you
// and pulses its dots every few seconds; saved is two level lines and does
// not move.
//
// The alternatives it was chosen over (Mirada, Pupilas, Sobria) live in the
// catalog only, so the product bundle carries none of their code.

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

// Serena's pose: the head (--bx/--by/--rot) always follows the gaze; the eyes
// (--ex/--ey) slide on the body only while open — the CSS ignores them at
// rest. Asking is pinned on you, so the head does not turn.
export function serenaPose(eyes, gx, gy) {
  return {
    "--bx": r3(gx * 1.8),
    "--by": r3(gy * 1.6),
    "--ex": r3(gx * 2.4),
    "--ey": r3(gy * 2.4),
    "--rot": r3(eyes === "asks" ? 0 : gx * 5),
  };
}

// Serena's eye geometry in viewBox units; chip sizes get heavier marks so the
// eyes do not dissolve into the colour.
export function serenaGeometry(p, small) {
  return {
    w: small ? 2.6 : 2.3, // half-width of the arc and the line
    h: small ? 1.9 : 1.7, // depth of the arc
    dr: small ? 2.2 : 2.0, // dot radius
    gap: p.eyeGap + (small ? 0.5 : 0.3),
    arcSw: small ? 2.8 : 2.4,
    lineSw: small ? 2.1 : 2.2,
  };
}

// Serena's body: the identity hue at the owner's tone (avatar-identity.js,
// AVATAR_TONES). The default `deep` is the hue one step DEEPER than the
// palette's L 0.80, which white eyes cannot stand on (a lilac ball with white
// eyes is a blank at 24px). Same hue, lower lightness — computed from the
// palette's own oklch so the eight stay as far apart as they were chosen to be.
// Mixing with black was tried first and turned peach into brown.
export function faceBodyColor(colorId, tone) {
  const [l, c, h] = bodyOklch(colorId, tone);
  return `oklch(${l} ${c} ${h})`;
}

// syncPose writes a pose's custom properties straight to the element. The
// scheduler does the same behind the renderer's back, so the last gaze it sent
// would survive a state change whose rendered style is unchanged (idle →
// saved, idle → asks): the rest pose is written again whenever the state is.
export function syncPose(el, vars) {
  for (const k in vars) el.style.setProperty(k, String(vars[k]));
}

let faceSeq = 0;

// useFaceMotion registers a mounted face with the shared scheduler and
// applies what it sends: blink / live / nudge classes and the gaze, turned
// into custom properties by `pose`. Exported for the catalog's alternative
// drawings, which move on the same clock.
export function useFaceMotion(ref, { p, eyes, mode, follow, pinned, pose, rest }) {
  useEffect(() => {
    if (rest && ref.current) syncPose(ref.current, rest);
    // A shut face is resting on purpose: it does not blink or look anywhere.
    if (eyes === "saved" || pinned) return undefined;
    const el = ref.current;
    const motion = faceMotion();
    if (!el || !motion) return undefined;
    const apply = (ev) => {
      if ("blink" in ev) return void el.classList.toggle("is-blink", ev.blink);
      if ("live" in ev) return void el.classList.toggle("is-live", ev.live);
      if ("nudge" in ev) return void el.classList.toggle("is-nudge", ev.nudge);
      syncPose(el, pose(ev.gx, ev.gy));
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
  tone,
  seedKey,
  state = "idle",
  size = 32,
  follow = false,
  gaze,
  sheen = false,
  muted = false,
  title,
}) {
  const av = owner ? ownerAvatar(owner) : { shape: shape || "circle", color: color || "peach", tone: avatarTone(tone) };
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
  const rest = serenaPose(eyes, g0x, g0y);
  const pose = (gx, gy) => serenaPose(eyes, ...combineGaze(eyes, gx, gy));
  pose.key = eyes;
  const small = size <= 24;
  useFaceMotion(ref, { p, eyes, mode: eyes, follow, pinned: !!gaze, pose, rest });

  const style = {
    "--of-body": muted ? "#4a4b5c" : faceBodyColor(av.color, av.tone),
    // One viewBox unit in CSS pixels: the head and the eyes move, and the
    // strokes are drawn, in the units the body was drawn in.
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
      class={`of ow-av of-serena is-${size} is-${eyes}${muted ? "" : ` is-tone-${av.tone}`}${small ? " is-small" : ""}${breathes ? " is-breathe" : ""}${sheen ? " has-sheen" : ""}`}
      style={{ ...style, width: `${size}px`, height: `${size}px` }}
      role={title ? "img" : undefined}
      aria-label={title}
      aria-hidden={title ? undefined : "true"}
    >
      <span class="of-head">
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
        </svg>
        <SerenaEyes p={p} shape={s} eyes={eyes} small={small} />
      </span>
    </span>
  );
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
   animation on the compositor. As HTML boxes the same transform and opacity
   transitions are composited, and the main thread only pays for the event
   that starts them.

   Structure: .of-eyes (the pair, translated by --ex/--ey when open) > per
   eye a square box centred on the eye and turned by the owner's own tilt >
   its drawings, which exist only for the states that use them:
     arc   (idle, working, asks) — a round-capped curve, static SVG in an HTML box: ᵕ
     dot   (idle, working, asks) — a circle, scaled 0 ↔ 1
     line  (saved)               — a level pill, static
   Geometry is in viewBox units turned into % of the box, so one drawing
   serves every size; stroke widths are in --of-u units. */

const EYE_BOX = 12; // viewBox units: each eye's box, centred on the eye

export function SerenaEyes({ p, shape, eyes, small }) {
  const [cx, cy0] = eyeCenter(shape);
  // A touch lower than the tile's eyes: more forehead reads as a head.
  const cy = cy0 + 1.2;
  const g = serenaGeometry(p, small);
  const pct = (n) => `${r3((n / 32) * 100)}%`;
  const q = (n) => `${r3(((n + EYE_BOX / 2) / EYE_BOX) * 100)}%`; // local → % of the eye box
  const qs = (n) => `${r3((n / EYE_BOX) * 100)}%`;
  // The arc ᵕ is the lab's own curve, a round-capped quadratic, drawn in a
  // static SVG that fills the eye's box. Only its HTML wrapper animates
  // (opacity), so the tween stays on the compositor.
  const E = EYE_BOX / 2;
  const arcD = `M${r3(-g.w)} ${r3(-g.h * 0.45)}Q0 ${r3(g.h * 1.55)} ${g.w} ${r3(-g.h * 0.45)}`;
  const eye = (side) => (
    <span
      class="of-eye"
      style={{
        left: pct(cx + side * g.gap - EYE_BOX / 2),
        top: pct(cy - EYE_BOX / 2),
        width: pct(EYE_BOX),
        height: pct(EYE_BOX),
        transform: `rotate(${r3(p.tilt * 0.45 + side * p.skew * 0.3)}deg)`,
      }}
    >
      {eyes === "saved" ? (
        <span
          class="of-line"
          style={{ left: q(-g.w - g.lineSw / 2), top: q(0.3 - g.lineSw / 2), width: qs(2 * g.w + g.lineSw), height: qs(g.lineSw) }}
        />
      ) : (
        <>
          <span class="of-arc">
            <svg viewBox={`${-E} ${-E} ${EYE_BOX} ${EYE_BOX}`} aria-hidden="true">
              <path d={arcD} stroke-width={g.arcSw} />
            </svg>
          </span>
          <span class="of-dot" style={{ left: q(-g.dr), top: q(0.2 - g.dr), width: qs(2 * g.dr), height: qs(2 * g.dr) }} />
        </>
      )}
    </span>
  );
  return (
    <span class="of-eyes" aria-hidden="true">
      {eye(-1)}
      {eye(1)}
    </span>
  );
}
