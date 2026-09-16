import { useRef } from "preact/hooks";
import { prefersReducedMotion } from "../../hooks/motion.js";

// arrivals — which ledger rows are NEW, and therefore allowed to move.
//
// The ledger's rows must not all animate whenever the ledger renders. Two
// cases make that difference:
//
//   1. HYDRATION. Opening a saved conversation mounts a ledger that already
//      holds forty rows. None of them just happened, so none of them may
//      arrive: a transcript that re-enacts its own history on every open is a
//      slot machine. Whatever is present at the FIRST render is furniture.
//
//   2. A BATCH. Expanding the fold header reveals eleven rows in one commit;
//      a reconnect can deliver a burst the same way. Eleven staggered entries
//      is the same slot machine on a smaller scale, and none of those rows
//      "arrived" either -- they were already done, they were merely hidden.
//      So a commit that adds more than BURST_MAX rows animates nothing.
//
// What is left is the case the movement is FOR: one call, or a couple, landing
// in a transcript the reader is watching.

// BURST_MAX — above this, an added run is a reveal, not an arrival.
export const BURST_MAX = 4;

// STAGGER_MAX — the highest stagger slot handed out, so the last row of an
// allowed burst never waits longer than STAGGER_MAX * --motion-stagger (135ms)
// before it starts. Rows follow each other; they do not queue.
export const STAGGER_MAX = 3;

// EXPIRE_MS — how long an id stays marked. Long enough for the animation to
// finish, short enough that a row remounted much later (a fold toggled, a
// re-projection) is furniture again rather than replaying its entrance.
export const EXPIRE_MS = 600;

// computeArrivals — the pure step (exported for tests). `known` is the set of
// ids this ledger has already rendered; it is MUTATED, which is what makes
// "first render" mean something. Returns a Map of id -> stagger slot for the
// rows allowed to move.
export function computeArrivals(ids, known, firstRender) {
  const fresh = [];
  for (const id of ids) {
    if (id == null) continue;
    if (!known.has(id)) fresh.push(id);
    known.add(id);
  }
  // Ids that vanished stop being remembered, so the map stays bounded and a
  // row genuinely re-added later can arrive again.
  const present = new Set(ids);
  for (const id of [...known]) if (!present.has(id)) known.delete(id);

  if (firstRender || fresh.length === 0 || fresh.length > BURST_MAX) return new Map();
  return new Map(fresh.map((id, i) => [id, Math.min(i, STAGGER_MAX)]));
}

// useArrivals returns `slotOf(id)` — the stagger slot for a row that has just
// arrived, or null for one that has always been there. Under reduced motion
// nothing ever arrives: the rows are simply present, with all their text.
export function useArrivals(rows) {
  const known = useRef(new Set());
  const first = useRef(true);
  const marks = useRef(new Map());
  const stamped = useRef(0);

  const reduced = prefersReducedMotion();
  const ids = rows.map((r) => r.id);
  const fresh = computeArrivals(ids, known.current, first.current);
  first.current = false;

  if (fresh.size > 0 && !reduced) {
    marks.current = fresh;
    stamped.current = Date.now();
  } else if (marks.current.size > 0 && Date.now() - stamped.current > EXPIRE_MS) {
    // Expired lazily, on the next render that happens anyway: a timer here
    // would re-render the whole ledger only to remove a class nobody can see.
    marks.current = new Map();
  }

  const current = marks.current;
  return (id) => (id != null && current.has(id) ? current.get(id) : null);
}
