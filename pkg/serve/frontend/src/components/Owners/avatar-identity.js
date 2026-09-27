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
export const AVATAR_SHAPES = [
  ...DEFAULT_AVATAR_SHAPES, "triangle", "cloud", "flower", "ghost", "bean", "diamond", "shield", "bell",
];

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
//
// `dark` and `pale` are the chroma each hue can carry at the two extra tones
// (AVATAR_TONES below) without leaving sRGB, measured rather than guessed: a
// chroma the screen cannot show is clipped per channel by the browser, which
// is what turns a dark orange into brown.
export const AVATAR_COLORS = [
  { id: "peach", hex: "#f6aa73", oklch: "0.80 0.115 56", dark: 0.146, pale: 0.102 },
  { id: "mauve", hex: "#d8a8f3", oklch: "0.80 0.115 313", dark: 0.17, pale: 0.11 },
  { id: "sage", hex: "#aeca76", oklch: "0.80 0.115 124", dark: 0.148, pale: 0.11 },
  { id: "sky", hex: "#4dd3de", oklch: "0.80 0.115 203", dark: 0.102, pale: 0.11 },
  { id: "azure", hex: "#6ec9fe", oklch: "0.80 0.115 236", dark: 0.128, pale: 0.092 },
  { id: "rose", hex: "#f39fcf", oklch: "0.80 0.115 345", dark: 0.17, pale: 0.11 },
  { id: "mint", hex: "#71d5a8", oklch: "0.80 0.115 163", dark: 0.128, pale: 0.11 },
  { id: "lilac", hex: "#b2b5ff", oklch: "0.80 0.109 282", dark: 0.17, pale: 0.08 },
];

/* ── The tone: the same hue at three lightnesses ─────────────────────────
   pkg/owner/avatar.go's AvatarTones. Absent means `deep`, the body every
   owner had before tones existed, so a stored avatar without one is drawn
   exactly as before. No new hue is added, so no tone can land on a state
   colour; and there is no grey tone, because a grey owner reads as the
   muted, parked mark.

     deep — oklch L 0.68, C 0.12: today's body, white eyes (eye/body ≈ 2.8:1).
     dark — L 0.60 at the most chroma the hue carries (≤ 0.17), white eyes
            (≈ 4:1). The first try (L 0.50, C 0.11) turned peach into brown:
            dark orange with little chroma IS brown, so the fix is chroma,
            not a different hue. It also sank into the sidebar once saved's
            opacity was applied, which is why a saved dark face steps back
            less (OwnerFace.css) — measured to keep the same silhouette
            contrast as a saved deep one (≈ 3.1–3.5:1 on --zl-sheet).
     pale — L 0.84, C ≤ 0.11, INK eyes (≈ 10:1). White eyes cannot stand on
            a pale body; ink ones are the same four drawings in a colour
            that can. The closest pale pair (mauve/lilac, ΔE 0.058) is about
            as far apart as the closest deep pair (0.064). */
export const AVATAR_TONES = ["deep", "dark", "pale"];
const TONE_L = { deep: 0.68, dark: 0.6, pale: 0.84 };

// avatarTone answers the tone to draw: itself while it is listed, `deep`
// otherwise (absent, or written by a build that knows more tones).
export function avatarTone(tone) {
  return AVATAR_TONES.includes(tone) ? tone : "deep";
}

// bodyOklch is the body the face wears, as the three oklch numbers. `deep`
// keeps today's exact value (0.68 0.12 h).
export function bodyOklch(colorId, tone) {
  const c = avatarColor(colorId);
  const h = Number(c.oklch.split(" ")[2]);
  const t = avatarTone(tone);
  return [TONE_L[t], t === "deep" ? 0.12 : c[t], h];
}

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
  // Selectable only too. Each one changes the SILHOUETTE, which is what still
  // reads at 20px: seven lobes, a wavy hem, a dent on top, a point on four
  // sides, a flat top over a point, a flared base.
  flower: flowerPath(),
  ghost: "M16 2.5C23.2 2.5 28 7.8 28 15v13.4q-2-2.8-4 0t-4 0-4 0-4 0-4 0-4 0V15C4 7.8 8.8 2.5 16 2.5Z",
  bean: "M10.4 4.3C14.6 2.8 17.4 6 21.6 5.4 26.8 4.8 30.2 9.4 29.8 15.6 29.4 23.4 23.8 28.6 16 28.6 7.6 28.6 2.4 23.2 2.4 15.2 2.4 9.6 5.8 6 10.4 4.3Z",
  diamond: "M13.9 2.9Q16 .8 18.1 2.9L29.1 13.9Q31.2 16 29.1 18.1L18.1 29.1Q16 31.2 13.9 29.1L2.9 18.1Q.8 16 2.9 13.9Z",
  shield: "M4 5.6Q4 3 6.6 3H25.4Q28 3 28 5.6V15C28 22.4 22.2 27.6 16 30.2 9.8 27.6 4 22.4 4 15Z",
  bell: "M16 2.4C22 2.4 24.6 7.4 24.6 13V18.6C24.6 22.4 28.6 23.8 28.6 26.6 28.6 28.3 27.4 29 25.6 29H6.4C4.6 29 3.4 28.3 3.4 26.6 3.4 23.8 7.4 22.4 7.4 18.6V13C7.4 7.4 10 2.4 16 2.4Z",
};

// A scalloped outline, sampled from r(θ) rather than written by hand: seven
// shallow lobes, so it is a flower and not a gear.
function flowerPath() {
  const pts = [];
  for (let i = 0; i < 84; i++) {
    const t = (i / 84) * Math.PI * 2;
    const r = 12.6 + 1.9 * Math.cos(7 * t);
    pts.push(`${(16 + r * Math.sin(t)).toFixed(2)} ${(16.4 - r * Math.cos(t)).toFixed(2)}`);
  }
  return `M${pts.join("L")}Z`;
}

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
  flower: [16, 15.4],
  ghost: [16, 14],
  bean: [16, 16],
  diamond: [16, 15],
  shield: [16, 13.6],
  // The bell's head is narrow and its flare is below: eyes in the head.
  bell: [16, 12.6],
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
  return { shape, color, tone: avatarTone(owner?.avatar?.tone) };
}

// storedAvatar is what a client sends: the default tone is left out, so an
// owner.json only grows a `tone` when one was actually chosen.
export function storedAvatar({ shape, color, tone }) {
  return avatarTone(tone) === "deep" ? { shape, color } : { shape, color, tone };
}

/* ── New owner's proposal: a face nobody has yet ─────────────────────────
   The deterministic default is a hash, and a hash repeats: with a dozen
   owners it had already drawn three mauve drops. New owner therefore proposes
   a combination no existing owner uses, preferring the ones that stand
   furthest from ALL of them: for each candidate, its distance to the nearest
   existing face (a different shape counts 1; the body colours, tone
   included, count their OKLab distance, saturating at 1 from ΔE 0.12 — the
   gap between two hues nobody confuses). Highest nearest-distance wins; ties
   go to the one furthest from the rest in total, then to a hash of the
   folder, so the proposal is stable while the dialog is open and differs
   from one project to the next. It is only a proposal: the user can change
   any axis. With no owners yet, or with every combination taken, it is
   today's default. */
const bodyLab = (a) => {
  const [l, c, h] = bodyOklch(a.color, a.tone);
  const rad = (h * Math.PI) / 180;
  return [l, c * Math.cos(rad), c * Math.sin(rad)];
};
const faceDistance = (a, b) => {
  const la = bodyLab(a);
  const lb = bodyLab(b);
  const de = Math.hypot(la[0] - lb[0], la[1] - lb[1], la[2] - lb[2]);
  return (a.shape === b.shape ? 0 : 1) + Math.min(1, de / 0.12);
};
const faceKey = (a) => `${a.shape}:${a.color}:${avatarTone(a.tone)}`;

export function proposeAvatar(existing, seedKey) {
  const fallback = { ...defaultAvatar(seedKey), tone: "deep" };
  const taken = (existing || []).map((a) => ({ ...a, tone: avatarTone(a.tone) }));
  if (taken.length === 0) return fallback;
  const used = new Set(taken.map(faceKey));
  let best = null;
  for (const shape of AVATAR_SHAPES) {
    for (const { id: color } of AVATAR_COLORS) {
      for (const tone of AVATAR_TONES) {
        const c = { shape, color, tone };
        const key = faceKey(c);
        if (used.has(key)) continue;
        let near = Infinity;
        let sum = 0;
        for (const t of taken) {
          const d = faceDistance(c, t);
          near = Math.min(near, d);
          sum += d;
        }
        const score = [Math.round(near * 1000), Math.round(sum * 1000), hash(`${seedKey}|${key}`, 2166136261)];
        if (!best || score[0] > best.score[0]
          || (score[0] === best.score[0] && (score[1] > best.score[1]
            || (score[1] === best.score[1] && score[2] > best.score[2])))) {
          best = { c, score };
        }
      }
    }
  }
  return best ? best.c : fallback;
}

