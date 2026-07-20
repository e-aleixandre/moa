import { useState, useRef, useEffect } from "preact/hooks";

// MIN_DWELL_MS — a terminated line that has just entered the tail's visible
// window stays for at least this long before it can start folding away, so a
// burst of fast tools can't flash a line away before it can be read
// (TOOLCALLS-ALT-SPEC-FABLE.md, direction B: minimum 400ms dwell).
export const MIN_DWELL_MS = 400;

// FOLD_MS — once a row is due to leave the window it doesn't vanish in one
// frame; it spends this long in a `folding` state (height + opacity animating
// to 0) so the collapse reads as a motion toward the "N earlier actions"
// counter instead of a jump (TOOL-LIVE-FIXES-SPEC-FABLE.md P2, option 2A).
// Keep it in sync with the .tg-row.folding transition in ActivityLedger.css.
export const FOLD_MS = 180;

// computeTailWindow — the pure state step behind useTailWindow (exported for
// tests). Given the ideal `target` window, the previously-shown rows, the two
// timing maps (mutated in place) and the current time, it returns the rows to
// render (each possibly `_folding`) plus the delay until the next phase change
// (or null if nothing is pending). A row that leaves `target` goes through
// steady → folding (FOLD_MS) → dropped; re-entering `target` cancels its fold.
export function computeTailWindow(target, prevShown, seen, left, now) {
  const targetIds = new Set(target.map((r) => r.id));

  // Stamp first-seen for target rows; cancel any pending fold if a row is back.
  for (const row of target) {
    if (!seen.has(row.id)) seen.set(row.id, now);
    left.delete(row.id);
  }

  // foldStart = when a lingering row begins folding: once BOTH its minimum
  // dwell has elapsed AND it has actually left target. So even long-lived rows
  // (shown well past the dwell) animate out instead of disappearing in a frame.
  const foldStartOf = (id) => {
    const firstSeen = seen.get(id) ?? now;
    const leftAt = left.get(id) ?? now;
    return Math.max(leftAt, firstSeen + MIN_DWELL_MS);
  };

  const lingering = [];
  for (const row of prevShown) {
    if (targetIds.has(row.id)) continue; // still in target, added below
    if (!left.has(row.id)) left.set(row.id, now);
    const foldStart = foldStartOf(row.id);
    if (now >= foldStart + FOLD_MS) continue; // fully expired → drop
    const folding = now >= foldStart;
    lingering.push(folding ? { ...row, _folding: true } : row);
  }

  const shown = [...lingering, ...target];

  // Drop stamps for rows no longer shown (bounds the maps).
  const shownIds = new Set(shown.map((r) => r.id));
  for (const id of [...seen.keys()]) if (!shownIds.has(id)) seen.delete(id);
  for (const id of [...left.keys()]) if (!shownIds.has(id)) left.delete(id);

  // Next phase change: a lingering row entering its fold, or a folding row
  // being dropped. null when nothing is pending (no timer needed).
  const deadlines = [];
  for (const row of shown) {
    if (targetIds.has(row.id)) continue;
    const foldStart = foldStartOf(row.id);
    if (now < foldStart) deadlines.push(foldStart - now);
    deadlines.push(foldStart + FOLD_MS - now);
  }
  const nextDeadline = deadlines.length > 0 ? Math.max(0, Math.min(...deadlines)) : null;

  return { shown, nextDeadline };
}

// useTailWindow holds the tail's visible terminated rows steady under rapid
// churn, then folds them out gracefully. `target` is the ideal visible window
// (the last N terminated rows); this returns the rows to actually render, each
// possibly carrying `_folding: true` for its final FOLD_MS before removal. The
// hook re-renders itself via a timer at each phase change, so callers don't
// need to poll.
export function useTailWindow(target) {
  const [, force] = useState(0);
  const seenRef = useRef(new Map()); // id -> first-seen ts (dwell floor)
  const leftRef = useRef(new Map()); // id -> left-target ts (fold timing)
  const timerRef = useRef(null);
  const shownRef = useRef([]);

  const { shown, nextDeadline } = computeTailWindow(
    target,
    shownRef.current,
    seenRef.current,
    leftRef.current,
    Date.now(),
  );
  shownRef.current = shown;

  useEffect(() => {
    if (timerRef.current) {
      clearTimeout(timerRef.current);
      timerRef.current = null;
    }
    if (nextDeadline == null) return;
    timerRef.current = setTimeout(() => {
      timerRef.current = null;
      force((n) => n + 1);
    }, nextDeadline);
    return () => {
      if (timerRef.current) {
        clearTimeout(timerRef.current);
        timerRef.current = null;
      }
    };
  });

  return shown;
}
