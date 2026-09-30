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
// reason this exists. The mark is SHAPE × COLOUR × TONE (avatar-identity.js),
// independent axes with 14 × 8 × 3 = 336 combinations, and none carries state.
//
// WHAT IT LOOKS LIKE NOW. The "Serena" face (OwnerFace.jsx, chosen in the
// ?view=faces-fable lab): a flat body in the identity colour and eyes that are
// content and shut (ᵕ ᵕ) at rest. It is ALIVE on purpose — the owner asked for
// faces that blink and look around — and it behaves by state (idle breathes,
// wanders its head and peeks; working opens its eyes on its work; asks looks
// at you; saved is two level lines).
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
  tone,
  state = "idle",
  size = 32,
  seedKey,
  follow,
  title,
}) {
  return (
    <OwnerFace
      shape={shape}
      color={color}
      tone={tone}
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
  const { shape, color, tone } = ownerAvatar(owner);
  return (
    <OwnerAvatar
      shape={shape}
      color={color}
      tone={tone}
      seedKey={owner?.codebase_key || owner?.name}
      state={state}
      size={size}
      title={title}
    />
  );
}
