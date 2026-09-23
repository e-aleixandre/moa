import { ownerAvatar } from "./avatar-identity.js";
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
