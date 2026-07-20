import { useState, useRef, useEffect } from "preact/hooks";

// MIN_DWELL_MS — a terminated line that has just entered the tail's visible
// window stays for at least this long before a newer one can push it out, so a
// burst of fast tools can't flash a line away before it can be read
// (TOOLCALLS-ALT-SPEC-FABLE.md, direction B: minimum 400ms dwell before it
// folds away).
export const MIN_DWELL_MS = 400;

// useTailWindow holds the tail's visible terminated rows steady under rapid
// churn. `target` is the ideal visible window (the last N terminated rows);
// this returns the rows to actually render. New rows appear immediately, but a
// row is only dropped once it has been shown for MIN_DWELL_MS — until then it
// lingers (the window may briefly hold one extra line, then settles).
//
// Rows are matched by `id`. The hook re-renders itself via a timer when a
// lingering row's dwell expires, so callers don't need to poll.
export function useTailWindow(target) {
  const [, force] = useState(0);
  // id -> timestamp the row first became visible in the window.
  const seenRef = useRef(new Map());
  const timerRef = useRef(null);
  const shownRef = useRef([]);

  const now = Date.now();
  const targetIds = new Set(target.map((r) => r.id));
  const seen = seenRef.current;

  // Stamp first-seen for rows currently in target.
  for (const row of target) {
    if (!seen.has(row.id)) seen.set(row.id, now);
  }

  // Rows to keep = target ∪ (previously shown rows still within their dwell).
  const lingering = shownRef.current.filter(
    (row) => !targetIds.has(row.id) && now - (seen.get(row.id) ?? 0) < MIN_DWELL_MS,
  );
  const shown = [...lingering, ...target];
  shownRef.current = shown;

  // Drop stamps for rows no longer shown (they've fully expired/left).
  for (const id of [...seen.keys()]) {
    if (!shown.some((r) => r.id === id)) seen.delete(id);
  }

  // Schedule a re-render when the soonest lingering row's dwell expires so it
  // gets dropped even if no new event arrives.
  useEffect(() => {
    if (timerRef.current) {
      clearTimeout(timerRef.current);
      timerRef.current = null;
    }
    if (lingering.length === 0) return;
    const soonest = Math.min(
      ...lingering.map((row) => MIN_DWELL_MS - (Date.now() - (seenRef.current.get(row.id) ?? 0))),
    );
    timerRef.current = setTimeout(() => {
      timerRef.current = null;
      force((n) => n + 1);
    }, Math.max(0, soonest));
    return () => {
      if (timerRef.current) {
        clearTimeout(timerRef.current);
        timerRef.current = null;
      }
    };
  });

  return shown;
}
