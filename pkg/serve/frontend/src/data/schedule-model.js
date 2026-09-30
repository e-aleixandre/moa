// schedule-model.js — scheduled and recurring tasks on the client, as pure
// decisions (no store, no fetch). A scheduled task is a template: an ordinary
// task record with `when`, `tz`, `target` and `delivery` (docs/serve.md,
// Scheduled tasks). The server owns the calendar: it reads "When" and decides
// every run and every instant, the pickers' and presets' included. What lives
// here is how a schedule is WORDED, in the viewer's zone, what the pickers
// ask the server, and the bodies the editor sends.

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

export function whenWords(when, tz) {
  if (!when) return '';
  return when.kind === 'repeat' ? ruleText(when.rule) : whenLong(when.at, tz);
}

export function whenButton(when, now, tz) {
  if (!when) return '';
  return when.kind === 'repeat' ? ruleShort(when.rule) : whenShort(when.at, now, tz);
}

// Presets the When editor offers before anything is typed: words the server
// reads, like anything typed.
export const WHEN_PRESETS = Object.freeze([
  { label: 'In 20 minutes', text: 'in 20 minutes' },
  { label: 'Tonight', text: 'tonight' },
  { label: 'Tomorrow morning', text: 'tomorrow at 09:00' },
  { label: 'Monday', text: 'monday at 09:00' },
]);

// dateInputValue — the date field's value for an instant the server gave.
export function dateInputValue(ms, tz) {
  const w = wall(ms, tz);
  return `${w.y}-${pad(w.mo)}-${pad(w.d)}`;
}

const DATE_RE = /^(\d{4})-(\d{2})-(\d{2})$/;
const TIME_RE = /^(\d{2}):(\d{2})$/;

// civilDay — the day of the month and weekday a date field names. A civil
// date has no zone and no instant.
function civilDay(date) {
  const m = DATE_RE.exec(String(date || ''));
  if (!m) return null;
  return { d: +m[3], dow: new Date(Date.UTC(+m[1], +m[2] - 1, +m[3])).getUTCDay() };
}

// repeatOptions — the Repeat field, named after the date field's day.
export function repeatOptions(date) {
  const c = civilDay(date) || { d: 1, dow: 1 };
  return [
    { id: 'never', label: 'Never' },
    { id: 'daily', label: 'Every day' },
    { id: 'weekdays', label: 'Weekdays' },
    { id: 'weekly', label: `Every ${WEEKDAY_NAMES[c.dow]}` },
    { id: 'monthly', label: `Monthly on day ${c.d}` },
  ];
}

// pickText — the three fields (date, time, repeat) as words for the server,
// which resolves them like anything typed into When. Null when incomplete.
export function pickText(date, time, repeat) {
  const c = civilDay(date);
  if (!c || !TIME_RE.test(String(time || ''))) return null;
  const t = `at ${time}`;
  switch (repeat) {
    case 'daily': return `every day ${t}`;
    case 'weekdays': return `weekdays ${t}`;
    case 'weekly': return `every ${WEEKDAY_NAMES[c.dow].toLowerCase()} ${t}`;
    case 'monthly': return `every month on the ${ordinal(c.d)} ${t}`;
    default: return `${date} ${t}`;
  }
}

// pickOf — the three fields showing a value the server gave (its next run
// for a repeat); tomorrow at 09:00 while nothing is chosen.
export function pickOf(when, next, tz, now) {
  const at = next ?? (when?.kind === 'once' ? when.at : null);
  const repeat = when?.kind === 'repeat' ? when.rule.freq : 'never';
  return {
    date: dateInputValue(at ?? now + 86400000, tz),
    time: repeat !== 'never' ? hhmm(when.rule.h, when.rule.mi) : at != null ? clock(at, tz) : '09:00',
    repeat,
  };
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
  if (s === 'paused' || !t.next) return '';
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

// modelLabel — the catalog's display name when it knows the model, else the
// bare id without its provider prefix.
export function modelLabel(spec, models = []) {
  const m = (models || []).find((e) => e.id === spec);
  return m?.name || String(spec || '').split('/').pop();
}

export function deliverySummary(d, target, models = []) {
  const v = { ...DEFAULT_DELIVERY, ...(d || {}) };
  const s = deliveryRows(target).map((r) => r.short[v[r.key]]).join(' · ');
  const lead = target?.kind === 'new' ? `${[modelLabel(target.model, models), target.thinking].filter(Boolean).join(' · ')} · ` : '';
  return lead + s.charAt(0).toUpperCase() + s.slice(1);
}

export function isDefaultDelivery(d) {
  const v = { ...DEFAULT_DELIVERY, ...(d || {}) };
  return v.busy === 'steer' && v.saved === 'wake' && v.late === 'ask';
}

// ── The editor ────────────────────────────────────────────────────────────

const same = (a, b) => JSON.stringify(a ?? null) === JSON.stringify(b ?? null);

// schedDraft — the editable fields, and `next`: the server's next run for
// `when`, never sent and never edited on its own.
export function schedDraft(t) {
  return {
    title: t?.title || '',
    description: t?.description || '',
    subtasks: (t?.subtasks || []).map((s) => ({ title: s.title, done: !!s.done })),
    when: t?.when ? JSON.parse(JSON.stringify(t.when)) : null,
    next: t?.next ?? null,
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
  if (schedChanged(draft, before).includes('when')) next.next = draft.next ?? null;
  return next;
}

// schedReady — Schedule and Save need a title, a When the server resolved,
// and somewhere to send it.
export function schedReady(draft) {
  return !!draft?.title?.trim() && !!draft.when && !!draft.target && (draft.target.kind !== 'new' || !!draft.target.model);
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

// ── When: the server's answer ─────────────────────────────────────────────

// createWhenInput — When asks the server one question at a time, whether it
// was typed, picked or a preset. The value is only ever the server's answer
// to the latest question: nothing while it is open (hold) or when it failed,
// and an older answer arriving late is ignored.
export function createWhenInput(post, { onRead, onValue }) {
  let seq = 0;
  const ask = (text, tz) => {
    const mine = ++seq;
    onRead(null);
    onValue(null);
    if (!String(text || '').trim()) return Promise.resolve();
    let asked;
    try { asked = Promise.resolve(post({ text, tz })); } catch (error) { asked = Promise.reject(error); }
    return asked.then((res) => {
      if (mine !== seq) return;
      const r = previewResult(res);
      onRead(r);
      onValue(r?.when ? { when: r.when, next: r.next } : null);
    }, (error) => {
      if (mine === seq) onRead(previewResult(null, error));
    });
  };
  ask.hold = () => { seq++; onRead(null); onValue(null); };
  ask.cancel = () => { seq++; };
  return ask;
}

// whenMountQuestion — what a When editor asks the server when it opens: the
// text that has no answer yet. A value the server already resolved is shown
// as it is; asking its first words again would bring back a choice the user
// has since replaced.
export function whenMountQuestion(text, value) {
  return !value && String(text || '').trim() ? text : null;
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
// decide it, by its own revision. It comes from the detail's decisions (every
// late run, oldest first), never from the page of Runs.
export function lateRun(detail) {
  const d = detail?.decisions || [];
  return d.length ? d[d.length - 1] : null;
}

// awaitsYou — a run of this template waits for your Run or Skip, paused or not.
export function awaitsYou(t) {
  return stateOf(t) === 'late' || (t?.late_count || 0) > 0 || (t?.decisions?.length || 0) > 0;
}

export function failedRun(detail) {
  const id = detail?.failure?.occurrence_id;
  return (detail?.runs || []).find((r) => (id ? r.id === id : r.state === 'failed')) || null;
}

export function confirmBody(run, action) {
  return { revision: run.revision, action };
}

// uncertainRun — a run that was on its way to its session when moa lost
// track of it: it may already have been delivered, so running it again could
// duplicate the work.
export function uncertainRun(run) {
  return run?.reason === 'delivery_uncertain';
}

export function lateBanner(run, now, tz) {
  if (!run) return '';
  const due = whenShort(run.at, now, tz);
  const head = `Was due ${due.charAt(0).toLowerCase()}${due.slice(1)}.`;
  if (uncertainRun(run)) return `${head} It may already have been delivered — running it again could duplicate the work.`;
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

const SKIP_WORDS = {
  superseded: 'A newer run replaced it',
  legacy_delivered: 'Already sent before migration',
};

export function runRows(detail) {
  const rows = [];
  if (detail?.next && stateOf(detail) !== 'paused') rows.push({ id: 'next', at: detail.next, word: 'Next', cls: 'next', note: '' });
  const past = [];
  for (const r of detail?.runs || []) {
    let note = '';
    if (r.state === 'skipped' && SKIP_WORDS[r.reason]) note = SKIP_WORDS[r.reason];
    else if (r.missed_count) note = `${r.missed_count} earlier run${r.missed_count === 1 ? '' : 's'} skipped`;
    else if (r.state === 'failed') note = failureWords(r.reason, r.note);
    else if (r.note && r.state !== 'done') note = r.note;
    past.push({ id: r.id, at: r.at, word: runWord(r), cls: runClass(r), note, run: r });
  }
  return rows.concat(past.sort((a, b) => b.at - a.at));
}

// schedActions — the foot of a scheduled task's detail with nothing edited.
export function schedActions(t) {
  const s = stateOf(t);
  if (s === 'late') return ['delete', 'lateSkip', 'lateRun'];
  if (s === 'failed') return ['delete', 'reroute'];
  if (s === 'paused') return awaitsYou(t) ? ['delete', 'lateSkip', 'lateRun', 'resume'] : ['delete', 'resume'];
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
