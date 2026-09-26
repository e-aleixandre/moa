import { test, expect } from 'bun:test';
import { readFileSync } from 'node:fs';
import { foregroundLine, liveBarModel, panelHasOverflow, canStopForeground } from './LiveBar.jsx';
import { liveTrayAgents } from '../../data/stream-model.js';

// The live bar is the ONE row of live work above the composer, the merge of the
// old now-line (the foreground phrase) and the old dock (the async work). These
// cover the DECISIONS — who owns the single sentence, when a timer runs, when
// the run is parked on you, and when there is a tally at all. The rendering is a
// class away and carries no logic.

// ── The foreground sentence (what the now-line used to decide) ─────────────

test('an idle session has no foreground sentence at all', () => {
  expect(foregroundLine({ state: 'idle' }, 1000)).toBe(null);
  expect(foregroundLine(null, 1000)).toBe(null);
});

test('a working session reads its in-flight action with a live elapsed timer', () => {
  const session = {
    state: 'running',
    runStartedAtMs: 1000,
    messages: [{ _type: 'tool_start', tool_name: 'grep', args: {}, status: 'running' }],
  };
  expect(foregroundLine(session, 13000)).toMatchObject({
    text: 'Searching the code…',
    waiting: false,
    elapsed: '12s',
  });
});

test('waiting on the user drops the timer and flags the amber state', () => {
  const session = { state: 'permission', runStartedAtMs: 1000 };
  expect(foregroundLine(session, 13000)).toMatchObject({
    text: 'Waiting for you',
    waiting: true,
    elapsed: '',
  });
});

test('a catalogue fixture phrase wins over activityText, which the adapter needs', () => {
  const session = { state: 'running', runStartedAtMs: 1000, liveLabel: 'Running go vet' };
  expect(foregroundLine(session, 5000)).toMatchObject({
    text: 'Running go vet',
    waiting: false,
    elapsed: '4s',
  });
});

// Without the server-stamped origin there is nothing honest to count from, so
// the phrase stands alone rather than counting from the client's clock.
test('a run with no server-stamped start shows the phrase without a timer', () => {
  const session = { state: 'running', thinkingText: 'hmm' };
  expect(foregroundLine(session, 13000)).toMatchObject({
    text: 'Thinking…',
    waiting: false,
    elapsed: '',
  });
});

// Compacting/verifying are real work, but momentary and not anchored to the
// run's start, so they read as in-progress copy without an age counter.
test('compacting shows in-progress copy without a timer', () => {
  const session = { state: 'running', compacting: true, runStartedAtMs: 1000 };
  expect(foregroundLine(session, 13000)).toMatchObject({
    text: 'Compacting context…',
    waiting: false,
    elapsed: '',
  });
});

// ── The whole bar: one sentence, one tally ─────────────────────────────────

const IDLE = { state: 'idle' };
const WORKING = {
  state: 'running',
  runStartedAtMs: 1000,
  messages: [{ _type: 'tool_start', tool_name: 'grep', args: {}, status: 'running' }],
};
const AGENTS = [
  { id: 'a1', kind: 'subagent', name: 'scout', accent: 'sky', action: 'reading pkg/serve', time: '1m12s' },
  { id: 'b1', kind: 'bash', name: 'bash', action: 'go test ./...', time: '4m18s' },
];

// Repose is the absence of the bar, not an empty bar: the transcript takes the
// space back.
test('nothing in the foreground and nothing in the background means no bar', () => {
  expect(liveBarModel(IDLE, [], 13000)).toBe(null);
  expect(liveBarModel(null, [], 13000)).toBe(null);
});

test('the foreground owns the single sentence even when async work is alive', () => {
  const model = liveBarModel(WORKING, AGENTS, 13000, 0);
  expect(model.sentence.kind).toBe('foreground');
  expect(model.sentence.text).toBe('Searching the code…');
  // The background is present, but only as a count — no second verb.
  expect(model.tally.count).toBe(2);
  expect(model.sentence.agent).toBeUndefined();
});

test('an ended turn owns the sentence while background work stays in the tally', () => {
  const model = liveBarModel(IDLE, AGENTS, 13000, 0);
  expect(model.sentence.kind).toBe('ended');
  expect(model.sentence.elapsed).toBe('');
  expect(model.tally.count).toBe(2);
});

// The tally is the door to the panel, so it must not exist when there is
// nothing behind it.
test('there is no tally when nothing async is alive', () => {
  const model = liveBarModel(WORKING, [], 13000);
  expect(model.sentence.kind).toBe('foreground');
  expect(model.tally).toBe(null);
});

test('a foreground parked on you keeps the sentence amber and timerless', () => {
  const model = liveBarModel({ state: 'permission', runStartedAtMs: 1000 }, AGENTS, 13000);
  expect(model.sentence.waiting).toBe(true);
  expect(model.sentence.elapsed).toBe('');
  expect(model.tally.count).toBe(2);
});

// The tally speaks the owner row's state language: the number only, amber
// when something waits on you, never one mark per item.
const RUNNING_CHILD = { id: 's1', kind: 'session', name: 'Deploy notes', state: 'running', action: 'Running' };
const WAITING_CHILD = { id: 's2', kind: 'session', name: 'Push pairing', state: 'permission', action: 'Waiting for permission' };

test('the tally is amber only when a child session waits on you', () => {
  expect(liveBarModel(IDLE, AGENTS, 13000).tally).toEqual({ count: 2, waiting: false });
  expect(liveBarModel(IDLE, [...AGENTS, RUNNING_CHILD], 13000).tally).toEqual({ count: 3, waiting: false });
  expect(liveBarModel(IDLE, [...AGENTS, RUNNING_CHILD, WAITING_CHILD], 13000).tally).toEqual({ count: 4, waiting: true });
  // A subagent or a command carries no waiting state of its own.
  expect(liveBarModel(IDLE, [{ ...AGENTS[0], state: 'permission' }], 13000).tally.waiting).toBe(false);
});

// A child that stopped with an error waits on you exactly as one asking for
// permission does: the owner row already says so, and the tally must agree.
test('an errored child session is counted and turns the tally amber', () => {
  const ownerSession = { id: 'owner-session', messages: [], subagents: {} };
  const sessions = {
    run: { id: 'run', ownerId: 'o1', title: 'Deploy notes', state: 'running' },
    bad: { id: 'bad', ownerId: 'o1', title: 'Migrate stock', state: 'error' },
  };
  const agents = liveTrayAgents(ownerSession, sessions, [{ id: 'o1', session_id: 'owner-session' }]);
  expect(liveBarModel(IDLE, agents, 13000).tally).toEqual({ count: 2, waiting: true });
  expect(liveBarModel(IDLE, agents.filter((a) => a.id !== 'bad'), 13000).tally).toEqual({ count: 1, waiting: false });
});

test('the tally chip draws the number and no mark per item', () => {
  const src = readFileSync(new URL('./LiveBar.jsx', import.meta.url), 'utf8');
  const chip = src.slice(src.indexOf('class={`zl-live-tally'), src.indexOf('</button>', src.indexOf('class={`zl-live-tally')));
  expect(chip).toContain('is-waiting');
  expect(chip).toContain('zl-live-n');
  expect(chip).not.toContain('LiveId');
  expect(chip).not.toContain('StateDot');
  expect(chip).not.toContain('zl-live-dots');
  const css = readFileSync(new URL('./LiveBar.css', import.meta.url), 'utf8');
  expect(css).not.toContain('zl-live-dots');
  expect(css).toMatch(/\.zl-live-tally\.is-waiting\s*\{[^}]*color:\s*var\(--zl-yellow\)/);
});

test('an ended turn never carries a background clock', () => {
  expect(liveBarModel(IDLE, AGENTS, 13000).sentence.elapsed).toBe('');
});

test('a missing or malformed agent list is simply no background', () => {
  expect(liveBarModel(WORKING, undefined, 13000).tally).toBe(null);
  expect(liveBarModel(IDLE, undefined, 13000)).toBe(null);
});

test('the panel wash is reserved for rows that are actually below its cap', () => {
  expect(panelHasOverflow({ scrollHeight: 182, clientHeight: 208 })).toBe(false);
  expect(panelHasOverflow({ scrollHeight: 294, clientHeight: 208 })).toBe(true);
});

test('the bar carries the catalogue classes, not a translation of them', () => {
  const src = readFileSync(new URL('./LiveBar.jsx', import.meta.url), 'utf8');
  expect(src).toContain('zl-live');
  expect(src).toContain('zl-live-tally');
  expect(src).toContain('zl-live-dot');
  expect(src).not.toContain('class={`livebar');
  expect(src).not.toContain('"lb-now"');
  expect(src).not.toContain('"lb-tally"');
});

// ── The counter's place ────────────────────────────────────────────────────
// The elapsed counter is the sentence's next sibling in both rows, and the
// sentence used to size to its words: every change of verb moved the counter.
// The rule that anchors it is CSS, so the guard reads the sheet: the sentence
// must be the flexible item and the counter must be pushed to the end of the
// slot, in the foreground row AND in the background spotlight, with no fixed
// width on the counter (a width sized for "14m 07s" wastes 30px against "12s"
// on a 390 row).
const liveCss = readFileSync(new URL('./LiveBar.css', import.meta.url), 'utf8');

function rule(selectorPattern) {
  const m = liveCss.match(new RegExp(`${selectorPattern}[^{]*\\{([^}]*)\\}`));
  return m ? m[1] : '';
}

test('the counter is anchored to the end of the foreground sentence slot', () => {
  expect(rule('\\.zl-live-now > \\.zl-live-txt')).toMatch(/flex:\s*1 1 auto/);
  expect(rule('\\.zl-live-now > \\.zl-live-el')).toMatch(/margin-left:\s*auto/);
  // Anchored by flex, not by a width the counter has to fit.
  expect(rule('\\.zl-live-el(?![-\\w])')).not.toMatch(/(?:min-)?width:/);
});

// ── Stop lives here, once ──────────────────────────────────────────────────
// Stop moved from the composer to this row. Two guards on the source: the bar
// is the ONLY place that draws the run's Stop square, and the composer draws
// no square at all -- neither for Stop nor for a live mic, which is what put
// two identical red squares with opposite meanings side by side.
const composerJsx = readFileSync(new URL('../Composer/Composer.jsx', import.meta.url), 'utf8');
const liveJsx = readFileSync(new URL('./LiveBar.jsx', import.meta.url), 'utf8');

test('the composer draws no Square: Stop is the live bar\'s, and a live mic is a mic', () => {
  expect(composerJsx).not.toMatch(/\bSquare\b/);
  expect(composerJsx).not.toContain('composer-stop-ghost');
  expect(liveJsx).toMatch(/class=\{`zl-live-stop/);
  expect(liveJsx).toMatch(/<Square /);
});

test('the bar offers Stop only for the agent\'s own foreground sentence', () => {
  expect(canStopForeground({ state: 'running' }, { kind: 'foreground', waiting: false })).toBe(true);
  expect(canStopForeground({ state: 'idle' }, { kind: 'ended', waiting: false })).toBe(false);
  expect(liveJsx).toContain('!!onStop && canStopForeground(session, model.sentence)');
});

test('foregroundLine flags waitingOnPermission only for a permission prompt', () => {
  const permission = { state: 'permission', runStartedAtMs: 1000 };
  expect(foregroundLine(permission, 13000)).toMatchObject({ waiting: true, waitingOnPermission: true });

  const askUser = { state: 'permission', runStartedAtMs: 1000, pendingAsk: { id: 'q1' } };
  expect(foregroundLine(askUser, 13000)).toMatchObject({ waiting: true, waitingOnPermission: false });
});

test('Stop stays hidden for permission approvals and an already-ended ask, but is available for pending questions', () => {
  const permissionModel = liveBarModel({ state: 'permission', runStartedAtMs: 1000 }, [], 13000);
  expect(permissionModel.sentence.waiting).toBe(true);
  expect(permissionModel.sentence.waitingOnPermission).toBe(true);
  expect(canStopForeground({ state: 'permission' }, permissionModel.sentence)).toBe(false);

  const askUserModel = liveBarModel(
    { state: 'permission', runStartedAtMs: 1000, pendingAsk: { id: 'q1' } },
    [],
    13000,
  );
  expect(askUserModel.sentence.waiting).toBe(true);
  expect(askUserModel.sentence.waitingOnPermission).toBe(false);
  expect(canStopForeground({ state: 'permission', pendingAsk: { id: 'q1' } }, askUserModel.sentence)).toBe(true);

  // RunEnded can reach the client before AskUserResolved; an idle session's
  // stale pendingAsk must not resurrect Stop during that short gap.
  const staleAsk = liveBarModel({ state: 'idle', pendingAsk: { id: 'q1' } }, [], 13000);
  expect(staleAsk.sentence.waiting).toBe(true);
  expect(canStopForeground({ state: 'idle', pendingAsk: { id: 'q1' } }, staleAsk.sentence)).toBe(false);
});
