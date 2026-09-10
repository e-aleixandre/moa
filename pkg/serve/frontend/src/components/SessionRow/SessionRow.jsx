import { Import, X } from "lucide-preact";
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
//   briefTone — which state that line is about ("yellow" | "red" | "mauve" |
//           "neutral"), so the reason is coloured by what it says rather than
//           by a class on the whole row
//   mono  — the project monogram {text, hue} for the leading square
//   path  — the session's working directory, the last and quietest line
//   origin — who started the session when it wasn't you ("automation", or the
//           label the caller passed). Omitted for ordinary user sessions.
const isEventOrigin = (origin) => typeof origin === "string" && origin.startsWith("event:");

// Monogram — the project's identity, as two letters on a tinted square. The
// caller passes the already-derived {text, hue} (data/util/format.js
// projectMonogram) rather than a cwd, so the row stays a pure presentation
// piece and the hash lives with the other project helpers.
//
// Hue only: the surface is hsl(h 50% 60% / .16) and the ink hsl(h 65% 78%),
// so every project lands on the same lightness and none of them can shout
// louder than the state colours next to it.
function Monogram({ mono }) {
  return (
    <span class="mono" style={`--mono-h:${mono.hue}`} aria-hidden="true">
      {mono.text}
    </span>
  );
}

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
  origin,
  brief,
  briefTone,
  mono,
  path,
  onClick,
  onClose,
  ...rest
}) {
  const needs = NEEDS_TONE[state];
  const isUnseenResult = unseen && (state === "idle" || state === "running" || state === "unseen");
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

  const hitLabel = `${title}${origin ? `, started by ${origin}` : ""}${pane ? `, pane ${pane}` : ""}${isUnseenResult ? ", new result" : STATE_LABEL_SUFFIX[state] ?? ""}`;

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
          /* The card is a LIST ROW, not a stack of lines: the monogram is a
             fixed leading column and the two text lines share the remaining
             width. That is what lets the ages line up down the list and what
             gives the reason a full line to be read on. */
          <>
            {mono && <Monogram mono={mono} />}
            <span class="body">
              <span class="r1">
                <span class="title" aria-hidden="true">{title}</span>
                {/* An event origin is shown as the same Import mark the event
                    block uses in the transcript: the source name never fit in a
                    7em badge, and "event:a…" told the owner nothing. */}
                {origin && (isEventOrigin(origin)
                  ? <span class="origin-event" aria-hidden="true"><Import size={12} /></span>
                  : <span class="origin" aria-hidden="true">{origin}</span>)}
                {pane && <span class="pane" aria-hidden="true">{pane}</span>}
                {/* State and age travel together at the end of the title line:
                    both are metadata about the row, and pinning the age to a
                    fixed width lands every dot on the same x. Ragged dots read
                    as a wobble down the list. */}
                <span class="edge">
                  <StateDot state={isUnseenResult ? "unseen" : state} size={7} />
                  {when && <span class="when" aria-hidden="true">{when}</span>}
                </span>
              </span>
              {meta && <span class="r2" aria-hidden="true">{meta}</span>}
              {brief && <span class={`brief${briefTone ? ` tone-${briefTone}` : ""}`} aria-hidden="true">{brief}</span>}
              {path && <span class="path" aria-hidden="true">{path}</span>}
            </span>
          </>
        ) : (
          <>
            <StateDot state={isUnseenResult ? "unseen" : state} size={8} />
            <span class="title" aria-hidden="true">{title}</span>
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
