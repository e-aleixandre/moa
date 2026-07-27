import "./IconButton.css";

// IconButton — square icon-only button (headers: rewind, settings, etc.)
// `children` is a lucide-preact icon; `label` feeds the aria-label.
export function IconButton({
  children,
  label,
  onClick,
  disabled = false,
  variant = "ghost",
  ...rest
}) {
  return (
    <button
      class={`icon-button variant-${variant}`}
      type="button"
      aria-label={label}
      title={label}
      onClick={onClick}
      disabled={disabled}
      {...rest}
    >
      {children}
    </button>
  );
}
