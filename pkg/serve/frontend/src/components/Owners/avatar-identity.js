// avatar-identity — WHO an owner is, as data: the two identity axes (shape ×
// colour), the deterministic default and the state mapping the face reads.
// No drawing lives here. It is its own module because both drawings import it
// (OwnerAvatar and OwnerFace, which OwnerAvatar now renders): kept inside
// OwnerAvatar.jsx the two files would import each other, and a module cycle
// here is the one that once left Owners.jsx half-initialised under the test
// runner (see OwnerIdentityPicker.jsx). pkg/owner/avatar.go is the other side
// of the contract; nothing here may change without it.

// ── The two identity axes ───────────────────────────────────────────────

// DEFAULT_AVATAR_SHAPES is the pool the deterministic default hashes over, and
// it must never change: the default is `hash % length`, so appending to it
// would silently re-face every owner that never chose one. Shapes added later
// are selectable only (AVATAR_SHAPES), never part of the pool. Both lists are
// pkg/owner/avatar.go's DefaultAvatarShapes / AvatarShapes, in that order.
export const DEFAULT_AVATAR_SHAPES = ["circle", "squircle", "blob", "hexagon", "drop", "pill"];
export const AVATAR_SHAPES = [...DEFAULT_AVATAR_SHAPES, "triangle", "cloud"];

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

// avatarColor is the palette entry for an id, falling back to the first so a
// drawing never has no colour.
export function avatarColor(id) {
  return COLOR_BY_ID.get(id) || AVATAR_COLORS[0];
}

/* The outlines, in one 32×32 box so the two sizes are one drawing scaled.
   Hand-written paths rather than a library: eight shapes is not a dependency,
   and clip-path would have cost a second definition per shape for the rim.
   The ids are pkg/owner/avatar.go's AvatarShapes, in that order. */
export const SHAPE_PATHS = {
  circle: "M16 1.5a14.5 14.5 0 1 1 0 29 14.5 14.5 0 0 1 0-29Z",
  // A cubic superellipse: the corner never becomes a radius, which is what
  // separates it from the rounded square at a glance.
  squircle: "M16 1.5C26 1.5 30.5 6 30.5 16S26 30.5 16 30.5 1.5 26 1.5 16 6 1.5 16 1.5Z",
  blob: "M17.3 1.9c6.3-.5 11.5 3.2 12.9 9.2 1.3 6-1.4 11.7-6.4 15.1-5 3.3-11.5 3-15.4-1.1C4.2 20.8 2.4 14 5.1 8.6 7.5 3.9 11.5 2.4 17.3 1.9Z",
  hexagon: "M16 1.5 28.6 8.75v14.5L16 30.5 3.4 23.25V8.75Z",
  drop: "M16 1.8c5.8 6 11.7 8.4 11.7 15.4a11.7 11.7 0 1 1-23.4 0c0-7 5.9-9.4 11.7-15.4Z",
  pill: "M11.5 6h9a10 10 0 0 1 0 20h-9a10 10 0 0 1 0-20Z",
  // Selectable only (not in the default pool). A triangle with softened
  // corners, and a cloud whose flat base gives the eyes somewhere to sit.
  triangle: "M13.2 5.4Q16 .9 18.8 5.4L29.3 23.4Q31.9 28 26.6 28H5.4Q.1 28 2.7 23.4Z",
  cloud: "M9.2 27.2A6.4 6.4 0 0 1 7.7 14.6 8.4 8.4 0 0 1 24 12.1 7.5 7.5 0 0 1 24.4 27.2Z",
};

// Where the eyes sit inside each outline. A drop is heavy at the bottom and a
// pill has no top, so a single centre would have put eyes on an edge.
export const EYE_CENTER = {
  circle: [16, 15],
  squircle: [16, 15],
  blob: [16.4, 15],
  hexagon: [16, 15.2],
  drop: [16, 18],
  pill: [16, 16],
  // Both are heavy at the bottom, like the drop.
  triangle: [16, 19.4],
  cloud: [16.4, 19],
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
export function hash(text, seed) {
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
    shape: DEFAULT_AVATAR_SHAPES[hash(codebaseKey, 2166136261) % DEFAULT_AVATAR_SHAPES.length],
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

