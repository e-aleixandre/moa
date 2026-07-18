import "./Chip.css";

// Chip — small generic pill. Base for tray chips, mobile strip, tags.
// Accepts any child (e.g. a StateDot) and optionally a semantic tone
// and a size ("md" by default, "sm" compact).
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
