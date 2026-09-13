// artifact-groups.js — the list's rhythm. Eighty screenshots in one flat pile
// have no landmarks; the day they were delivered is the one the eye already
// uses ("the ones from yesterday"), so the list is cut there and nowhere else.
// Server order (updated_at desc) is preserved inside and across groups.
// DOM-free so the cut is unit-testable without a renderer.

const DAY = 86400000;

function startOfDay(ms) {
  const d = new Date(ms);
  d.setHours(0, 0, 0, 0);
  return d.getTime();
}

// artifactDayLabel — "Today", "Yesterday", then the date in the short form
// the session dossier already uses for "started", with the year only once it
// stops being this one. `now` is Date.now() by default so a frozen lab clock
// (fidelity-freeze) yields a stable heading.
export function artifactDayLabel(iso, now = Date.now()) {
  if (!iso) return 'Earlier';
  const ms = new Date(iso).getTime();
  if (!Number.isFinite(ms)) return 'Earlier';
  const day = startOfDay(ms);
  const today = startOfDay(now);
  if (day === today) return 'Today';
  if (day === today - DAY) return 'Yesterday';
  const date = new Date(ms);
  const sameYear = date.getFullYear() === new Date(now).getFullYear();
  return date.toLocaleDateString([], sameYear
    ? { day: 'numeric', month: 'short' }
    : { day: 'numeric', month: 'short', year: 'numeric' });
}

// groupArtifactsByDay — consecutive entries sharing a label form one group.
// Consecutive, not bucketed: the input is already sorted by the server, and
// re-sorting here would let the list disagree with the transcript.
export function groupArtifactsByDay(items, now = Date.now()) {
  const groups = [];
  for (const item of items) {
    const label = artifactDayLabel(item.updatedAt || item.createdAt, now);
    const last = groups[groups.length - 1];
    if (last && last.label === label) last.items.push(item);
    else groups.push({ label, items: [item] });
  }
  return groups;
}
