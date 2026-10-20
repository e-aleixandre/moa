// scheduled-visible.js — what every scheduled task is doing, said in the
// user's terms (project, who set it, when it fires next, how long it has been
// running) instead of session ids. Pure decisions; the rows live in
// components/Tasks/ScheduledVisible.jsx.
import { isRepeat, isScheduled, stateOf, whenShort, inWords, clock, wall, WEEKDAY_NAMES, hhmm } from './schedule-model.js';
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
// reads "Every day at 11:00" in Madrid. Day and time come together from the
// next run, which the server computed in the template's zone, so a Monday
// 00:30 UTC run reads "Every Sunday at 20:30" in New York. A monthly day that
// lands on another day locally would change with the month's length: that
// rule is said in the template's own zone.
export function recurrenceWords(t, tz) {
  if (!isRepeat(t)) return 'Once';
  const r = t.when.rule;
  const home = t.tz || 'UTC';
  if (!t.next) return ruleWords(r, r.dow, `${hhmm(r.h, r.mi)} ${home}`);
  const here = wall(t.next, tz);
  const there = wall(t.next, home);
  if (r.freq === 'monthly' && here.d !== there.d) return ruleWords(r, r.dow, `${hhmm(there.h, there.mi)} ${home}`);
  const shift = (here.dow - there.dow + 7) % 7;
  return ruleWords({ ...r, shift }, ((r.dow ?? 0) + shift) % 7, clock(t.next, tz));
}

function ruleWords(r, dow, at) {
  if (r.freq === 'daily') return `Every day at ${at}`;
  if (r.freq === 'weekdays') {
    if (!r.shift) return `Weekdays at ${at}`;
    return `${WEEKDAY_NAMES[(1 + r.shift) % 7]} to ${WEEKDAY_NAMES[(5 + r.shift) % 7]} at ${at}`;
  }
  if (r.freq === 'weekly') return `Every ${WEEKDAY_NAMES[dow]} at ${at}`;
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

// byProject — rows grouped under their project (its key: two folders with
// the same name are two projects): what waits for you first, then recurring,
// then by next run. A name shared by several projects carries its parent
// folder to tell them apart.
export function byProject(rows) {
  const m = new Map();
  for (const t of rows) {
    const k = t.project_key || t.project_cwd || '';
    if (!m.has(k)) m.set(k, []);
    m.get(k).push(t);
  }
  const order = (t) => (stateOf(t) === 'late' ? 0 : isRepeat(t) ? 1 : 2);
  const groups = [...m.entries()].map(([key, list]) => ({ key, name: projectOf(list[0]), rows: list.sort((a, b) => order(a) - order(b) || (a.next || Infinity) - (b.next || Infinity) || a.id - b.id) }));
  const seen = {};
  for (const g of groups) seen[g.name] = (seen[g.name] || 0) + 1;
  for (const g of groups) if (seen[g.name] > 1) g.name = parentName(g.rows[0].project_cwd) || g.name;
  return groups.sort((a, b) => b.rows.filter(isRepeat).length - a.rows.filter(isRepeat).length || a.name.localeCompare(b.name));
}

function parentName(cwd = '') {
  const parts = cwd.split('/').filter(Boolean);
  return parts.length > 1 ? parts.slice(-2).join('/') : '';
}

// watchList — For you's "Running on its own": recurring tasks an agent set
// that have run at least three times and that nobody has reviewed (Keep).
export function watchList(list) {
  return openTemplates(list).filter((t) => isRepeat(t) && t.created_by_session_id && stateOf(t) !== 'paused' && (t.run_stats?.ran || 0) >= 3 && !t.kept_at);
}
