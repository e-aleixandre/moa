// schedule-model.js — scheduled and recurring tasks on the client, as pure
// decisions (no store, no fetch). A scheduled task is a template: an ordinary
// task record with `when`, `tz`, `target` and `delivery` (docs/serve.md,
// Scheduled tasks). The server owns the calendar: it reads "When" and decides
// every run. What lives here is how a schedule is WORDED, in the viewer's
// zone, and the bodies the editor sends.

import { projectName, sessionTitle } from './util/format.js';

export const DEFAULT_DELIVERY = Object.freeze({ busy: 'steer', saved: 'wake', late: 'ask' });

export const isScheduled = (t) => !!t && !!t.when;
export const isRepeat = (t) => t?.when?.kind === 'repeat';

// ── Clock ─────────────────────────────────────────────────────────────────

export const WEEKDAY_NAMES = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'];
const WD_SHORT = ['sun', 'mon', 'tue', 'wed', 'thu', 'fri', 'sat'];
const MON = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

export function deviceZone() {
  try { return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'; } catch { return 'UTC'; }
}

const fmtCache = new Map();
function fmt(tz) {
  if (!fmtCache.has(tz)) {
    const opts = { hourCycle: 'h23', year: 'numeric', month: 'numeric', day: 'numeric', hour: 'numeric', minute: 'numeric', weekday: 'short' };
    let f;
    try { f = new Intl.DateTimeFormat('en-US', { ...opts, timeZone: tz }); } catch { f = new Intl.DateTimeFormat('en-US', { ...opts, timeZone: 'UTC' }); }
    fmtCache.set(tz, f);
  }
  return fmtCache.get(tz);
}

// wall — the wall clock of `ms` in `tz`.
export function wall(ms, tz) {
  const p = Object.fromEntries(fmt(tz).formatToParts(new Date(ms)).map((x) => [x.type, x.value]));
  return { y: +p.year, mo: +p.month, d: +p.day, h: +p.hour % 24, mi: +p.minute, dow: WD_SHORT.indexOf(p.weekday.toLowerCase()) };
}

// zoned — the instant a wall time names in `tz` (a gap moves forward). It
// only previews the date and time fields; the server resolves what is saved.
export function zoned(y, mo, d, h, mi, tz) {
  const want = Date.UTC(y, mo - 1, d, h, mi);
  let guess = want;
  for (let i = 0; i < 3; i++) {
    const w = wall(guess, tz);
    guess += want - Date.UTC(w.y, w.mo - 1, w.d, w.h, w.mi);
  }
  return guess;
}

function addDays(w, n) {
  const t = new Date(Date.UTC(w.y, w.mo - 1, w.d + n));
  return { y: t.getUTCFullYear(), mo: t.getUTCMonth() + 1, d: t.getUTCDate(), dow: t.getUTCDay() };
}

const pad = (n) => String(n).padStart(2, '0');
export const hhmm = (h, mi) => `${pad(h)}:${pad(mi)}`;

function dayDiff(ms, now, tz) {
  const a = wall(ms, tz);
  const b = wall(now, tz);
  return Math.round((Date.UTC(a.y, a.mo - 1, a.d) - Date.UTC(b.y, b.mo - 1, b.d)) / 86400000);
}

export function dateLabel(ms, tz) {
  const w = wall(ms, tz);
  return `${WEEKDAY_NAMES[w.dow].slice(0, 3)} ${w.d} ${MON[w.mo - 1]}`;
}

export function clock(ms, tz) {
  const w = wall(ms, tz);
  return hhmm(w.h, w.mi);
}

// whenShort — "Today 21:00", "Tomorrow 03:00", "Mon 09:00", "Mon 13 Oct 09:00".
export function whenShort(ms, now, tz) {
  const diff = dayDiff(ms, now, tz);
  const t = clock(ms, tz);
  if (diff === 0) return `Today ${t}`;
  if (diff === 1) return `Tomorrow ${t}`;
  if (diff === -1) return `Yesterday ${t}`;
  if (diff > 1 && diff < 7) return `${WEEKDAY_NAMES[wall(ms, tz).dow].slice(0, 3)} ${t}`;
  return `${dateLabel(ms, tz)} ${t}`;
}

// whenLong — "Wed 1 Oct, 03:00".
export function whenLong(ms, tz) {
  return `${dateLabel(ms, tz)}, ${clock(ms, tz)}`;
}

export function inWords(ms, now) {
  const m = Math.round((ms - now) / 60000);
  const a = Math.abs(m);
  if (a < 1) return 'now';
  let s;
  if (a < 60) s = `${a} min`;
  else if (a < 60 * 36) s = `${Math.round(a / 60)} h`;
  else s = `${Math.round(a / 1440)} days`;
  return m < 0 ? `${s} ago` : `in ${s}`;
}

function ordinal(n) {
  const s = n % 100 >= 11 && n % 100 <= 13 ? 'th' : ({ 1: 'st', 2: 'nd', 3: 'rd' }[n % 10] || 'th');
  return `${n}${s}`;
}

export function ruleText(rule) {
  const t = hhmm(rule.h, rule.mi);
  if (rule.freq === 'daily') return `Every day, ${t}`;
  if (rule.freq === 'weekdays') return `Weekdays, ${t}`;
  if (rule.freq === 'weekly') return `Every ${WEEKDAY_NAMES[rule.dow]}, ${t}`;
  if (rule.freq === 'monthly') return `Monthly on the ${ordinal(rule.dom)}, ${t}`;
  return t;
}

export function ruleShort(rule) {
  const t = hhmm(rule.h, rule.mi);
  if (rule.freq === 'daily') return `Daily ${t}`;
  if (rule.freq === 'weekdays') return `Weekdays ${t}`;
  if (rule.freq === 'weekly') return `${WEEKDAY_NAMES[rule.dow].slice(0, 3)} ${t}`;
  if (rule.freq === 'monthly') return `Monthly ${t}`;
  return t;
}

// nextOf — the next slot of a rule after `after`, for a draft the server has
// not answered for yet. A saved schedule's next run is always the server's.
export function nextOf(rule, after, tz) {
  const base = wall(after, tz);
  for (let i = 0; i < 400; i++) {
    const day = addDays(base, i);
    const ok = rule.freq === 'daily'
      || (rule.freq === 'weekdays' && day.dow >= 1 && day.dow <= 5)
      || (rule.freq === 'weekly' && day.dow === rule.dow)
      || (rule.freq === 'monthly' && day.d === rule.dom);
    if (!ok) continue;
    const t = zoned(day.y, day.mo, day.d, rule.h, rule.mi, tz);
    if (t > after) return t;
  }
  return null;
}

export function whenNext(when, now, tz) {
  if (!when) return null;
  return when.kind === 'repeat' ? nextOf(when.rule, now, tz) : when.at;
}

export function whenWords(when, tz) {
  if (!when) return '';
  return when.kind === 'repeat' ? ruleText(when.rule) : whenLong(when.at, tz);
}

export function whenButton(when, now, tz) {
  if (!when) return '';
  return when.kind === 'repeat' ? ruleShort(when.rule) : whenShort(when.at, now, tz);
}

// Presets the When editor offers before anything is typed.
export function presets(now, tz) {
  const cur = wall(now, tz);
  const tomorrow = addDays(cur, 1);
  const nextMon = addDays(cur, ((1 - cur.dow + 7) % 7) || 7);
  return [
    { label: 'In 20 minutes', at: Math.round((now + 20 * 60000) / 60000) * 60000 },
    { label: 'Tonight', at: zoned(cur.y, cur.mo, cur.d, 21, 0, tz) },
    { label: 'Tomorrow morning', at: zoned(tomorrow.y, tomorrow.mo, tomorrow.d, 9, 0, tz) },
    { label: 'Monday', at: zoned(nextMon.y, nextMon.mo, nextMon.d, 9, 0, tz) },
  ].filter((p) => p.at > now);
}

export function dateInputValue(ms, tz) {
  const w = wall(ms, tz);
  return `${w.y}-${pad(w.mo)}-${pad(w.d)}`;
}

export function fromInputs(date, time, tz) {
  const [y, mo, d] = String(date).split('-').map(Number);
  const [h, mi] = String(time).split(':').map(Number);
  if (![y, mo, d, h, mi].every(Number.isFinite)) return null;
  return zoned(y, mo, d, h, mi, tz);
}

// repeatOptions — the Repeat field, named after the date it is set on.
export function repeatOptions(at, tz) {
  const w = wall(at, tz);
  return [
    { id: 'never', label: 'Never' },
    { id: 'daily', label: 'Every day', rule: { freq: 'daily' } },
    { id: 'weekdays', label: 'Weekdays', rule: { freq: 'weekdays' } },
    { id: 'weekly', label: `Every ${WEEKDAY_NAMES[w.dow]}`, rule: { freq: 'weekly', dow: w.dow } },
    { id: 'monthly', label: `Monthly on day ${w.d}`, rule: { freq: 'monthly', dom: w.d } },
  ];
}

// pickedWhen — the three fields (date, time, repeat) as a canonical value.
export function pickedWhen(date, time, repeat, tz) {
  const base = fromInputs(date, time, tz);
  if (base == null) return null;
  const opt = repeatOptions(base, tz).find((o) => o.id === repeat);
  if (!opt?.rule) return { kind: 'once', at: base };
  const w = wall(base, tz);
  return { kind: 'repeat', rule: { ...opt.rule, h: w.h, mi: w.mi } };
}

// ── The list ──────────────────────────────────────────────────────────────

const ORDER = { late: 0, failed: 1, scheduled: 2, paused: 3 };
export const stateOf = (t) => t?.schedule_state || 'scheduled';

// scheduledRows — the Scheduled group: open templates, what needs you first,
// then by next run, paused at the end. A finished once-template is Done.
export function scheduledRows(list) {
  return (list || []).filter((t) => isScheduled(t) && t.status !== 'done' && !t.archived_at)
    .sort((a, b) => ((ORDER[stateOf(a)] ?? 2) - (ORDER[stateOf(b)] ?? 2)) || ((a.next || Infinity) - (b.next || Infinity)) || (a.id - b.id));
}

// waitingCount — the runs waiting for your Run or Skip (a paused template's
// included): the group's "N waiting for you".
export function waitingCount(rows) {
  return (rows || []).reduce((n, t) => n + (t.late_count || (stateOf(t) === 'late' ? 1 : 0)), 0);
}

// attentionCount — the number on the Tasks door: the server's attention
// (open requests + late runs), whatever the view is filtered to. An older
// server has no attention: its open requests.
export function attentionCount(slice) {
  const c = slice?.counts || {};
  if (Number.isFinite(c.attention)) return c.attention;
  if (Number.isFinite(c.open_requests)) return c.open_requests;
  return (slice?.list || []).filter((t) => t.place === 'you' && t.requester_session_id && t.status !== 'done').length;
}

export function schedEyebrow(t) {
  const s = stateOf(t);
  if (s === 'late') return 'Waiting for you';
  if (s === 'failed') return 'Not sent';
  if (s === 'paused') return 'Paused';
  return isRepeat(t) ? 'Recurring' : 'Scheduled';
}

const noToday = (s) => s.replace(/^Today /, '');

// schedRight — the row's right edge: when a late run was due, when a failed
// one was meant to go, the next run (in words while it is close).
export function schedRight(t, now, tz, late = null) {
  const s = stateOf(t);
  if (s === 'late') {
    const due = late?.at || (!isRepeat(t) ? t.when.at : 0);
    return due ? `was ${noToday(whenShort(due, now, tz))}` : 'Run now?';
  }
  if (s === 'failed') return isRepeat(t) ? '' : noToday(whenShort(t.when.at, now, tz));
  if (!t.next) return '';
  return t.next - now < 3 * 3600000 ? inWords(t.next, now) : whenShort(t.next, now, tz);
}

// ── Names ─────────────────────────────────────────────────────────────────

export function targetSessionId(target, owners = []) {
  if (!target) return '';
  if (target.kind === 'session') return target.id || '';
  if (target.kind === 'owner') return owners.find((o) => o.id === target.id)?.session_id || '';
  return '';
}

export function targetName(target, sessions = {}, owners = []) {
  if (!target) return '';
  if (target.kind === 'owner') return owners.find((o) => o.id === target.id)?.name || 'Owner';
  if (target.kind === 'new') return `New session · ${projectName(target.cwd || '') || target.project || ''}`;
  const s = sessions[target.id];
  return s ? sessionTitle(s) : 'Deleted session';
}

// targetFromDest — the Send to picker speaks the Move picker's levels. An
// owner row carries the owner's session; a schedule names the owner itself.
export function targetFromDest(dest, { model = '', thinking = '' } = {}) {
  if (dest?.place === 'new') {
    const t = { kind: 'new', project: dest.key, cwd: dest.cwd || '', model };
    if (thinking) t.thinking = thinking;
    return t;
  }
  if (dest?.owner?.id) return { kind: 'owner', id: dest.owner.id };
  return { kind: 'session', id: dest?.sessionId || '' };
}

// isTargetHere — the picker's check mark, for a target.
export function isTargetHere(target, dest, owners = []) {
  if (!target || !dest) return false;
  if (dest.place === 'new') return target.kind === 'new' && target.project === dest.key && (!dest.cwd || target.cwd === dest.cwd);
  if (dest.place !== 'agent') return false;
  if (target.kind === 'owner') return targetSessionId(target, owners) === dest.sessionId;
  return target.kind === 'session' && target.id === dest.sessionId;
}

// ── Delivery ──────────────────────────────────────────────────────────────

export const DELIVERY_ROWS = [
  { key: 'busy', label: "If it's working", opts: [['steer', 'Steer it'], ['wait', "Wait until it's free"]], short: { steer: 'Steers if working', wait: 'Waits if working' } },
  { key: 'saved', label: "If it's saved", opts: [['wake', 'Wake it'], ['hold', 'Wait until I open it']], short: { wake: 'wakes if saved', hold: 'waits if saved' } },
  { key: 'late', label: "If it's 10+ min late", opts: [['ask', 'Ask me'], ['run', 'Run anyway'], ['skip', 'Skip it']], short: { ask: 'asks if late', run: 'runs if late', skip: 'skips if late' } },
];

// A new session is never working or saved when it is made: only lateness
// applies to it.
export function deliveryRows(target) {
  return target?.kind === 'new' ? DELIVERY_ROWS.filter((r) => r.key === 'late') : DELIVERY_ROWS;
}

export const modelLabel = (spec) => String(spec || '').split('/').pop();

export function deliverySummary(d, target) {
  const v = { ...DEFAULT_DELIVERY, ...(d || {}) };
  const s = deliveryRows(target).map((r) => r.short[v[r.key]]).join(' · ');
  const lead = target?.kind === 'new' ? `${[modelLabel(target.model), target.thinking].filter(Boolean).join(' · ')} · ` : '';
  return lead + s.charAt(0).toUpperCase() + s.slice(1);
}

export function isDefaultDelivery(d) {
  const v = { ...DEFAULT_DELIVERY, ...(d || {}) };
  return v.busy === 'steer' && v.saved === 'wake' && v.late === 'ask';
}

// ── The editor ────────────────────────────────────────────────────────────

const same = (a, b) => JSON.stringify(a ?? null) === JSON.stringify(b ?? null);

export function schedDraft(t) {
  return {
    title: t?.title || '',
    description: t?.description || '',
    subtasks: (t?.subtasks || []).map((s) => ({ title: s.title, done: !!s.done })),
    when: t?.when ? JSON.parse(JSON.stringify(t.when)) : null,
    target: t?.target ? { ...t.target } : null,
    delivery: { ...DEFAULT_DELIVERY, ...(t?.delivery || {}) },
  };
}

const FIELDS = ['title', 'description', 'subtasks', 'when', 'target', 'delivery'];

export function schedChanged(draft, t) {
  const base = schedDraft(t);
  return FIELDS.filter((k) => !same(draft[k], base[k]));
}

export function schedDirty(draft, t) {
  return schedChanged(draft, t).length > 0;
}

// schedulePatch — the revision the edit started from and only what changed.
// A calendar edit carries this device's zone. Runs are never sent: a run
// already made keeps its own snapshot.
export function schedulePatch(draft, t, tz) {
  const body = { revision: t.revision };
  for (const k of schedChanged(draft, t)) {
    body[k] = k === 'title' ? draft.title.trim() : draft[k];
    if (k === 'when') body.tz = tz;
  }
  return body;
}

export function rebaseSchedDraft(draft, before, current) {
  const next = schedDraft(current);
  for (const k of schedChanged(draft, before)) next[k] = draft[k];
  return next;
}

export function scheduleBody(draft, tz) {
  const body = { title: draft.title.trim(), description: draft.description || '', place: 'you' };
  if (draft.subtasks?.length) body.subtasks = draft.subtasks;
  return Object.assign(body, { when: draft.when, tz, target: draft.target, delivery: { ...DEFAULT_DELIVERY, ...(draft.delivery || {}) } });
}

// ── Send later ────────────────────────────────────────────────────────────

export function sendLaterBody(text, when, delivery, sessionId, tz) {
  const full = String(text || '').trim();
  const title = full.split('\n')[0].trim().slice(0, 90);
  return {
    title,
    description: full === title ? '' : full,
    place: 'you',
    when,
    tz,
    target: { kind: 'session', id: sessionId },
    delivery: { ...DEFAULT_DELIVERY, ...(delivery || {}) },
  };
}

// submitSendLater — the draft is cleared only once the server has the
// schedule, and only the text that was scheduled.
export async function submitSendLater({ text, when, delivery, sessionId, tz }, { create, clear, fail }) {
  if (!when || !String(text || '').trim()) return false;
  try {
    await create(sendLaterBody(text, when, delivery, sessionId, tz));
  } catch (error) {
    fail?.(error);
    return false;
  }
  clear(text);
  return true;
}

// ── When: the server's preview ────────────────────────────────────────────

// createWhenPreview — each change asks the server; only the answer to the
// latest question is shown, whatever order the answers arrive in.
export function createWhenPreview(post, onResult) {
  let seq = 0;
  return (text, tz) => {
    const mine = ++seq;
    let asked;
    try { asked = Promise.resolve(post({ text, tz })); } catch (error) { asked = Promise.reject(error); }
    return asked
      .then((res) => { if (mine === seq) onResult(previewResult(res)); })
      .catch((error) => { if (mine === seq) onResult(previewResult(null, error)); });
  };
}

const TRY_ONCE = 'Try “in 20 min”, “friday at 18:00” or “every monday at 9”.';
const PREVIEW_ERRORS = {
  past: 'That time has passed. Pick a later one.',
  repeat: 'Try “every day at 8”, “weekdays at 9” or “every monday at 9”.',
  unknown: TRY_ONCE,
  invalid: TRY_ONCE,
};

function errorBody(error) {
  const msg = String(error?.message || '');
  const i = msg.indexOf('{');
  if (i < 0) return {};
  try { return JSON.parse(msg.slice(i)); } catch { return {}; }
}

export function previewResult(res, error = null) {
  if (error) {
    const body = errorBody(error);
    return { error: PREVIEW_ERRORS[body.code] || TRY_ONCE, code: body.code || 'unknown' };
  }
  if (!res?.when) return null;
  const out = { when: res.when, next: res.next, tz: res.tz };
  if (res.alt) out.alt = res.alt;
  if (res.adjusted) out.adjusted = true;
  return out;
}

// ── Runs, late and failed ─────────────────────────────────────────────────

const REASON_WORDS = {
  session_deleted: 'session deleted',
  session_limit: 'too many sessions are open',
  resume_failed: 'the session could not be reopened',
  steer_queue_full: "the session's queue was full",
  admission_failed: 'the session did not take it',
  owner_missing: 'the owner no longer exists',
  owner_has_no_session: 'the owner has no conversation',
  project_missing: 'the project is gone',
  model_unavailable: 'the model is not available',
  create_failed: 'the session could not be created',
  destination_unverifiable: "moa couldn't tell whether the session was already made",
  destination_ambiguous: 'more than one session claims it',
  template_invalid: 'the schedule is no longer valid',
  busy_wait: 'waiting for the session to be free',
  regated: 'moa restarted before it was sent',
  schedule_deleted: 'the task was deleted',
};

export function failureWords(reason, note = '') {
  if (note) return note;
  return REASON_WORDS[reason] || (reason ? String(reason).replace(/_/g, ' ') : 'the session could not be reached');
}

// lateRun — the newest run waiting for your OK: the banner's Run and Skip
// decide it, by its own revision.
export function lateRun(detail) {
  return (detail?.runs || []).find((r) => r.state === 'late') || null;
}

export function failedRun(detail) {
  const id = detail?.failure?.occurrence_id;
  return (detail?.runs || []).find((r) => (id ? r.id === id : r.state === 'failed')) || null;
}

export function confirmBody(run, action) {
  return { revision: run.revision, action };
}

export function lateBanner(run, now, tz) {
  if (!run) return '';
  const due = whenShort(run.at, now, tz);
  const head = `Was due ${due.charAt(0).toLowerCase()}${due.slice(1)}.`;
  return run.observed_at ? `${head} moa was down until ${clock(run.observed_at, tz)}.` : head;
}

// Reroute is only for a run that failed before it ever reached its session.
export function canReroute(run) {
  return !!run && run.state === 'failed' && !run.admitted_at;
}

// runWord — a run in one word. A run handed over but not yet in its session
// (held, or waiting for the session to be free) is Waiting, never Working.
export function runWord(run) {
  switch (run.state) {
    case 'done': return 'Done';
    case 'late': return 'Waiting for you';
    case 'failed': return 'Not sent';
    case 'skipped': return 'Skipped';
    case 'assigned': return run.admitted_at ? 'Working' : 'Waiting';
    default: return 'Waiting';
  }
}

function runClass(run) {
  const w = runWord(run);
  if (w === 'Working') return 'working';
  if (w === 'Waiting') return 'waiting';
  return run.state;
}

// runOpenSession — a run opens the session it went to, never its child task.
export function runOpenSession(run) {
  return run?.session_id || '';
}

export function runRows(detail) {
  const rows = [];
  if (detail?.next && stateOf(detail) !== 'paused') rows.push({ id: 'next', at: detail.next, word: 'Next', cls: 'next', note: '' });
  for (const r of detail?.runs || []) {
    let note = '';
    if (r.missed_count) note = `${r.missed_count} earlier run${r.missed_count === 1 ? '' : 's'} skipped`;
    else if (r.state === 'failed') note = failureWords(r.reason, r.note);
    else if (r.note && r.state !== 'done') note = r.note;
    rows.push({ id: r.id, at: r.at, word: runWord(r), cls: runClass(r), note, run: r });
  }
  return rows;
}

// schedActions — the foot of a scheduled task's detail with nothing edited.
export function schedActions(t) {
  const s = stateOf(t);
  if (s === 'late') return ['delete', 'lateSkip', 'lateRun'];
  if (s === 'failed') return ['delete', 'reroute'];
  if (s === 'paused') return ['delete', 'resume'];
  if (isRepeat(t)) return ['delete', 'pause', 'skip', 'runNow'];
  return t.next ? ['delete', 'sendNow'] : ['delete'];
}

// ── One session ───────────────────────────────────────────────────────────

// schedPin — the line over the composer: what is scheduled into this
// session, what waits for your OK first, "+N" for the rest. Paused ones stay
// in the list, off the line.
export function schedPin(scheduled, now, tz) {
  const rows = scheduledRows(scheduled).filter((t) => stateOf(t) === 'late' || (stateOf(t) !== 'paused' && t.next));
  if (!rows.length) return null;
  const t = rows[0];
  const late = stateOf(t) === 'late';
  return {
    task: t,
    more: rows.length - 1,
    late,
    key: late ? 'Waiting for you' : 'Scheduled',
    when: late ? 'Run now?' : (t.next - now < 3 * 3600000 ? inWords(t.next, now) : whenShort(t.next, now, tz)),
    byAgent: !!t.created_by_session_id,
  };
}

// scheduleVerdict — the schedule half of a session's Tasks words.
export function scheduleVerdict(scheduled, now, tz) {
  const rows = scheduledRows(scheduled);
  const parts = [];
  const waiting = waitingCount(rows);
  if (waiting) parts.push(`${waiting} waiting`);
  const next = rows.filter((t) => stateOf(t) !== 'paused' && t.next).sort((a, b) => a.next - b.next)[0];
  if (next) parts.push(`next ${noToday(whenShort(next.next, now, tz))}`);
  return parts;
}
