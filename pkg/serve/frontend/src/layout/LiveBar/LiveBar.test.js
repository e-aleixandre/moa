import { test, expect } from 'bun:test';
import { readFileSync } from 'node:fs';
import { foregroundLine, liveBarModel } from './LiveBar.jsx';

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

test('the background takes the sentence only when the foreground is silent', () => {
  const model = liveBarModel(IDLE, AGENTS, 13000, 0);
  expect(model.sentence.kind).toBe('background');
  expect(model.sentence.agent.id).toBe('a1');
});

test('the spotlight moves the background sentence across the live work', () => {
  expect(liveBarModel(IDLE, AGENTS, 13000, 1).sentence.agent.id).toBe('b1');
  // Out-of-range indices clamp instead of blanking the sentence.
  expect(liveBarModel(IDLE, AGENTS, 13000, 9).sentence.agent.id).toBe('b1');
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

// A background sentence borrows the item's own age, not the run's: there is no
// foreground run to count from.
test('a background sentence carries the item elapsed', () => {
  expect(liveBarModel(IDLE, AGENTS, 13000, 0).sentence.elapsed).toBe('1m12s');
});

test('a missing or malformed agent list is simply no background', () => {
  expect(liveBarModel(WORKING, undefined, 13000).tally).toBe(null);
  expect(liveBarModel(IDLE, undefined, 13000)).toBe(null);
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
