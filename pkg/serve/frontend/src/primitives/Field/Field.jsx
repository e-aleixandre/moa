import "./Field.css";

// Field — the text input primitive. It did not exist: 18 raw <input>/<textarea>
// were scattered across the app, each re-inventing the border, the background
// and the iOS zoom guard. That is not adoption drift, it is a missing piece.
//
// Two shapes, because the codebase already has exactly two:
//   - `box`   — a bordered field on its own (Live Preview URL, secret batch)
//   - `inset` — a recessed well, usually with a leading icon (drawer search,
//               command palette, Spine search)
//
// The leading/trailing slots exist because every search field in the app pairs
// the input with an icon, and doing that by hand is how the well ended up with
// four different paddings.
//
// The font-size floor is NOT a prop: --text-input (16px) is a hard constraint,
// since iOS Safari zooms the page when focusing anything smaller. A field that
// let a caller shrink it would be a field that lets a caller break the PWA.
export function Field({
  variant = "box",
  size = "md",
  leading,
  trailing,
  mono = false,
  // Both spellings land on the wrapper. Preact callers write `class`, and if it
  // fell through to ...rest it would silently style the inner input instead --
  // the wrapper would lose its layout and the input would grow a second box.
  class: klass = "",
  className = "",
  inputRef,
  as = "input",
  ...rest
}) {
  const Tag = as;
  const classes = [
    "field",
    `field-${variant}`,
    `field-${size}`,
    mono ? "field-mono" : "",
    klass,
    className,
  ]
    .filter(Boolean)
    .join(" ");
  return (
    <div class={classes}>
      {leading && <span class="field-affix field-leading" aria-hidden="true">{leading}</span>}
      <Tag class="field-input" ref={inputRef} {...rest} />
      {trailing && <span class="field-affix field-trailing">{trailing}</span>}
    </div>
  );
}
