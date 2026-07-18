import "./Chip.css";

// Chip — píldora genérica pequeña. Base para tray chips, strip móvil, tags.
// Acepta cualquier hijo (p.ej. un StateDot) y opcionalmente un tono semántico.
export function Chip({ children, tone, mono = false, onClick, ...rest }) {
  const classes = ["chip"];
  if (tone) classes.push(`tone-${tone}`);
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
