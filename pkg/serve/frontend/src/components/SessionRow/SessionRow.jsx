import { Import, X } from "lucide-preact";
import "./SessionRow.css";

// SessionRow — the session piece. Markup and CSS are the catalogue's
// (catalog/zones-lab.jsx:99 `Row`, zones-lab.css:884 `.zl-row`), MOVED here
// rather than imitated: the class names travelled with the rules, so the sheet
// is the accepted design rather than a translation of it. The catalogue now
// imports this component (catalog/zones-lab.jsx), which is what makes one
// definition instead of two.
//
// What is NOT the catalogue's is everything the prototype never had, and it is
// re-grafted on top: the accessible name, the close button that stays tabbable,
// the origin mark, the pane badge, the unseen state and the focus ring.
//
// The variants (pill | tab) the old imitation carried are gone with it: nothing
// mounted them but the molecules gallery, and the catalogue only ever drew one
// row.

// `state` "permission" and "error" tint the whole row ("needs you"), not just
// the dot. Kept as a class the row's own sheet can key on, because the
// catalogue's reason line is coloured by what it says (`is-permission`) while
// a caller with no tone still gets the old row-level tinting.
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

// The row takes these OPTIONAL extras, all additive — omit them and the row
// renders as a single line:
//   when  — short age, right-aligned on the title row ("now", "18m")
//   brief — one line of live status under the title. Renderable, not just text,
//           so a caller can bold a lead-in (<><b>Needs you: </b>…</>)
//   briefTone — which state that line is about ("yellow" | "red" | "mauve" |
//           "neutral"), so the reason is coloured by what it says rather than
//           by a class on the whole row
//   path  — the session's working directory, the last and quietest line
//   project — the project's name, for a row whose second line is a brief and
//           so has no path: it sits at the end of that line, under the age.
//           A row that shows its path does not need it twice.
//   origin — who started the session when it wasn't you ("automation", or the
//           label the caller passed). Omitted for ordinary user sessions.
const isEventOrigin = (origin) => typeof origin === "string" && origin.startsWith("event:");

// There is no leading project square. The row used to open with a two-letter
// monogram on a tinted tile, measured against the owner's real list: 25
// projects on 6 hues and 2 letters gave 3 colliding marks (`de` was both dev
// and design-visual, in the same hue), so the tile could not be trusted to
// name a project. Where the row prints the path it was redundant; where it
// does not (a working session says what it is doing) it was wrong often
// enough not to be relied on. It cost 44px of a 272px column, which the
// title has back.

// Dot — the state mark. The catalogue's own span with a state class, not the
// StateDot primitive: the halo, the size and the "idle is not drawn" rule are
// all in .zl-dot, and StateDot writes its size as an inline style no sheet can
// win against — which is how the 9px Ambient asked for never applied.
//
// The catalogue names the "needs you" state `needs`; production calls it
// `permission`. The class is production's, so one vocabulary reaches the CSS.
export function Dot({ state }) {
  return <span class={`zl-dot is-${state}`} aria-hidden="true" />;
}

export function SessionRow({
  title,
  state = "idle",
  active = false,
  unseen = false,
  meta,
  pane,
  when,
  origin,
  brief,
  briefTone,
  project,
  path,
  onClick,
  onClose,
  ...rest
}) {
  const needs = NEEDS_TONE[state];
  const isUnseenResult = unseen && (state === "idle" || state === "running" || state === "unseen");
  const dotState = isUnseenResult ? "unseen" : state;
  const classes = [
    "zl-row",
    active ? "is-current" : "",
    needs ? `needs-${needs}` : "",
  ]
    .filter(Boolean)
    .join(" ");

  const handleClose = (event) => {
    event.stopPropagation();
    onClose?.(event);
  };

  const hitLabel = `${title}${project ? `, in ${project}` : ""}${origin ? `, started by ${origin}` : ""}${pane ? `, pane ${pane}` : ""}${isUnseenResult ? ", new result" : STATE_LABEL_SUFFIX[state] ?? ""}`;

  return (
    <span class={`zl-row-slot${onClose ? " has-close" : ""}`} {...rest}>
      {/* The catalogue's row IS the button (zones-lab.jsx:100-106): one hit
          target for the whole row, which is what gives a thumb its 52px. The
          close button cannot nest inside it, so the button keeps the row's
          class and the slot around it only positions the ✕. */}
      <button
        type="button"
        class={classes}
        onClick={onClick}
        aria-current={active ? "true" : undefined}
        aria-label={hitLabel}
      >
        <span class="zl-row-main">
          <span class="zl-row-l1">
            <span class="zl-row-title" aria-hidden="true">{title}</span>
            {/* An event origin is shown as the same Import mark the event
                block uses in the transcript: the source name never fit in a
                7em badge, and "event:a…" told the owner nothing. */}
            {origin && (isEventOrigin(origin)
              ? <span class="zl-row-origin-event" aria-hidden="true"><Import size={12} /></span>
              : <span class="zl-row-origin" aria-hidden="true">{origin}</span>)}
            {pane && <span class="zl-row-pane" aria-hidden="true">{pane}</span>}
            {/* State and age travel together at the end of the title line:
                both are metadata about the row, and pinning the age to a
                fixed width lands every dot on the same x. Ragged dots read
                as a wobble down the list. */}
            <span class="zl-row-meta">
              <Dot state={dotState} />
              {when && <span class="zl-row-when zl-data" aria-hidden="true">{when}</span>}
            </span>
          </span>
          {meta && <span class="zl-row-meta-line zl-data" aria-hidden="true">{meta}</span>}
          {/* Active sessions say what they are doing; saved ones say where they
              live. Two lines is the budget, so the more useful datum wins. */}
          {brief && (
            <span class="zl-row-l2">
              <span class={`zl-row-brief${briefTone ? ` tone-${briefTone}` : ""}`} aria-hidden="true">{brief}</span>
              {project && <span class="zl-row-proj zl-data" aria-hidden="true">{project}</span>}
            </span>
          )}
          {path && <span class="zl-row-path zl-data" aria-hidden="true">{path}</span>}
        </span>
      </button>
      {onClose && (
        <button
          type="button"
          class="zl-row-x"
          aria-label={`Close ${title}`}
          onClick={handleClose}
        >
          <X size={11} />
        </button>
      )}
    </span>
  );
}
