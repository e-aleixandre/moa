import "./Button.css";

// Button — botón de acción genérico.
export function Button({
  variant = "solid",
  size = "md",
  type = "button",
  className = "",
  children,
  disabled = false,
  ...rest
}) {
  const classes = [`button`, `variant-${variant}`, `size-${size}`, className]
    .filter(Boolean)
    .join(" ");
  return (
    <button class={classes} type={type} disabled={disabled} {...rest}>
      {children}
    </button>
  );
}
