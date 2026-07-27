import "./ThinkingMeter.css";

// ThinkingMeter — a single component with 3 switchable variants (bars |
// dial | glyph) so we can compare which one we like before settling on one.
const LEVELS = ["off", "low", "medium", "high", "xhigh"];

function levelToFilled(level) {
  const idx = LEVELS.indexOf(level);
  return idx < 0 ? 0 : idx;
}

function Bars({ filled, hot, a11y }) {
  return (
    <span class={`thinking-meter variant-bars${hot ? " hot" : ""}`} {...a11y}>
      {[0, 1, 2, 3].map((i) => (
        <i key={i} class={i < filled ? "on" : ""} />
      ))}
    </span>
  );
}

function Dial({ filled, hot, a11y }) {
  return (
    <span
      class={`thinking-meter variant-dial level-${filled}${hot ? " hot" : ""}`}
      {...a11y}
    />
  );
}

function Glyph({ filled, hot, a11y }) {
  return (
    <span class={`thinking-meter variant-glyph${hot ? " hot" : ""}`} {...a11y}>
      {[0, 1, 2, 3].map((i) => (
        <span key={i} class={i < filled ? "filled" : "empty"} aria-hidden="true">
          {i < filled ? "▮" : "▯"}
        </span>
      ))}
    </span>
  );
}

export function ThinkingMeter({
  level = "off",
  variant = "bars",
  hot = false,
  label,
  decorative = false,
  ...rest
}) {
  const filled = levelToFilled(level);
  // Accessible name: hidden when the consumer already exposes the level in text
  // (decorative), otherwise a sensible default like "Thinking: high".
  const a11y = decorative
    ? { "aria-hidden": "true" }
    : { role: "img", "aria-label": label || `Thinking: ${level}` };
  const props = { filled, hot, a11y, ...rest };
  if (variant === "dial") return <Dial {...props} />;
  if (variant === "glyph") return <Glyph {...props} />;
  return <Bars {...props} />;
}
