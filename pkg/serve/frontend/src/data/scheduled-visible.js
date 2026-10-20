// scheduled-visible.js — what every scheduled task is doing, said in the
// user's terms (project, who set it, when it fires next, how long it has been
// running) instead of session ids. Pure decisions; the rows live in
// components/Tasks/ScheduledVisible.jsx.
import { isRepeat, isScheduled, stateOf, whenShort, inWords, clock, WEEKDAY_NAMES, hhmm } from './schedule-model.js';
import { projectName } from './util/format.js';

// openTemplates — every scheduled task that is not finished.
export function openTemplates(list) {
  return (list || []).filter((t) => isScheduled(t) && t.status !== 'done' && !t.archived_at);
}

// staleKind — a once task whose moment has passed and that will not run by
// itself: never sent (failed), or sent and never finished. It stays until the
// user deletes it. A recurring task is never stale: it runs again.
export function staleKind(t) {
  if (isRepeat(t)) return '';
  const s = stateOf(t);
  if (s === 'failed') return 'failed';
  if (s === 'scheduled' && !t.next) return 'unfinished';
  return '';
}

export function bucketOf(t) {
  if (staleKind(t)) return 'stale';
  if (isRepeat(t)) return 'recurring';
  return 'upcoming';
}

// recurrenceWords — the rule in the viewer's clock: a daily run at 09:00 UTC
// reads "Every day at 11:00" in Madrid. The next run carries the local time.
export function recurrenceWords(t, tz) {
  if (!isRepeat(t)) return 'Once';
  const r = t.when.rule;
  const at = t.next ? clock(t.next, tz) : `${hhmm(r.h, r.mi)} ${t.tz || 'UTC'}`;
  if (r.freq === 'daily') return `Every day at ${at}`;
  if (r.freq === 'weekdays') return `Weekdays at ${at}`;
  if (r.freq === 'weekly') return `Every ${WEEKDAY_NAMES[r.dow]} at ${at}`;
  if (r.freq === 'monthly') return `Every month on day ${r.dom} at ${at}`;
  return at;
}

export function nextWords(t, now, tz) {
  if (stateOf(t) === 'paused') return 'Paused';
  if (!t.next) return '';
  return t.next - now < 3 * 3600000 ? inWords(t.next, now) : whenShort(t.next, now, tz);
}

// projectOf — the project's name: its folder, never its key.
export function projectOf(t) {
  return projectName(t.project_cwd || '') || 'No project';
}

// creatorWords — who set it, as the owner thinks of it: an owner by name,
// "an agent" for any other session, "you" when no session did.
export function creatorWords(t, owners = []) {
  const sid = t.created_by_session_id;
  if (!sid) return 'Set by you';
  const own = owners.find((o) => o.session_id === sid);
  return own ? `Set by ${own.name}` : 'Set by an agent';
}

const day = (ms, tz) => new Intl.DateTimeFormat('en-GB', { day: 'numeric', month: 'short', timeZone: tz }).format(ms);

// lastRunWords — when it last ran and how it ended. Cost is unknown until the
// server reports it; it is never shown as zero.
export function lastRunWords(t, now, tz) {
  const s = t.run_stats;
  if (!s || !s.last) return 'Never ran';
  const when = whenShort(s.last.due_at, now, tz);
  if (s.last.state === 'failed') return `Not sent ${when}`;
  if (s.last.state === 'skipped') return `Skipped ${when}`;
  if (s.last.state === 'assigned') return `Sent ${when}, not finished`;
  return `Last ran ${when}`;
}

export function runsWords(t, tz) {
  const s = t.run_stats;
  if (!s || !s.ran) return '';
  return isRepeat(t) ? `${s.ran} runs since ${day(s.first_at, tz)}` : '';
}

export function costWords(t) {
  const c = t.run_stats?.cost_usd;
  return Number.isFinite(c) ? `$${c.toFixed(2)}` : 'cost unknown';
}

export function staleWords(t, now, tz) {
  const at = t.when?.at ? whenShort(t.when.at, now, tz) : '';
  return staleKind(t) === 'failed' ? `Didn't run · was ${at}` : `Sent ${at} · never finished`;
}

// byProject — rows grouped under their project: what waits for you first,
// then recurring, then by next run.
export function byProject(rows) {
  const m = new Map();
  for (const t of rows) {
    const k = projectOf(t);
    if (!m.has(k)) m.set(k, []);
    m.get(k).push(t);
  }
  const order = (t) => (stateOf(t) === 'late' ? 0 : isRepeat(t) ? 1 : 2);
  return [...m.entries()]
    .map(([name, list]) => ({ name, rows: list.sort((a, b) => order(a) - order(b) || (a.next || Infinity) - (b.next || Infinity) || a.id - b.id) }))
    .sort((a, b) => b.rows.filter(isRepeat).length - a.rows.filter(isRepeat).length || a.name.localeCompare(b.name));
}

// watchList — For you's "Running on its own": recurring tasks an agent set
// that have run at least three times and that nobody has reviewed (Keep).
export function watchList(list) {
  return openTemplates(list).filter((t) => isRepeat(t) && t.created_by_session_id && stateOf(t) !== 'paused' && (t.run_stats?.ran || 0) >= 3 && !t.kept_at);
}
