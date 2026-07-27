import { X } from "lucide-preact";
import { StateDot } from "../../primitives/index.js";
import "./SessionRow.css";

// SessionRow — the session piece, a single component with 3 switchable
// variants (pill | tab | card) to compare directions A/B/C live,
// just like ThinkingMeter with `variant`.
//
// `state` "permission" and "error" tint the whole row ("needs you"), not just the
// dot: permission uses yellow (like the mockup), error uses the same pattern
// in red to keep the system's traffic-light convention.
const NEEDS_TONE = {
  permission: "yellow",
  error: "red",
};

// Suffix added to the button's accessible name when the state is relevant
// for screen reader users (not just color/icon).
const STATE_LABEL_SUFFIX = {
  permission: ", requires permission",
  error: ", error",
};

// The card variant takes three OPTIONAL extras, all additive — omit them and the
// card renders exactly as it always has:
//   when  — short age, right-aligned on the title row ("now", "18m")
//   brief — one line of live status under the title. Renderable, not just text,
//           so a caller can bold a lead-in (<><b>Needs you: </b>…</>)
//   path  — the session's working directory, the last and quietest line
export function SessionRow({
  title,
  state = "idle",
  variant = "card",
  active = false,
  unseen = false,
  meta,
  age,
  pane,
  when,
  brief,
  path,
  onClick,
  onClose,
  ...rest
}) {
  const needs = NEEDS_TONE[state];
  const classes = [
    "session-row",
    `variant-${variant}`,
    active ? "on" : "",
    needs ? `needs-${needs}` : "",
  ]
    .filter(Boolean)
    .join(" ");

  const handleClose = (event) => {
    event.stopPropagation();
    onClose?.(event);
  };

  const hitLabel = `${title}${pane ? `, pane ${pane}` : ""}${STATE_LABEL_SUFFIX[state] ?? ""}`;

  return (
    <span class={classes} {...rest}>
      <button
        type="button"
        class="session-row-hit"
        onClick={onClick}
        aria-current={active ? "true" : undefined}
        aria-label={hitLabel}
      >
        {variant === "card" ? (
          <>
            <span class="r1">
              <StateDot state={state} size={8} />
              <span class="title" aria-hidden="true">{title}</span>
              {pane && <span class="pane" aria-hidden="true">{pane}</span>}
              {unseen && <span class="unseen" aria-hidden="true" />}
              {when && <span class="when" aria-hidden="true">{when}</span>}
            </span>
            {meta && <span class="r2" aria-hidden="true">{meta}</span>}
            {brief && <span class="brief" aria-hidden="true">{brief}</span>}
            {path && <span class="path" aria-hidden="true">{path}</span>}
          </>
        ) : (
          <>
            <StateDot state={state} size={8} />
            <span class="title" aria-hidden="true">{title}</span>
            {unseen && <span class="unseen" aria-hidden="true" />}
            {variant === "tab" && age && <span class="n" aria-hidden="true">{age}</span>}
          </>
        )}
      </button>
      {onClose && (
        <button
          type="button"
          class="x"
          aria-label={`Close ${title}`}
          onClick={handleClose}
        >
          <X size={11} />
        </button>
      )}
    </span>
  );
}
