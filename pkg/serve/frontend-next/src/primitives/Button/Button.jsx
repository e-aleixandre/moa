import "./Button.css";

// Button — botón de acción genérico.
export function Button({
  variant = "solid",
  size = "md",
  type = "button",
  children,
  disabled = false,
  ...rest
}) {
  return (
    <button
      class={`button variant-${variant} size-${size}`}
      type={type}
      disabled={disabled}
      {...rest}
    >
      {children}
    </button>
  );
}
