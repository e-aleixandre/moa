import "./Spinner.css";

// Spinner — small spinning ring to signal work in progress. Extracts and
// tokenizes the pattern that was already duplicated in AgentTray (agent-spinner):
// here it gains a color variant (blue/sky/teal/mauve) to tell apart several
// parallel subagents without inventing a new pattern for each place that needs
// it (FanoutBlock, AgentTray, tool tickers, etc.).
const COLORS = ["blue", "sky", "teal", "mauve"];

const DEFAULT_SIZE = 11;

export function Spinner({ color = "blue", size = DEFAULT_SIZE, label, ...rest }) {
  const safe = COLORS.includes(color) ? color : "blue";
  const a11y = label
    ? { role: "img", "aria-label": label }
    : { "aria-hidden": "true" };
  return (
    <span
      class={`spinner c-${safe}`}
      style={{ width: `${size}px`, height: `${size}px` }}
      {...a11y}
      {...rest}
    />
  );
}
