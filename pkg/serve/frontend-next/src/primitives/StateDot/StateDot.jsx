import "./StateDot.css";

// StateDot — session state dot. Base atom reused by chips, mobile strips,
// headers, etc.
const STATES = ["idle", "running", "permission", "error", "saved"];

// Default 8px = --space-2. Passed as a number to allow arbitrary sizes in
// context, but the default value comes from the spacing scale.
const DEFAULT_SIZE = 8;

export function StateDot({ state = "idle", size = DEFAULT_SIZE, label, ...rest }) {
  const safe = STATES.includes(state) ? state : "saved";
  // On its own, a color-only dot is not accessible: if the consumer gives a
  // label we expose it; otherwise it's decorative (the state is usually already
  // in text next to it).
  const a11y = label
    ? { role: "img", "aria-label": label }
    : { "aria-hidden": "true" };
  return (
    <span
      class={`state-dot ${safe}`}
      style={{ width: `${size}px`, height: `${size}px` }}
      {...a11y}
      {...rest}
    />
  );
}
