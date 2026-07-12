// format.test.js — run with `bun test`
import { test, expect } from 'bun:test';
import { formatDiff, toolPreview, sessionDotState } from './format.js';

test('formatDiff numbers lines from startLine', () => {
  const out = formatDiff('old line\nsecond', 'new line\nsecond', 260);
  const lines = out.split('\n');
  expect(lines[0]).toBe('260 - old line');
  expect(lines[1]).toBe('260 + new line');
  expect(lines[2]).toBe('261   second');
});

test('formatDiff multiline change keeps real numbering', () => {
  const out = formatDiff('ctx\na\nb\nctx2', 'ctx\nX\nctx2', 10);
  expect(out).toContain('10   ctx');
  expect(out).toContain('11 - a');
  expect(out).toContain('12 - b');
  expect(out).toContain('11 + X');
  expect(out).toContain('13   ctx2'); // trailing context: old numbering
});

test('formatDiff without startLine numbers from 1 (degraded)', () => {
  const out = formatDiff('a\nb', 'a\nB');
  expect(out).toContain('1   a');
  expect(out).toContain('2 - b');
  expect(out).toContain('2 + B');
});

test('formatDiff ignores invalid startLine values', () => {
  for (const bad of [0, -5, undefined, null, NaN, 'x']) {
    const out = formatDiff('a', 'b', bad);
    expect(out).toContain('1 - a');
    expect(out).toContain('1 + b');
  }
});

test('toolPreview edit fallback uses start_line', () => {
  const p = toolPreview('edit', { oldText: 'foo', newText: 'bar' }, null, 'running', 42);
  expect(p.kind).toBe('diff');
  expect(p.text).toContain('42 - foo');
  expect(p.text).toContain('42 + bar');
});

test('toolPreview edit prefers server diff over fallback', () => {
  const result = '@@ -257 +257 @@\n 257  ctx';
  const p = toolPreview('edit', { oldText: 'foo', newText: 'bar' }, result, 'done', 42);
  expect(p.text).toBe(result);
});

test('sessionDotState: idle main with live subagents shows running', () => {
  expect(sessionDotState({ state: 'idle', subagentCount: 2 })).toBe('running');
  expect(sessionDotState({ state: 'idle', subagents: { j1: { status: 'running' } } })).toBe('running');
});

test('sessionDotState: idle with no subagents stays idle', () => {
  expect(sessionDotState({ state: 'idle', subagentCount: 0 })).toBe('idle');
  expect(sessionDotState({ state: 'idle', subagents: { j1: { status: 'completed' } } })).toBe('idle');
  expect(sessionDotState({ state: 'idle' })).toBe('idle');
});

test('sessionDotState: a non-idle main state always wins', () => {
  expect(sessionDotState({ state: 'permission', subagentCount: 0 })).toBe('permission');
  expect(sessionDotState({ state: 'error' })).toBe('error');
  expect(sessionDotState({ state: 'running', subagentCount: 5 })).toBe('running');
});

test('sessionDotState: null-safe', () => {
  expect(sessionDotState(null)).toBe('idle');
});

test('sessionDotState: saved with subagent data stays saved', () => {
  expect(sessionDotState({ state: 'saved', subagentCount: 1 })).toBe('saved');
});
