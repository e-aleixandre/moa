import { useMemo } from "preact/hooks";

// OwnerAvatar — the owner's identity mark, drawn wherever an owner appears:
// the sidebar's OWNERS section, its row inside a project group, the chip a
// child session wears, and the New owner picker.
//
// WHAT IT IS. An owner is a standing thing you recognise across modes: the
// OWNERS section, its group in By project, the chip a child wears. Two owners
// of the same repository ("Winerim" and "Winerim Web") share a monogram, so a
// two-letter tile cannot tell them apart — that is the whole reason this
// exists. The mark is therefore SHAPE × COLOUR, two independent axes with
// 6 × 8 = 48 combinations, and neither of them ever carries state.
//
// WHAT CARRIES STATE. The eyes, and only the eyes. Identity colour ≠ state
// colour (CRITERIO §1): the palette below has no amber, red or green in it,
// because those three are the product's semantic dots and an avatar that
// borrowed one would say "error" by being pink. So the fill says WHO and the
// eyes say WHAT IS HAPPENING — idle looks at you, working looks aside, asks
// raises a brow, saved has them shut.
//
// NO ANIMATION. Not a blink, not a drift. This is a tool that is looked at
// for hours (CRITERIO §5); a face that moves in the corner of the list is a
// permanent interruption. The only motion is the ordinary hover/press of the
// row it sits in, and `prefers-reduced-motion` is respected by construction
// because there is nothing to reduce.

// ── The two identity axes ───────────────────────────────────────────────

export const AVATAR_SHAPES = ["circle", "squircle", "blob", "hexagon", "drop", "pill"];

// The identity palette. EIGHT HUES SPREAD ROUND THE WHEEL, not eight tints of
// the theme: the first attempt derived them from `--peach` / `--mauve` /
// `--sky` / `--teal` / `--lavender` / `--flamingo` and muted each one, which
// put six of the eight inside a 90° arc and left `mauve`/`lilac` 0.011 apart
// in OKLab once mixed into the fill. On a phone that is one colour, and the
// owner said so after using it.
//
// So they are computed rather than picked: one lightness and one chroma
// (oklch L 0.80, C 0.115, clamped to sRGB) at eight hues, chosen to maximise
// the smallest distance between any two of them AFTER the mix with
// `--zl-raised`, while staying clear of the state dots. Measured on the fill
// actually drawn, the closest pair went from dE 0.011 to dE 0.031 — the same
// separation the product already has between two dots you never confuse.
//
// What "clear of the state dots" means (CRITERIO §1): no hue within 28° of
// amber #f9e2af (waiting on you), 18° of red #f38ba8 (error) or 18° of green
// #a6e3a1. Blue and mauve are not vetoed — `azure` and `lilac` do live near
// them — because those two dots are "running" and "unread" rather than alarms,
// and a 7px dot beside a 32px mark with eyes is not read as the same object.
// `sand` is GONE for this reason: it was the amber-ish tile, and amber is the
// one colour the owner named as unusable.
//
// The IDS are unchanged apart from sand→azure, so no owner.json is rewritten;
// a stored `sand` is migrated in pkg/owner/avatar.go and below.
export const AVATAR_COLORS = [
  { id: "peach", hex: "#f6aa73", oklch: "0.80 0.115 56" },
  { id: "mauve", hex: "#d8a8f3", oklch: "0.80 0.115 313" },
  { id: "sage", hex: "#aeca76", oklch: "0.80 0.115 124" },
  { id: "sky", hex: "#4dd3de", oklch: "0.80 0.115 203" },
  { id: "azure", hex: "#6ec9fe", oklch: "0.80 0.115 236" },
  { id: "rose", hex: "#f39fcf", oklch: "0.80 0.115 345" },
  { id: "mint", hex: "#71d5a8", oklch: "0.80 0.115 163" },
  { id: "lilac", hex: "#b2b5ff", oklch: "0.80 0.109 282" },
];

// RENAMED_COLORS is pkg/owner/avatar.go's `renamedAvatarColors`: an id that
// left the list maps onto the surviving colour nearest the hue that owner
// already had, so its face changes as little as the change allows.
const RENAMED_COLORS = { sand: "sage" };

const COLOR_BY_ID = new Map(AVATAR_COLORS.map((c) => [c.id, c]));

/* The outlines, in one 32×32 box so the two sizes are one drawing scaled.
   Hand-written paths rather than a library: six shapes is not a dependency,
   and clip-path would have cost a second definition per shape for the rim.
   The ids are pkg/owner/avatar.go's AvatarShapes, in that order. */
const SHAPE_PATHS = {
  circle: "M16 1.5a14.5 14.5 0 1 1 0 29 14.5 14.5 0 0 1 0-29Z",
  // A cubic superellipse: the corner never becomes a radius, which is what
  // separates it from the rounded square at a glance.
  squircle: "M16 1.5C26 1.5 30.5 6 30.5 16S26 30.5 16 30.5 1.5 26 1.5 16 6 1.5 16 1.5Z",
  blob: "M17.3 1.9c6.3-.5 11.5 3.2 12.9 9.2 1.3 6-1.4 11.7-6.4 15.1-5 3.3-11.5 3-15.4-1.1C4.2 20.8 2.4 14 5.1 8.6 7.5 3.9 11.5 2.4 17.3 1.9Z",
  hexagon: "M16 1.5 28.6 8.75v14.5L16 30.5 3.4 23.25V8.75Z",
  drop: "M16 1.8c5.8 6 11.7 8.4 11.7 15.4a11.7 11.7 0 1 1-23.4 0c0-7 5.9-9.4 11.7-15.4Z",
  pill: "M11.5 6h9a10 10 0 0 1 0 20h-9a10 10 0 0 1 0-20Z",
};

// Where the eyes sit inside each outline. A drop is heavy at the bottom and a
// pill has no top, so a single centre would have put eyes on an edge.
const EYE_CENTER = {
  circle: [16, 15],
  squircle: [16, 15],
  blob: [16.4, 15],
  hexagon: [16, 15.2],
  drop: [16, 18],
  pill: [16, 16],
};

/* ── The one state axis: the eyes ────────────────────────────────────────

   Four drawings, mapped from the owner's own conversation:
     idle    — open, level, looking at you
     working — both pupils moved to one side, STATIC. A held glance reads as
               "busy with something over there" without a single frame of
               motion; a drifting eye would be the animation this refuses.
     asks    — open, plus one raised brow. The brow is the interrogative: a
               "‽" glyph at 20px was a smudge, and a mouth would have made the
               mark a face rather than a token.
     saved   — shut. The same "parked on purpose" the saved session dot says
               by not being drawn at all. */
export const AVATAR_EYE_STATES = ["idle", "working", "asks", "saved"];

// eyeStateFor maps an owner's row state onto the four drawings. `unread` is
// deliberately NOT a fifth face: an unread report is news waiting in the row's
// line, not something the owner is doing, and a face per state would make the
// avatar a second status display competing with the dot.
export function eyeStateFor(state) {
  if (state === "working" || state === "running") return "working";
  if (state === "asks" || state === "permission" || state === "error") return "asks";
  if (state === "saved") return "saved";
  return "idle";
}

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

/* ── Deterministic default ──────────────────────────────────────────────
   A new owner must already look like itself before anyone picks anything, and
   the same project must land on the same mark on every machine. Hashed from
   `codebase_key` (the backend's own identity for a project, shared by every
   worktree of a repository) rather than from the name, so renaming an owner
   does not change its face. Two independent hashes, or shape and colour would
   march in lockstep and the 48 combinations would collapse to 8.

   FNV-1a with a final avalanche, and it is pkg/owner/avatar.go's hash byte for
   byte: the server sends `avatar` on every owner, but this has to agree with
   it anyway, because a server of an older build sends none and both sides then
   compute the face themselves. The finalizer is not decoration — with the
   plain `h*31 + c` it replaced, "winerim-backend" and "winerim-web" (the exact
   pair the mark exists to tell apart) hashed to the same shape AND the same
   colour, because both axes read low bits that the seed barely reaches. */
function hash(text, seed) {
  let h = seed >>> 0;
  const s = String(text || "");
  for (let i = 0; i < s.length; i++) {
    h = (h ^ s.charCodeAt(i)) >>> 0;
    h = Math.imul(h, 16777619) >>> 0;
  }
  h = (h ^ (h >>> 16)) >>> 0;
  h = Math.imul(h, 2246822507) >>> 0;
  h = (h ^ (h >>> 13)) >>> 0;
  return h;
}

export function defaultAvatar(codebaseKey) {
  return {
    shape: AVATAR_SHAPES[hash(codebaseKey, 2166136261) % AVATAR_SHAPES.length],
    color: AVATAR_COLORS[hash(codebaseKey, 5381) % AVATAR_COLORS.length].id,
  };
}

// ownerAvatar reads the owner's chosen `avatar:{shape,color}` (additive in
// owner.json) and falls back to the deterministic default. One function, so
// every surface that draws an owner agrees about its face.
export function ownerAvatar(owner) {
  const fallback = defaultAvatar(owner?.codebase_key || owner?.name);
  const shape = AVATAR_SHAPES.includes(owner?.avatar?.shape) ? owner.avatar.shape : fallback.shape;
  const stored = owner?.avatar?.color;
  const color = COLOR_BY_ID.has(stored)
    ? stored
    : (RENAMED_COLORS[stored] || fallback.color);
  return { shape, color };
}

/* ── The component ─────────────────────────────────────────────────────── */

// size — 32 in a row, 20 in a chip. Only two, because the mark is a token and
// a third size would be a third set of eye positions to keep honest.
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

export function OwnerAvatar({
  shape = "circle",
  color = "peach",
  state = "idle",
  size = 32,
  title,
}) {
  const hex = (COLOR_BY_ID.get(color) || AVATAR_COLORS[0]).hex;
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

// OwnerAvatarFor is the one-argument form every surface actually calls.
export function OwnerAvatarFor({ owner, state, size = 32, title }) {
  const { shape, color } = ownerAvatar(owner);
  return <OwnerAvatar shape={shape} color={color} state={state} size={size} title={title} />;
}
