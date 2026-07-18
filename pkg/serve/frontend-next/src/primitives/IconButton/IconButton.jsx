import "./IconButton.css";

// IconButton — botón cuadrado solo-icono (headers: rewind, settings, etc.)
// `children` es un icono de lucide-preact; `label` alimenta el aria-label.
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
