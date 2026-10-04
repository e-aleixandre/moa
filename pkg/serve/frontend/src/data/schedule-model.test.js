// schedule-model.test.js — scheduled and recurring tasks on the client
// (DESIGN §8.4). Run with `bun test`. Times are fixed and zoned explicitly so
// the words do not depend on the machine running the tests.
import { test, expect } from 'bun:test';
import {
  attentionCount, canReroute, confirmBody, createWhenInput, deliverySummary, isScheduled, lateBanner, lateRun, modelLabel,
  previewResult, rebaseSchedDraft, runOpenSession, runRows, schedActions, schedDraft, schedEyebrow, schedPin, schedRight,
  scheduleBody, schedulePatch, scheduledRows, sendLaterBody, submitSendLater, targetFromDest, targetName, targetSessionId,
  waitingCount, whenLong, whenShort, ruleShort, ruleText, inWords, DEFAULT_DELIVERY,
} from './schedule-model.js';
import { groupTasks, pinnedLine, sessionRecords, sessionTasksStatus, sessionTasksVerdict } from './tasks-model.js';
import { projectStream } from './stream-model.js';
import { filterCommands } from './composer-suggest.js';

const TZ = 'Europe/Madrid';
// Tue 30 Sep 2026, 16:40 in Madrid (UTC+2).
const NOW = Date.UTC(2026, 8, 30, 14, 40);
const at = (d, h, mi = 0) => Date.UTC(2026, 8, d, h - 2, mi); // Madrid wall → UTC (September/October, UTC+2)

const tmpl = (id, extra = {}) => ({
  id, title: `t${id}`, place: 'you', status: 'pending', revision: 3, tz: TZ,
  when: { kind: 'once', at: at(30, 21) }, target: { kind: 'session', id: 'ci' },
  delivery: { ...DEFAULT_DELIVERY }, schedule_state: 'scheduled', next: at(30, 21), created_at: 1, ...extra,
});
const weekly = (id, extra = {}) => tmpl(id, { when: { kind: 'repeat', rule: { freq: 'weekly', dow: 1, h: 9, mi: 0 } }, next: Date.UTC(2026, 9, 5, 7), ...extra });

const SESSIONS = {
  ci: { id: 'ci', title: 'Fix the flake', state: 'idle', cwd: '/x/moa/main' },
  own: { id: 'own', title: 'moa', state: 'saved', cwd: '/x/moa/main', kind: 'owner' },
};
const OWNERS = [{ id: 'owner-7', session_id: 'own', name: 'moa' }];

// ── Words ────────────────────────────────────────────────────────────────

test('the words for a time are the lab\'s, in the given zone', () => {
  expect(whenShort(at(30, 21), NOW, TZ)).toBe('Today 21:00');
  expect(whenShort(Date.UTC(2026, 9, 1, 1), NOW, TZ)).toBe('Tomorrow 03:00');
  expect(whenLong(Date.UTC(2026, 9, 1, 1), TZ)).toBe('Thu 1 Oct, 03:00');
  expect(inWords(NOW + 20 * 60000, NOW)).toBe('in 20 min');
  expect(ruleText({ freq: 'weekly', dow: 1, h: 9, mi: 0 })).toBe('Every Monday, 09:00');
  expect(ruleShort({ freq: 'daily', h: 7, mi: 30 })).toBe('Daily 07:30');
  expect(ruleText({ freq: 'monthly', dom: 1, h: 9, mi: 0 })).toBe('Monthly on the 1st, 09:00');
});

// ── scheduledGroupingAndAttentionCount ───────────────────────────────────

test('scheduledGroupingAndAttentionCount', () => {
  const list = [
    tmpl(1, { schedule_state: 'late', late_count: 2 }),
    weekly(2, { schedule_state: 'paused', late_count: 1 }),
    { id: 3, title: 'r1', place: 'you', status: 'pending', requester_session_id: 'ci', created_at: 5 },
    { id: 4, title: 'r2', place: 'you', status: 'pending', requester_session_id: 'ci', created_at: 6 },
    tmpl(5, { schedule_state: 'failed', failure: { occurrence_id: 9, reason: 'session_deleted' } }),
    { id: 6, title: 'note', place: 'you', status: 'pending', created_at: 7 },
    tmpl(7, { when: { kind: 'once', at: at(30, 23) }, next: at(30, 23) }),
    { id: 8, title: 'child', place: 'agent', status: 'pending', assignee_session_id: 'ci', parent_task_id: 7, occurrence_id: 2 },
  ];
  // The Scheduled group holds the templates, never You or a checklist.
  const groups = groupTasks(list, { agents: true, sessions: SESSIONS });
  const you = groups.find((g) => g.id === 'you');
  expect(you.rows.map((t) => t.id)).toEqual([4, 3, 6]);
  expect(groups.flatMap((g) => g.rows).some(isScheduled)).toBe(false);
  expect(groups.find((g) => g.id === 'agent:ci').rows.map((t) => t.id)).toEqual([8]);

  // What needs you first, then by next run, paused at the end.
  const rows = scheduledRows(list);
  expect(rows.map((t) => t.id)).toEqual([1, 5, 7, 2]);
  // "N waiting for you" counts runs, a paused template's late run included.
  expect(waitingCount(rows)).toBe(3);

  // The footer is the server's attention count, whatever the view shows.
  const slice = { list, counts: { open_requests: 2, late_occurrences: 3, attention: 5 }, session: 'ci' };
  expect(attentionCount(slice)).toBe(5);
  // An older server has no attention: the open requests it lists.
  expect(attentionCount({ list, counts: { open_requests: 2 } })).toBe(2);
  expect(attentionCount({ list, counts: {} })).toBe(2);

  // The row's right edge: late says when it was due; soon is in words.
  expect(schedRight(rows[0], NOW, TZ)).toBe('was 21:00');
  expect(schedRight(tmpl(9, { next: NOW + 20 * 60000 }), NOW, TZ)).toBe('in 20 min');
  expect(schedRight(weekly(11, { schedule_state: 'paused', next: Date.UTC(2026, 9, 5, 7) }), NOW, TZ)).toBe('');
  expect(modelLabel('anthropic/claude-sonnet-5-5', [{ id: 'anthropic/claude-sonnet-5-5', name: 'Claude Sonnet 5.5' }])).toBe('Claude Sonnet 5.5');
  expect(modelLabel('anthropic/x')).toBe('x');
  expect(schedRight(weekly(10, { next: Date.UTC(2026, 9, 5, 7) }), NOW, TZ)).toBe('Mon 09:00');
  expect(schedEyebrow(rows[0])).toBe('Waiting for you');
  expect(schedEyebrow(rows[1])).toBe('Not sent');
  expect(schedEyebrow(rows[3])).toBe('Paused');
  expect(schedEyebrow(weekly(11))).toBe('Recurring');
  expect(schedEyebrow(tmpl(12))).toBe('Scheduled');
});

// ── scheduledEditorKeepsOccurrenceSnapshots ──────────────────────────────

test('scheduledEditorKeepsOccurrenceSnapshots', () => {
  const base = weekly(2, { title: 'Weekly report', description: 'd', runs: [{ id: 30, state: 'assigned', revision: 4 }] });
  const draft = schedDraft(base);
  expect(schedulePatch(draft, base, 'Europe/Madrid')).toEqual({ revision: 3 });

  // A calendar edit sends the canonical when with the device zone and the
  // revision the edit started from — never the runs.
  const edited = { ...draft, when: { kind: 'repeat', rule: { freq: 'weekly', dow: 1, h: 7, mi: 0 } } };
  const body = schedulePatch(edited, base, 'Atlantic/Canary');
  expect(body).toEqual({ revision: 3, when: { kind: 'repeat', rule: { freq: 'weekly', dow: 1, h: 7, mi: 0 } }, tz: 'Atlantic/Canary' });
  expect('runs' in body).toBe(false);

  // Target and delivery are sent whole.
  const moved = { ...draft, title: ' New ', target: { kind: 'owner', id: 'owner-7' }, delivery: { busy: 'wait', saved: 'wake', late: 'skip' } };
  expect(schedulePatch(moved, base, TZ)).toEqual({
    revision: 3, title: 'New', target: { kind: 'owner', id: 'owner-7' }, delivery: { busy: 'wait', saved: 'wake', late: 'skip' },
  });

  // A new-session target read back from the server is sent back as it was.
  const nb = tmpl(4, { target: { kind: 'new', project: 'moa', cwd: '/x/moa/main', model: 'anthropic/claude-sonnet-5-5', thinking: 'low' } });
  expect(schedulePatch({ ...schedDraft(nb), delivery: { ...DEFAULT_DELIVERY, late: 'run' } }, nb, TZ)).toEqual({
    revision: 3, delivery: { busy: 'steer', saved: 'wake', late: 'run' },
  });

  // Create: canonical when + tz + target + delivery, always private.
  expect(scheduleBody({ ...edited, subtasks: [{ title: 's', done: false }] }, 'Europe/Madrid')).toEqual({
    title: 'Weekly report', description: 'd', place: 'you', subtasks: [{ title: 's', done: false }],
    when: { kind: 'repeat', rule: { freq: 'weekly', dow: 1, h: 7, mi: 0 } }, tz: 'Europe/Madrid',
    target: { kind: 'session', id: 'ci' }, delivery: { busy: 'steer', saved: 'wake', late: 'ask' },
  });

  // 409: the task as it is now, with the owner's edits kept.
  const current = { ...base, revision: 5, title: 'Renamed elsewhere', delivery: { busy: 'wait', saved: 'hold', late: 'ask' } };
  const rebased = rebaseSchedDraft(edited, base, current);
  expect(rebased.title).toBe('Renamed elsewhere');
  expect(rebased.when).toEqual(edited.when);
  expect(rebased.delivery).toEqual({ busy: 'wait', saved: 'hold', late: 'ask' });
  expect(schedulePatch(rebased, current, TZ).revision).toBe(5);
});

// ── scheduledTargetPickerUsesCurrentLevels ───────────────────────────────

test('scheduledTargetPickerUsesCurrentLevels', () => {
  // The Move picker's owner row carries the owner's SESSION; a schedule
  // needs the owner ENTITY.
  expect(targetFromDest({ place: 'agent', sessionId: 'own', owner: OWNERS[0] })).toEqual({ kind: 'owner', id: 'owner-7' });
  expect(targetFromDest({ place: 'agent', sessionId: 'ci' })).toEqual({ kind: 'session', id: 'ci' });
  expect(targetFromDest({ place: 'new', key: 'moa', cwd: '/x/moa/wt' }, { model: 'anthropic/claude-sonnet-5-5', thinking: 'medium' })).toEqual({
    kind: 'new', project: 'moa', cwd: '/x/moa/wt', model: 'anthropic/claude-sonnet-5-5', thinking: 'medium',
  });
  expect(targetName({ kind: 'owner', id: 'owner-7' }, SESSIONS, OWNERS)).toBe('moa');
  expect(targetName({ kind: 'session', id: 'ci' }, SESSIONS, OWNERS)).toBe('Fix the flake');
  expect(targetName({ kind: 'session', id: 'gone' }, SESSIONS, OWNERS)).toBe('Deleted session');
  expect(targetName({ kind: 'new', project: 'moa', cwd: '/x/moa/main' }, SESSIONS, OWNERS)).toBe('New session · moa');
});

// ── scheduledOpenUsesExistingNavigation ──────────────────────────────────

test('scheduledOpenUsesExistingNavigation', () => {
  expect(targetSessionId({ kind: 'session', id: 'ci' }, OWNERS)).toBe('ci');
  expect(targetSessionId({ kind: 'owner', id: 'owner-7' }, OWNERS)).toBe('own');
  expect(targetSessionId({ kind: 'new', project: 'moa' }, OWNERS)).toBe('');
  // A run opens its session, never its child task's id.
  expect(runOpenSession({ id: 12, state: 'done', child_task_id: 44, session_id: 'sess-9' })).toBe('sess-9');
  expect(runOpenSession({ id: 13, state: 'done', child_task_id: 45 })).toBe('');
});

// ── sendLaterPreservesDraftUntilCommitted ────────────────────────────────

test('sendLaterPreservesDraftUntilCommitted', async () => {
  const text = 'If the pipeline of !482 is green, merge it and delete the branch.\nThen tell me.';
  const when = { kind: 'once', at: at(30, 18) };
  expect(sendLaterBody(text, when, DEFAULT_DELIVERY, 'ci', TZ)).toEqual({
    title: 'If the pipeline of !482 is green, merge it and delete the branch.', description: text, place: 'you',
    when, tz: TZ, target: { kind: 'session', id: 'ci' }, delivery: DEFAULT_DELIVERY,
  });
  const long = 'x'.repeat(120);
  expect(sendLaterBody(long, when, DEFAULT_DELIVERY, 'ci', TZ).title).toBe('x'.repeat(90));
  expect(sendLaterBody('short', when, DEFAULT_DELIVERY, 'ci', TZ).description).toBe('');

  const cleared = [];
  // No time read yet: nothing is created and the draft stays.
  let created = 0;
  expect(await submitSendLater({ text, when: null, sessionId: 'ci', tz: TZ }, { create: async () => { created++; }, clear: (t) => cleared.push(t) })).toBe(false);
  // The create fails: the draft stays.
  expect(await submitSendLater({ text, when, sessionId: 'ci', tz: TZ }, { create: async () => { throw new Error('400: nope'); }, clear: (t) => cleared.push(t) })).toBe(false);
  expect(cleared).toEqual([]);
  expect(created).toBe(0);
  // Success clears exactly the text that was scheduled.
  const sent = [];
  expect(await submitSendLater({ text, when, sessionId: 'ci', tz: TZ }, { create: async (b) => { sent.push(b); return { id: 1 }; }, clear: (t) => cleared.push(t) })).toBe(true);
  expect(sent[0].when).toEqual(when);
  expect(cleared).toEqual([text]);
});

// ── scheduledWhenUsesServerPreview ───────────────────────────────────────

test('scheduledWhenUsesServerPreview', async () => {
  const pending = [];
  const post = (body) => new Promise((resolve, reject) => pending.push({ body, resolve, reject }));
  const seen = [];
  const preview = createWhenInput(post, { onRead() {}, onValue: (v) => { if (v) seen.push(v); } });
  preview('at 5', TZ);
  preview('at 5pm', TZ);
  expect(pending.map((p) => p.body)).toEqual([{ text: 'at 5', tz: TZ }, { text: 'at 5pm', tz: TZ }]);
  // The newer answer lands first; the older one arriving later is ignored.
  pending[1].resolve({ when: { kind: 'once', at: at(30, 17) }, tz: TZ, next: at(30, 17) });
  await Promise.resolve(); await Promise.resolve();
  pending[0].resolve({ when: { kind: 'once', at: Date.UTC(2026, 9, 1, 3) }, tz: TZ, next: Date.UTC(2026, 9, 1, 3), alt: at(30, 17) });
  await Promise.resolve(); await Promise.resolve();
  expect(seen).toHaveLength(1);
  expect(seen[0].when).toEqual({ kind: 'once', at: at(30, 17) });

  // The server's canonical value, its other half of the day and a DST move.
  expect(previewResult({ when: { kind: 'once', at: 1 }, tz: TZ, next: 1, alt: 2, adjusted: true })).toEqual({
    when: { kind: 'once', at: 1 }, next: 1, alt: 2, adjusted: true, tz: TZ,
  });
  expect(previewResult({ tz: TZ })).toBe(null);
  const err = (code) => Object.assign(new Error(`400: {"code":"${code}","error":"x"}`), { status: 400 });
  expect(previewResult(null, err('past')).error).toBe('That time has passed. Pick a later one.');
  expect(previewResult(null, err('unknown')).error).toBe('Try “in 20 min”, “friday at 18:00” or “every monday at 9”.');
  expect(previewResult(null, err('repeat')).error).toBe('Try “every day at 8”, “weekdays at 9” or “every monday at 9”.');
});

test('runRows lists runs newest first whatever order the server sends', () => {
  const detail = weekly(2, { next: at(31, 9), runs: [
    { id: 1, revision: 1, at: at(28, 9), state: 'done' }, { id: 3, revision: 1, at: at(30, 9), state: 'done' }, { id: 2, revision: 1, at: at(29, 9), state: 'done' },
  ] });
  expect(runRows(detail).map((r) => r.id)).toEqual(['next', 3, 2, 1]);
});

// ── scheduledLateFailureControls ─────────────────────────────────────────

test('scheduledLateFailureControls', () => {
  const detail = weekly(2, {
    schedule_state: 'late', late_count: 2,
    decisions: [
      { id: 20, revision: 2, at: at(29, 9), state: 'late', observed_at: at(29, 9, 20) },
      { id: 21, revision: 7, at: at(30, 9), state: 'late', observed_at: at(30, 9, 12), missed_count: 3 },
    ],
    runs: [
      { id: 21, revision: 7, at: at(30, 9), state: 'late', observed_at: at(30, 9, 12), missed_count: 3 },
      { id: 20, revision: 2, at: at(29, 9), state: 'late', observed_at: at(29, 9, 20) },
      { id: 19, revision: 5, at: at(28, 9), state: 'assigned', notice_id: 'tn_1', session_id: 'ci' },
      { id: 18, revision: 5, at: at(27, 9), state: 'assigned', admitted_at: at(27, 9), session_id: 'ci' },
      { id: 17, revision: 5, at: at(26, 9), state: 'ready' },
      { id: 16, revision: 3, at: at(25, 9), state: 'failed', reason: 'session_deleted' },
      { id: 15, revision: 3, at: at(24, 9), state: 'skipped' },
      { id: 14, revision: 3, at: at(23, 9), state: 'done', session_id: 'ci', child_task_id: 99 },
    ],
  });
  // The newest late run is the one the banner decides, by its own revision.
  expect(lateRun(detail).id).toBe(21);
  expect(confirmBody(lateRun(detail), 'run')).toEqual({ revision: 7, action: 'run' });
  expect(confirmBody(lateRun(detail), 'skip')).toEqual({ revision: 7, action: 'skip' });
  expect(lateBanner(lateRun(detail), NOW, TZ)).toBe('Was due today 09:00. moa was down until 09:12.');

  const words = runRows(detail, NOW, TZ).map((r) => [r.id, r.word, r.note]);
  expect(words).toEqual([
    ['next', 'Next', ''],
    [21, 'Waiting for you', '3 earlier runs skipped'],
    [20, 'Waiting for you', ''],
    [19, 'Waiting', ''],
    [18, 'Working', ''],
    [17, 'Waiting', ''],
    [16, 'Not sent', 'session deleted'],
    [15, 'Skipped', ''],
    [14, 'Done', ''],
  ]);

  // Reroute only a failed run that never reached its session.
  expect(canReroute({ state: 'failed' })).toBe(true);
  expect(canReroute({ state: 'failed', admitted_at: 5 })).toBe(false);
  expect(canReroute({ state: 'assigned' })).toBe(false);

  expect(schedActions(weekly(3))).toEqual(['delete', 'pause', 'skip', 'runNow']);
  expect(schedActions(tmpl(3))).toEqual(['delete', 'sendNow']);
  expect(schedActions(weekly(3, { schedule_state: 'paused' }))).toEqual(['delete', 'resume']);
  expect(schedActions(detail)).toEqual(['delete', 'lateSkip', 'lateRun']);
  expect(schedActions(tmpl(3, { schedule_state: 'failed', failure: { occurrence_id: 16 } }))).toEqual(['delete', 'reroute']);

  expect(deliverySummary(DEFAULT_DELIVERY, { kind: 'session', id: 'ci' })).toBe('Steers if working · wakes if saved · asks if late');
  expect(deliverySummary({ busy: 'wait', saved: 'hold', late: 'skip' }, { kind: 'new', model: 'anthropic/claude-sonnet-5-5', thinking: 'medium' }))
    .toBe('claude-sonnet-5-5 · medium · Skips if late');
});

// ── scheduledSessionStatusAndPinnedLine ──────────────────────────────────

test('scheduledSessionStatusAndPinnedLine', () => {
  // Only a self-made schedule: the status line still has a Tasks entry.
  const own = { requests: [], checklist: [], scheduled: [tmpl(1, { created_by_session_id: 'ci', next: at(30, 17) })] };
  expect(sessionTasksVerdict(own, NOW, TZ)).toBe('next 17:00');
  expect(sessionTasksStatus(own, NOW, TZ)).toEqual({ text: 'next 17:00', forYou: 0, short: 'next 17:00' });
  expect(schedPin(own.scheduled, NOW, TZ)).toMatchObject({ task: { id: 1 }, more: 0, late: false, key: 'Scheduled', when: 'in 20 min', byAgent: true });

  // A late run and a request: the request keeps the pinned line; the late
  // run is what the schedule pin shows first.
  const mixed = {
    requests: [{ id: 5, title: 'Pick a name', status: 'pending', created_at: 1 }],
    checklist: [{ id: 6, title: 'a', status: 'done' }, { id: 7, title: 'b', status: 'pending' }, { id: 8, title: 'c', status: 'done' }],
    scheduled: [tmpl(2, { next: at(30, 17) }), weekly(3, { schedule_state: 'late', late_count: 1 }), weekly(4, { schedule_state: 'paused' })],
  };
  expect(pinnedLine(sessionRecords(mixed, 'ci')).task.id).toBe(5);
  const pin = schedPin(mixed.scheduled, NOW, TZ);
  expect(pin.task.id).toBe(3);
  expect(pin.late).toBe(true);
  expect(pin.key).toBe('Waiting for you');
  expect(pin.when).toBe('Run now?');
  expect(pin.more).toBe(1);
  expect(sessionTasksVerdict(mixed, NOW, TZ)).toBe('1 for you · 1 waiting · next 17:00 · 2/3');
  // Without schedules the words are unchanged.
  expect(sessionTasksVerdict({ requests: [], checklist: [{ id: 1, status: 'done' }] })).toBe('1/1');
});

// ── scheduledTranscriptRendering ─────────────────────────────────────────

test('scheduledTranscriptRendering', () => {
  const custom = {
    source: 'event', source_name: 'tasks', id: 'tn_9', kind: 'assigned', task_id: 44, autorun: true,
    parent_task_id: 7, occurrence_id: 12, due_at: Date.UTC(2026, 8, 30, 1), tz: TZ, sent_at: Date.UTC(2026, 8, 30, 7, 14),
    confirmed_at: Date.UTC(2026, 8, 30, 7, 13), title: 'Scheduled task · nightly · due 03:00, sent 09:14 after your OK',
  };
  const blocks = projectStream({ id: 's', messages: [
    { role: 'user', content: [{ type: 'text', text: 'run the nightly job' }], custom, _msg_id: 'm1', timestamp: 1790000000 },
    { role: 'user', content: [{ type: 'text', text: 'old prompt' }], custom: { source: 'schedule', scheduled_for: '2026-09-30T01:00:00Z', delivered_at: '2026-09-30T01:00:10Z' }, _msg_id: 'm2' },
  ] });
  expect(blocks[0]).toMatchObject({
    kind: 'event', source: 'scheduled', title: 'Scheduled task · nightly · due 03:00, sent 09:14 after your OK', body: 'run the nightly job',
    taskId: 44, parentTaskId: 7,
  });
  expect(blocks[1]).toMatchObject({ kind: 'event', source: 'scheduled', title: 'old prompt' });
});

test('the composer no longer suggests /schedule', () => {
  expect(filterCommands('sch').map((c) => c.name)).not.toContain('schedule');
});

// ── Review fixes: the server owns every instant ─────────────────────────

import * as model from './schedule-model.js';

const deferred = () => {
  const pending = [];
  const post = (body) => new Promise((resolve, reject) => pending.push({ body, resolve, reject }));
  return { pending, post };
};
const flush = async () => { for (let i = 0; i < 4; i++) await Promise.resolve(); };
const parseErr = (code) => Object.assign(new Error(`400: {"code":"${code}","error":"x"}`), { status: 400 });

test('the When pickers and presets ask the server and keep exactly its answer', async () => {
  // No client calendar: nothing turns a wall time into an instant here.
  for (const name of ['zoned', 'fromInputs', 'pickedWhen', 'nextOf', 'whenNext', 'presets']) expect(model[name]).toBeUndefined();
  expect(model.WHEN_PRESETS.map((p) => p.text)).toEqual(['in 20 minutes', 'tonight', 'tomorrow at 09:00', 'monday at 09:00']);
  expect(model.WHEN_PRESETS.every((p) => p.at === undefined)).toBe(true);

  // Madrid, Sun 28 Mar 2027 02:30 does not exist. The pickers say what was
  // picked, in words; the server decides the instant and the rule.
  expect(model.pickText('2027-03-28', '02:30', 'never')).toBe('2027-03-28 at 02:30');
  expect(model.pickText('2027-03-28', '02:30', 'daily')).toBe('every day at 02:30');
  expect(model.pickText('2027-03-28', '02:30', 'weekdays')).toBe('weekdays at 02:30');
  expect(model.pickText('2027-03-28', '02:30', 'weekly')).toBe('every sunday at 02:30');
  expect(model.pickText('2027-03-28', '02:30', 'monthly')).toBe('every month on the 28th at 02:30');
  expect(model.pickText('', '02:30', 'never')).toBe(null);

  const { pending, post } = deferred();
  const values = [];
  const ask = model.createWhenInput(post, { onRead() {}, onValue: (v) => values.push(v) });
  ask(model.pickText('2027-03-28', '02:30', 'daily'), 'Europe/Madrid');
  expect(pending[0].body).toEqual({ text: 'every day at 02:30', tz: 'Europe/Madrid' });
  // Whatever the server says is what is saved, even an instant no client
  // arithmetic would produce.
  const server = { when: { kind: 'repeat', rule: { freq: 'daily', h: 2, mi: 30 } }, tz: 'Europe/Madrid', next: 1806204612345, adjusted: true };
  pending[0].resolve(server);
  await flush();
  expect(values.at(-1)).toEqual({ when: server.when, next: 1806204612345 });

  ask(model.pickText('2027-03-28', '02:30', 'never'), 'Europe/Madrid');
  pending[1].resolve({ when: { kind: 'once', at: Date.UTC(2027, 2, 28, 1, 30) }, tz: 'Europe/Madrid', next: Date.UTC(2027, 2, 28, 1, 30), adjusted: true });
  await flush();
  expect(values.at(-1)).toEqual({ when: { kind: 'once', at: Date.UTC(2027, 2, 28, 1, 30) }, next: Date.UTC(2027, 2, 28, 1, 30) });
});

test('an invalid When clears the value, and a stale answer never brings it back', async () => {
  const { pending, post } = deferred();
  const values = [];
  const reads = [];
  const ask = model.createWhenInput(post, { onRead: (r) => reads.push(r), onValue: (v) => values.push(v) });
  ask('tomorrow at 09:00', TZ);
  pending[0].resolve({ when: { kind: 'once', at: at(31, 9) }, tz: TZ, next: at(31, 9) });
  await flush();
  expect(values.at(-1)?.when).toEqual({ kind: 'once', at: at(31, 9) });

  // Typing again: nothing is chosen until the server answers the new text.
  ask.hold();
  expect(values.at(-1)).toBe(null);
  ask('tomorrow at 10:00', TZ);
  ask('today at 99:90', TZ);
  pending[2].reject(parseErr('invalid'));
  await flush();
  expect(values.at(-1)).toBe(null);
  expect(reads.at(-1).error).toBeTruthy();
  // The older question answers last: ignored.
  pending[1].resolve({ when: { kind: 'once', at: at(31, 10) }, tz: TZ, next: at(31, 10) });
  await flush();
  expect(values.at(-1)).toBe(null);

  // Neither Schedule nor Save is offered without a value.
  const draft = { ...schedDraft(null), title: 'x', target: { kind: 'session', id: 'ci' } };
  expect(model.schedReady({ ...draft, when: null })).toBe(false);
  expect(model.schedReady({ ...draft, when: { kind: 'once', at: at(31, 9) } })).toBe(true);
});

test('late decisions come from the detail, even beyond Runs and under a pause', () => {
  const runs = Array.from({ length: 20 }, (_, i) => ({ id: 40 - i, revision: 1, at: at(30, 9) - i * 86400000, state: 'done' }));
  const detail = weekly(2, { schedule_state: 'late', late_count: 1, runs, decisions: [{ id: 3, revision: 6, at: at(1, 9), state: 'late' }] });
  expect(lateRun(detail).id).toBe(3);
  expect(confirmBody(lateRun(detail), 'run')).toEqual({ revision: 6, action: 'run' });

  const paused = weekly(2, { schedule_state: 'paused', late_count: 1, decisions: [{ id: 3, revision: 6, at: at(1, 9), state: 'late' }] });
  expect(schedActions(paused)).toEqual(['delete', 'lateSkip', 'lateRun', 'resume']);
  expect(model.awaitsYou(paused)).toBe(true);
  expect(model.awaitsYou(weekly(2, { schedule_state: 'paused' }))).toBe(false);
});

test('skipped runs say why: replaced by a newer run, or sent before migration', () => {
  const detail = weekly(2, { runs: [
    { id: 2, revision: 1, at: at(29, 9), state: 'skipped', reason: 'superseded', note: 'Superseded by a later run' },
    { id: 1, revision: 1, at: at(28, 9), state: 'skipped', reason: 'legacy_delivered', note: 'Already sent before migration' },
  ] });
  expect(runRows(detail).filter((r) => r.run).map((r) => [r.word, r.note])).toEqual([
    ['Skipped', 'A newer run replaced it'],
    ['Skipped', 'Already sent before migration'],
  ]);
});

test('an uncertain delivery warns that running it again could duplicate the work', () => {
  const run = { id: 5, revision: 3, at: at(30, 9), state: 'late', reason: 'delivery_uncertain', observed_at: at(30, 9) };
  expect(model.uncertainRun(run)).toBe(true);
  expect(model.uncertainRun({ ...run, reason: 'regated' })).toBe(false);
  expect(lateBanner(run, NOW, TZ)).toBe('Was due today 09:00. It may already have been delivered — running it again could duplicate the work.');
});
