// dossier.js — the two rules of the shell's third zone, kept pure so both can
// be exercised without a renderer: they are decisions about a SURFACE, not
// about styling.

// ── Who owns it ────────────────────────────────────────────────────────────
//
//   - Ambient off: there is no dossier at all. The switch gates a new surface,
//     not a restyle, so with it off the desktop is the same two-zone shell it
//     has always been — same DOM, same CSS.
//   - The pane grid: NO dossier. The grid shows several sessions at once and
//     "this session" has no single answer there: a third zone fed by the
//     focused tile would silently swap what it shows as focus moves, and a
//     dossier that changes under you is worse than none. The grid has never
//     had one; this keeps it that way rather than inventing an owner.
//   - No focused session (still loading, or nothing open): nothing to hold a
//     dossier for.
export function desktopDossierView({ ambient, view, session, panel }) {
  if (!ambient) return null;
  if (view === 'grid') return null;
  if (!session) return null;
  return { open: !!panel?.open, page: panel?.page || 'root' };
}

// ── Where it fits ──────────────────────────────────────────────────────────
//
// The dossier is a real column only where one fits; narrower than that it stays
// the drawer it has always been, over the centre.
//
// The number is the sum of what the three zones need, not a round breakpoint:
//
//   spine        264px  --spine-width
//   centre       844px  the conversation's OWN declared measure: .composer-wrap
//                       and .status-strip are max-width:844px and .stream-col
//                       780px. At 844 the centre is at full size, so docking
//                       the dossier costs the conversation nothing. Below it
//                       the composer starts shrinking and the status line
//                       begins shedding (the strip drops items from 560px) —
//                       and what it sheds is exactly the readings the dossier
//                       exists to hold, so paying for the third column out of
//                       the centre would be paying twice.
//   dossier      340px  --dossier-width, the panel's width today.
//                ─────
//                1448px
//
// Verified in the browser at 1600 / 1448 / 1447 / 1400 / 1100 / 900 on a real
// session: at 1448 the centre measures 844 with the dossier docked and nothing
// sheds; one pixel under, the zone flips back to a drawer.
export const DOSSIER_DOCK_MIN = 1448;

export function dossierDocks(viewportWidth) {
  return Number(viewportWidth) >= DOSSIER_DOCK_MIN;
}
