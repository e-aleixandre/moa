import { Dot } from "../SessionRow/SessionRow.jsx";
import { projectName } from "../../data/util/format.js";
import { ownerDotState, ownerLine, ownerState } from "../../data/owners-model.js";
import { OwnerAvatarFor } from "./OwnerAvatar.jsx";
import "./OwnerAvatar.css";
import "./OwnerRow.css";

/* OwnerRow and the sidebar's collapsible section heading.

   WHAT THE OWNER DECIDED (iteration 3). Owners is not the third mode of the
   segmented: the segmented says ORDER (Recent · By project), not "which list".
   So:

     · In Recent, OWNERS is a SECTION above everything else:
       OWNERS · NEEDS ATTENTION · ACTIVE · SAVED.
     · In By project, the owner is the FIRST ROW of its group. There is no
       Owners section there, because a row printed in a section and again
       inside its folder is the same row twice.
     · Owners, Active and Saved collapse; Needs attention does not. Collapsing
       is for a list you keep; Needs attention is a PROMOTION — it is empty
       when nothing is wrong, and hiding it would hide the one thing that
       cannot wait.
     · An owner NEVER rises into Needs attention (data/util/project-sessions.js
       attentionKind). Its state is painted on its own row instead, because an
       owner is not a piece of work that finishes: it is standing, and a row
       that leaves its section to appear in another one is a row you then have
       to find again. */

/* ── Chevron: the project group's, so the two mechanics cannot drift ───── */
function ChevronIcon() {
  return (
    <svg class="zl-proj-chev" viewBox="0 0 16 16" aria-hidden="true">
      <path d="M6 3.5L10.5 8 6 12.5" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" />
    </svg>
  );
}

/* ── OwnerRow ────────────────────────────────────────────────────────────
   A distinct row, on purpose. A session row is a piece of work with an age and
   a project; an owner is a standing thing with a face and a headcount. Same
   height rhythm and the same two-line grammar, so the column keeps its beat,
   but the avatar and the missing age say "this is not one of the sessions
   below" before you have read a word. */
export function OwnerRow({ owner, project = false, active = false, onOpen }) {
  const { lead, tail, stated } = ownerLine(owner);
  const state = ownerState(owner);
  const place = projectName(owner.root);
  // The address is dropped when it is the name again. Compared on letters
  // only: an owner called "Winerim Web" lives in `winerim-web`, and printing
  // that under it spends a line on the same word with a hyphen in it.
  const same = (a, b) => a.toLowerCase().replace(/[^a-z0-9]/g, "") === b.toLowerCase().replace(/[^a-z0-9]/g, "");
  const showPath = !project && !same(place, owner.name);
  // Where "N waiting on you" goes. Beside an idle owner's count ("4 working") it is a
  // short second half of a short line and the two belong together. Beside a
  // quoted question it is not: measured in the 300px drawer, "Needs your
  // answer · 2 waiting on you" truncated at "2 w…", losing the number that
  // stops work. So a stated owner drops it to the third line, where it sits
  // with the address rather than competing with the question.
  const tailBelow = !!tail && stated;
  const label = `${owner.name}, project owner. ${lead.text}${tail ? `. ${tail}` : ""}`;
  return (
    <span class="zl-row-slot">
      <button
        type="button"
        class={`zl-row ow-orow${active ? " is-current" : ""}`}
        onClick={() => onOpen?.(owner)}
        aria-current={active ? "true" : undefined}
        aria-label={label}
      >
        <OwnerAvatarFor owner={owner} state={state} size={32} />
        <span class="zl-row-main ow-orow-main">
          <span class="zl-row-l1">
            <span class="ow-orow-name" aria-hidden="true">{owner.name}</span>
            {/* The word "owner" earns its place only where the row sits among
                sessions — inside a project group. Under the OWNERS heading it
                would be the heading repeated on every row, and measured in the
                272px column it cost exactly the width that truncated
                "2 waiting on you", which is the clause this row exists for. */}
            {project && <span class="ow-orow-kind zl-data" aria-hidden="true">owner</span>}
            <span class="zl-row-meta">
              <Dot state={ownerDotState(owner)} />
            </span>
          </span>
          <span class="zl-row-l2">
            <span class={`zl-row-brief ow-orow-brief tone-${lead.tone}`} aria-hidden="true">
              {lead.text}
              {tail && !tailBelow && <span class="ow-orow-sep" aria-hidden="true"> · </span>}
              {tail && !tailBelow && <span class="ow-orow-wait" aria-hidden="true">{tail}</span>}
            </span>
          </span>
          {/* The third line: what is waiting, and where this owner lives. The
              address is here and not on the state line — sharing cost the
              state clause 45% of the width, and the state is why the row is
              read at all — and it is dropped entirely when it would repeat the
              name, since an owner is almost always called after its project. */}
          {(tailBelow || showPath) && (
            <span class="zl-row-l3 ow-orow-l3" aria-hidden="true">
              {tailBelow && <span class="ow-orow-wait">{tail}</span>}
              {showPath && <span class="zl-row-path ow-orow-path zl-data">{place}</span>}
            </span>
          )}
        </span>
      </button>
    </span>
  );
}

/* ── Section heading ─────────────────────────────────────────────────────
   The project group's mechanics, verbatim: a <button> with aria-expanded, the
   chevron that rotates 90° when open, and the count that stays visible either
   way — a collapsed section whose count disappeared would be a section you
   have to open to find out whether it was worth opening.

   `attn` is the one that is NOT a button (Needs attention). It keeps the same
   typography and loses only the chevron, so the column does not look like two
   kinds of list stacked. */
export function SectionHead({ label, n, attn = false, dot = null, open = true, onToggle }) {
  if (attn) {
    return (
      <div class="zl-group is-attn">
        <span>{label}</span>
        <span class="zl-group-n zl-data">{n}</span>
      </div>
    );
  }
  return (
    <button
      type="button"
      class={`zl-group ow-sec${open ? " is-open" : ""}`}
      aria-expanded={open}
      aria-label={`${label}, ${n}`}
      onClick={onToggle}
    >
      <ChevronIcon />
      <span>{label}</span>
      {/* The collapsed section's one mark: the most urgent thing inside it.
          Drawn only when collapsed — open, the rows say it themselves. */}
      {!open && dot && <Dot state={dot} />}
      <span class="zl-group-n zl-data">{n}</span>
    </button>
  );
}
