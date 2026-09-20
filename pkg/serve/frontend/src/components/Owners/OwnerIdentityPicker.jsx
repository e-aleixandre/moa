import { AVATAR_COLORS, AVATAR_SHAPES, OwnerAvatar } from "./OwnerAvatar.jsx";

/* OwnerIdentityPicker — the two rows that choose a face, in their own file.

   It lives apart from Owners.jsx because BOTH the New owner form and the Edit
   owner sheet draw it, and the edit sheet is imported by Owners.jsx: leaving
   the picker there made the two files import each other, and a cycle in a
   module graph is not a style question — under the test runner Owners.jsx was
   evaluated half-initialised and three unrelated palette tests failed. One
   definition, one direction. */
/* ── The identity picker ──────────────────────────────────────────────────
   The one new thing in the form: the face, above the two rows that change it.
   Preview first and large, because what you are choosing is what you will see
   in the list for months; the rows under it are swatches at the touch floor
   (44px), not a dropdown — six shapes and eight colours are fewer decisions
   than a menu costs to open.

   The eyes in the preview are the idle ones. The picker is not a place to
   show states: it is where you choose the half of the mark that never
   changes. */
export function OwnerIdentityPicker({ name, shape, color, onShape, onColor }) {
  return (
    <div class="ow-idp">
      <div class="ow-idp-preview">
        <OwnerAvatar shape={shape} color={color} state="idle" size={64} />
        <span class="ow-idp-name">{name || "New owner"}</span>
      </div>
      <div class="ow-idp-field">
        <span class="ow-idp-label">Shape</span>
        <div class="ow-idp-row is-shapes" role="radiogroup" aria-label="Avatar shape">
          {AVATAR_SHAPES.map((s) => (
            <button
              type="button"
              role="radio"
              aria-checked={s === shape}
              aria-label={s}
              class={`ow-swatch${s === shape ? " is-on" : ""}`}
              key={s}
              onClick={() => onShape(s)}
            >
              <OwnerAvatar shape={s} color={color} state="idle" size={32} />
            </button>
          ))}
        </div>
      </div>
      <div class="ow-idp-field">
        <span class="ow-idp-label">Colour</span>
        <div class="ow-idp-row is-colours" role="radiogroup" aria-label="Avatar colour">
          {AVATAR_COLORS.map((c) => (
            <button
              type="button"
              role="radio"
              aria-checked={c.id === color}
              aria-label={c.id}
              class={`ow-swatch is-colour${c.id === color ? " is-on" : ""}`}
              key={c.id}
              onClick={() => onColor(c.id)}
            >
              <span class="ow-swatch-c" style={`--ow-av-c:${c.hex}`} aria-hidden="true" />
            </button>
          ))}
        </div>
      </div>
    </div>
  );
}
