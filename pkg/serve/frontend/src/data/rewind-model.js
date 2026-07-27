// rewind-model.js — pure derivation for the Rewind timeline. Normalizes the
// backend's snake_case BranchPoint shape into the fields the RewindTimeline
// organism renders, and computes the header counter and "you are here"
// marker. Kept side-effect free so it can be unit tested without a DOM.

// normalizeBranchPoint maps one backend BranchPoint (entry_id, label, role,
// timestamp, branch_count, is_current_path) to camelCase, with `timestamp`
// coerced to milliseconds — the backend sends Unix seconds, but this stays
// tolerant of a millisecond value too (anything already above ~year-2001 in
// ms magnitude is assumed to already be ms).
export function normalizeBranchPoint(raw) {
  const ts = typeof raw.timestamp === 'number' ? raw.timestamp : 0;
  const timestampMs = ts > 1e12 ? ts : ts * 1000;
  return {
    entryId: raw.entry_id,
    label: raw.label || '',
    role: raw.role === 'assistant' ? 'assistant' : 'user',
    timestampMs,
    branchCount: raw.branch_count || 0,
    isCurrentPath: !!raw.is_current_path,
  };
}

// normalizeBranchPoints maps the whole list, tolerating a missing/empty
// response (returns []).
export function normalizeBranchPoints(list) {
  if (!Array.isArray(list)) return [];
  return list.map(normalizeBranchPoint);
}

// rewindSummary computes the sheet header counter: total points, and how many
// branches exist across them. A point's `branchCount` is its number of child
// paths; summing it across all points gives the total number of branch paths
// reachable from this timeline, which is more informative than just counting
// points that have at least one branch.
export function rewindSummary(points) {
  const pointCount = points.length;
  const branchCount = points.reduce((sum, p) => sum + (p.branchCount || 0), 0);
  return { pointCount, branchCount };
}

// currentPathTipId returns the entryId of the point that is both on the
// current path AND has no other current-path point after it — i.e. the tip
// ("you are here"). Points are assumed to come from the backend in tree order
// (oldest to newest); the tip is the LAST point with isCurrentPath true.
export function currentPathTipId(points) {
  let tip = null;
  for (const p of points) {
    if (p.isCurrentPath) tip = p.entryId;
  }
  return tip;
}

// isJumpable tells whether a point can be rewound to: it must not be the
// current tip (rewinding there would be a no-op) — everything else, including
// earlier points on the current path, is a valid jump target since it starts
// a new branch.
export function isJumpable(point, tipId) {
  return point.entryId !== tipId;
}

// formatRelativeTime renders a coarse "Xm ago" / "Xh ago" / "Xd ago" string
// from an epoch (ms). Falls back to "just now" for anything under a minute
// and to a plain date past a week, mirroring the other relative-age helpers
// in this codebase (format.js's relAge) but with the "ago" suffix the rewind
// timeline's copy wants.
export function formatRelativeTime(timestampMs, now = Date.now()) {
  if (!Number.isFinite(timestampMs) || timestampMs <= 0) return '';
  const diff = Math.max(0, now - timestampMs);
  const min = Math.floor(diff / 60000);
  if (min < 1) return 'just now';
  if (min < 60) return `${min}m ago`;
  const h = Math.floor(min / 60);
  if (h < 24) return `${h}h ago`;
  const d = Math.floor(h / 24);
  if (d < 7) return `${d}d ago`;
  return new Date(timestampMs).toLocaleDateString();
}
