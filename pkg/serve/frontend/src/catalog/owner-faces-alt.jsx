import { useMemo, useRef } from "preact/hooks";
import {
  AVATAR_SHAPES, EYE_CENTER, SHAPE_PATHS, avatarColor, eyeStateFor, ownerAvatar,
} from "../components/Owners/avatar-identity.js";
import { facePersonality } from "../components/Owners/faceMotion.js";
import { ShutEyes, r3, useFaceMotion } from "../components/Owners/OwnerFace.jsx";
import "./owner-faces-alt.css";

// owner-faces-alt — CATALOG ONLY. The two proposals Mirada was chosen over
// (B · Pupilas, C · Sobria) and the static mark it replaced
// (OwnerAvatarClassic), kept so ?view=faces can still show the comparison.
// They live here rather than in components/Owners so the production bundle
// carries none of their code or CSS. They move on the product's own clock
// (useFaceMotion), so what the lab compares is the drawing, not the timing.

export const ALT_VARIANTS = ["pupilas", "sobria"];

/* Gaze by state. Pupilas behaves like Mirada (working rests low and aside,
   asks is pinned on you); Sobria keeps the previous mark's pose to the unit
   (3.4 aside) with a third of the wander on top. */
const STATE_GAZE = {
  pupilas: {
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

export function altGaze(variant, eyes, gx, gy) {
  const table = STATE_GAZE[variant] || STATE_GAZE.pupilas;
  const s = table[eyes] || table.idle;
  return [clamp(s.x + gx * s.scale, -1, 1), clamp(s.y + gy * s.scale, -1, 1)];
}

function scleraSize(p) {
  return p.sclera === "tall" ? { rx: 3.1, ry: 3.8 } : { rx: 3.4, ry: 3.4 };
}

export function altPoseVars(variant, p, gx, gy) {
  if (variant === "pupilas") {
    const { rx, ry } = scleraSize(p);
    // At full travel the pupil tucks under the edge of the white (it is
    // clipped to it), which is what a real eye looking hard aside does.
    return {
      "--px": r3(gx * (rx - p.pupil * 0.55)),
      "--py": r3(gy * (ry - p.pupil * 0.75)),
    };
  }
  // sobria: 3.4 is the distance the previous mark's "working" pupils travel.
  return { "--ox": r3(gx * 3.4), "--oy": r3(gy * 1.2) };
}

let altSeq = 0;

export function OwnerFaceAlt({
  variant = "pupilas",
  owner,
  shape,
  color,
  seedKey,
  state = "idle",
  size = 32,
  follow = false,
  gaze,
  title,
}) {
  const av = owner ? ownerAvatar(owner) : { shape: shape || "circle", color: color || "peach" };
  const key = seedKey ?? owner?.codebase_key ?? owner?.name ?? `${av.shape}:${av.color}`;
  const p = useMemo(() => facePersonality(key), [key]);
  const eyes = eyeStateFor(state);
  const s = AVATAR_SHAPES.includes(av.shape) ? av.shape : "circle";
  const d = SHAPE_PATHS[s];
  const uid = useMemo(() => `ofa${++altSeq}`, []);
  const ref = useRef(null);
  const pose = (gx, gy) => altPoseVars(variant, p, ...altGaze(variant, eyes, gx, gy));
  pose.key = `${variant}:${s}`;
  useFaceMotion(ref, {
    p, eyes, mode: variant === "sobria" ? "calm" : eyes, follow, pinned: !!gaze, pose,
  });
  const rest = altPoseVars(variant, p, ...altGaze(variant, eyes, gaze?.[0] || 0, gaze?.[1] || 0));
  const breathes = variant === "pupilas" && eyes === "idle" && !gaze;
  return (
    <span
      ref={ref}
      class={`of ow-av of-${variant} is-${size} is-${eyes}${breathes ? " is-breathe" : ""}`}
      style={{
        "--ow-av-c": avatarColor(av.color).hex,
        "--ow-av-fill": `url(#${uid}f)`,
        "--ow-av-edge": `url(#${uid}l)`,
        "--of-breath": `${p.breath}ms`,
        "--of-breath-at": `-${p.seed % p.breath}ms`,
        ...rest,
        width: `${size}px`,
        height: `${size}px`,
      }}
      role={title ? "img" : undefined}
      aria-label={title}
      aria-hidden={title ? undefined : "true"}
    >
      <svg class="of-svg" viewBox="0 0 32 32" width={size} height={size} aria-hidden="true">
        <TileDefs uid={uid} />
        <path class="ow-av-body" d={d} />
        <path class="ow-av-lit" d={d} />
        {variant === "pupilas"
          ? <PupilasEyes p={p} shape={s} eyes={eyes} uid={uid} />
          : <SobriaEyes shape={s} eyes={eyes} />}
      </svg>
    </span>
  );
}

// The lit tile's two paint servers, one pair per mounted mark: a document
// holds dozens of these at once, and two <defs> sharing an id is the first
// paint server winning everywhere.
function TileDefs({ uid }) {
  return (
    <defs>
      <linearGradient id={`${uid}f`} x1="0" y1="0" x2="0" y2="1">
        <stop class="ow-av-f0" offset="0" />
        <stop class="ow-av-f1" offset="1" />
      </linearGradient>
      {/* The lit edge runs out at mid-height rather than at the bottom: a
          highlight faded over the whole outline reads as a thinner rim, not
          as one edge facing the light. */}
      <linearGradient id={`${uid}l`} x1="0" y1="0" x2="0" y2="0.5">
        <stop class="ow-av-l0" offset="0" />
        <stop class="ow-av-l1" offset="1" />
      </linearGradient>
    </defs>
  );
}

/* ── B · Pupilas ─────────────────────────────────────────────────────────
   The sclera is the lid: blinking closes the white, pupil and all. The glint
   is only drawn from 40px up (CSS), where it reads as life instead of noise. */

function PupilasEyes({ p, shape, eyes, uid }) {
  const [cx, cy] = EYE_CENTER[shape] || EYE_CENTER.circle;
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
  const [cx, cy] = EYE_CENTER[shape] || EYE_CENTER.circle;
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

/* ── The previous mark, kept for comparison ──────────────────────────────
   The static lit tile the product drew before Mirada. No surface renders it;
   the design labs show it as "Antes" beside the face that replaced it, and
   the B/C proposals above wear the same tile (owner-faces-alt.css). */

function Eyes({ state, shape }) {
  const [cx, cy] = EYE_CENTER[shape] || EYE_CENTER.circle;
  const dx = 4.3;
  if (state === "saved") {
    return (
      <g class="ow-av-eyes" stroke-linecap="round" fill="none" stroke-width="1.7">
        <path d={`M${cx - dx - 1.9} ${cy} q1.9 1.7 3.8 0`} />
        <path d={`M${cx + dx - 1.9} ${cy} q1.9 1.7 3.8 0`} />
      </g>
    );
  }
  // Looking aside: the pupils travel 3.4 of the 4.3 that separates them, which
  // is what makes the glance legible at 20px. At 1.5 — the first attempt,
  // photographed in the gallery — "working" and "idle" were the same drawing.
  const look = state === "working" ? 3.4 : 0;
  const r = state === "working" ? 1.7 : 2;
  return (
    <g class="ow-av-eyes">
      <ellipse cx={cx - dx + look} cy={cy} rx={r} ry={2} />
      <ellipse cx={cx + dx + look} cy={cy} rx={r} ry={2} />
      {state === "asks" && (
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

/* ── Where the light comes from ─────────────────────────────────────────
   The mark used to be one flat fill with a rim, in an interface where nothing
   else is: every sheet is lit from above (`inset 0 1px 0 var(--zl-line)`, in
   19 stylesheets), the page sits over the aurora (tokens/shell.css) and the
   surfaces drop soft shadows. So the plane is lit and its top edge catches
   that light — the same two gradients any other surface has, said in SVG. The
   DRAWING did not change: same shapes, same eight hues, same eyes, and the
   fill still averages the ~46% strength the palette was verified legible at
   (the gradient spends that strength, it does not lower it).

   Two alternatives were drawn beside this one and rejected by the owner: a
   halo in the identity colour (a glow reads as "something is happening", and
   identity never carries state) and a translucent glass pebble (it spends its
   contrast on the bottom edge, which is what a 20px chip can least afford). */
let paintSeq = 0;

export function OwnerAvatarClassic({
  shape = "circle",
  color = "peach",
  state = "idle",
  size = 32,
  title,
}) {
  const hex = avatarColor(color).hex;
  const eyes = eyeStateFor(state);
  // One id per mounted mark: a document holds dozens of these at once, and
  // two <defs> sharing an id is the first paint server winning everywhere.
  const uid = useMemo(() => `ova${++paintSeq}`, []);
  return (
    <svg
      class={`ow-av is-${size} is-${eyes}`}
      viewBox="0 0 32 32"
      width={size}
      height={size}
      style={`--ow-av-c:${hex};--ow-av-fill:url(#${uid}f);--ow-av-edge:url(#${uid}l)`}
      role={title ? "img" : undefined}
      aria-label={title}
      aria-hidden={title ? undefined : "true"}
    >
      <defs>
        <linearGradient id={`${uid}f`} x1="0" y1="0" x2="0" y2="1">
          <stop class="ow-av-f0" offset="0" />
          <stop class="ow-av-f1" offset="1" />
        </linearGradient>
        {/* The lit edge runs out at mid-height rather than at the bottom: a
            highlight faded over the whole outline reads as a thinner rim, not
            as one edge facing the light. */}
        <linearGradient id={`${uid}l`} x1="0" y1="0" x2="0" y2="0.5">
          <stop class="ow-av-l0" offset="0" />
          <stop class="ow-av-l1" offset="1" />
        </linearGradient>
      </defs>
      <path class="ow-av-body" d={SHAPE_PATHS[shape] || SHAPE_PATHS.circle} />
      <path class="ow-av-lit" d={SHAPE_PATHS[shape] || SHAPE_PATHS.circle} />
      <Eyes state={eyes} shape={shape} />
    </svg>
  );
}

export function OwnerAvatarClassicFor({ owner, state, size = 32, title }) {
  const { shape, color } = ownerAvatar(owner);
  return <OwnerAvatarClassic shape={shape} color={color} state={state} size={size} title={title} />;
}
