import { test, expect } from 'bun:test';
import { nowLineModel } from './NowLine.jsx';

// The now-line is the desktop/mobile activity line above the composer. These
// cover the DECISIONS (what it says, when it shows a timer, when it goes
// waiting); the rendering is a class prefix away and carries no logic.

test('an idle session has no now-line at all', () => {
  expect(nowLineModel({ state: 'idle' }, 1000)).toBe(null);
  expect(nowLineModel(null, 1000)).toBe(null);
});

test('a working session reads its in-flight action with a live elapsed timer', () => {
  const session = {
    state: 'running',
    runStartedAtMs: 1000,
    messages: [{ _type: 'tool_start', tool_name: 'grep', args: {}, status: 'running' }],
  };
  expect(nowLineModel(session, 13000)).toEqual({
    text: 'Searching the code…',
    waiting: false,
    elapsed: '12s',
  });
});

test('waiting on the user drops the timer and flags the amber state', () => {
  const session = { state: 'permission', runStartedAtMs: 1000 };
  expect(nowLineModel(session, 13000)).toEqual({
    text: 'Waiting for you',
    waiting: true,
    elapsed: '',
  });
});

// Without the server-stamped origin there is nothing honest to count from, so
// the phrase stands alone rather than counting from the client's clock.
test('a run with no server-stamped start shows the phrase without a timer', () => {
  const session = { state: 'running', thinkingText: 'hmm' };
  expect(nowLineModel(session, 13000)).toEqual({
    text: 'Thinking…',
    waiting: false,
    elapsed: '',
  });
});

// Compacting/verifying are real work, but momentary and not anchored to the
// run's start, so they read as in-progress copy without an age counter.
test('compacting shows in-progress copy without a timer', () => {
  const session = { state: 'running', compacting: true, runStartedAtMs: 1000 };
  expect(nowLineModel(session, 13000)).toEqual({
    text: 'Compacting context…',
    waiting: false,
    elapsed: '',
  });
});
