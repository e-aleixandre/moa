import "./Chip.css";

// Chip — píldora genérica pequeña. Base para tray chips, strip móvil, tags.
// Acepta cualquier hijo (p.ej. un StateDot) y opcionalmente un tono semántico
// y un tamaño ("md" por defecto, "sm" compacto).
export function Chip({ children, tone, size = "md", mono = false, onClick, ...rest }) {
  const classes = ["chip"];
  if (tone) classes.push(`tone-${tone}`);
  if (size !== "md") classes.push(`size-${size}`);
  if (mono) classes.push("mono");

  if (onClick) {
    return (
      <button type="button" class={classes.join(" ")} onClick={onClick} {...rest}>
        {children}
      </button>
    );
  }
  return (
    <span class={classes.join(" ")} {...rest}>
      {children}
    </span>
  );
}
