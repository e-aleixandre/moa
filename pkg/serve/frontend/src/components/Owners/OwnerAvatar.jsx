import { useMemo } from "preact/hooks";
import {
  EYE_CENTER, SHAPE_PATHS, avatarColor, eyeStateFor, ownerAvatar,
} from "./avatar-identity.js";
import { OwnerFace } from "./OwnerFace.jsx";

// OwnerAvatar — the owner's identity mark, drawn wherever an owner appears:
// the sidebar's OWNERS section, its row inside a project group, the side
// panel's header, the chip a child session wears, the phone's empty-state
// grid and the New/Edit owner picker.
//
// WHAT IT IS. An owner is a standing thing you recognise across modes. Two
// owners of the same repository ("Winerim" and "Winerim Web") share a
// monogram, so a two-letter tile cannot tell them apart — that is the whole
// reason this exists. The mark is SHAPE × COLOUR (avatar-identity.js), two
// independent axes with 6 × 8 = 48 combinations, and neither carries state.
//
// WHAT IT LOOKS LIKE NOW. The "Mirada" face (OwnerFace.jsx, chosen in the
// ?view=faces lab): a flat body in the identity colour and two short white
// strokes for eyes. It is ALIVE on purpose — the owner asked for faces that
// blink and look around — and the eyes behave by state (idle breathes and
// looks around, working narrows on its work, asks looks at you, saved rests).
// That is a complement to the words on the row, never a replacement: the
// row's dot and its lead clause still say the state.
//
// What keeps a moving face bearable in a list looked at for hours: one shared
// timer that sleeps between events (faceMotion.js), nothing moves offscreen,
// in a hidden tab or under prefers-reduced-motion, and each owner has its own
// slow rhythm so a column never twitches in unison.

export * from "./avatar-identity.js";

export function OwnerAvatar({
  shape = "circle",
  color = "peach",
  state = "idle",
  size = 32,
  seedKey,
  follow,
  title,
}) {
  return (
    <OwnerFace
      variant="mirada"
      shape={shape}
      color={color}
      seedKey={seedKey}
      state={state}
      size={size}
      follow={follow}
      title={title}
    />
  );
}

// OwnerAvatarFor is the one-argument form every surface actually calls. The
// owner's codebase_key seeds its rhythm, so it blinks the same way everywhere.
export function OwnerAvatarFor({ owner, state, size = 32, title }) {
  const { shape, color } = ownerAvatar(owner);
  return (
    <OwnerAvatar
      shape={shape}
      color={color}
      seedKey={owner?.codebase_key || owner?.name}
      state={state}
      size={size}
      title={title}
    />
  );
}

/* ── The previous mark, kept for comparison ──────────────────────────────
   The static lit tile the product drew before Mirada. No surface renders it;
   the design labs show it as "Antes" beside the face that replaced it, and
   the B/C proposals in the faces lab still wear its sheet (OwnerAvatar.css). */

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
