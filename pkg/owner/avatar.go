package owner

import "slices"

// The owner's identity mark: a shape and a colour, chosen when the owner is
// created and drawn wherever the owner appears (the sidebar's OWNERS section,
// its row inside a project group, the chip a child session wears).
//
// Two axes rather than a monogram because two owners of the same product share
// their initials — "Winerim" and "Winerim Web" both read as "Wi" — and the
// list is scanned by that mark. Neither axis ever carries STATE: the colours
// below contain no amber, red or green, which are the product's state dots
// (tmp/redesign/CRITERIO-VISUAL.md §1). What changes with state is drawn by
// the client on top of the mark, not stored here.
//
// The field is ADDITIVE: an owner.json written before avatars existed has none
// and gets DefaultAvatar, which is derived from CodebaseKey so every client
// draws the same face for the same project without a migration.

// AvatarShapes and AvatarColors are closed lists: anything else is refused on
// create rather than stored and silently dropped by whatever draws it.
var (
	AvatarShapes = []string{"circle", "squircle", "blob", "hexagon", "drop", "pill"}
	AvatarColors = []string{"peach", "mauve", "sage", "sky", "sand", "rose", "mint", "lilac"}
)

// Avatar is the mark. Both fields are required once the value is non-empty;
// a zero Avatar means "never chosen" and resolves to DefaultAvatar.
type Avatar struct {
	Shape string `json:"shape"`
	Color string `json:"color"`
}

// IsZero reports whether nothing was chosen.
func (a Avatar) IsZero() bool { return a.Shape == "" && a.Color == "" }

// Valid reports whether both axes are one of the closed lists.
func (a Avatar) Valid() bool {
	return slices.Contains(AvatarShapes, a.Shape) && slices.Contains(AvatarColors, a.Color)
}

// hash is FNV-1a with a final avalanche, and it is the frontend's hash byte
// for byte (src/components/Owners/OwnerAvatar.jsx). The two must agree, or an
// owner created by one and drawn by the other would change face; the client
// keeps its own copy because it has to draw an owner whose avatar field a
// server of an older build never sent.
//
// The finalizer is not decoration. The first attempt was `h = h*31 + c`, and
// with it "winerim-backend" and "winerim-web" — the exact pair the avatar
// exists to tell apart — came out with the SAME shape AND the same colour:
// that hash leaves the low bits almost untouched by the seed, and both axes
// read the low bits. Mixing the high bits down before the modulo is what makes
// the two seeds independent.
func hash(text string, seed uint32) uint32 {
	h := seed
	for i := 0; i < len(text); i++ {
		h ^= uint32(text[i])
		h *= 16777619
	}
	h ^= h >> 16
	h *= 2246822507
	h ^= h >> 13
	return h
}

// DefaultAvatar is the deterministic mark of a codebase. Hashed from the
// codebase key rather than the name, so renaming an owner does not change its
// face, and with two independent seeds, or shape and colour would march in
// lockstep and the 48 combinations would collapse to 8.
func DefaultAvatar(codebaseKey string) Avatar {
	return Avatar{
		Shape: AvatarShapes[hash(codebaseKey, 2166136261)%uint32(len(AvatarShapes))],
		Color: AvatarColors[hash(codebaseKey, 5381)%uint32(len(AvatarColors))],
	}
}

// ResolvedAvatar is what a client should draw: the chosen mark, or the
// deterministic default when none was chosen or the stored one is not in the
// closed lists any more.
func (o Owner) ResolvedAvatar() Avatar {
	if o.Avatar.Valid() {
		return o.Avatar
	}
	return DefaultAvatar(o.CodebaseKey)
}
