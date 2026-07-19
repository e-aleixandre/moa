// format.test.js — run with `bun test`
import { test, expect } from 'bun:test';
import { formatDiff, toolPreview, sessionDotState, isRecentSession, RECENT_DAYS, mobileModelLabel, fmtTokens } from './format.js';

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

test('isRecentSession: within the window is recent', () => {
  const twoDaysAgo = Date.now() - 2 * 24 * 60 * 60 * 1000;
  expect(isRecentSession({ updated: twoDaysAgo })).toBe(true);
});

test('isRecentSession: older than the window is not recent', () => {
  const old = Date.now() - (RECENT_DAYS + 1) * 24 * 60 * 60 * 1000;
  expect(isRecentSession({ updated: old })).toBe(false);
});

test('isRecentSession: no timestamp counts as recent (never silently hidden)', () => {
  expect(isRecentSession({})).toBe(true);
  expect(isRecentSession(null)).toBe(true);
});

test('mobileModelLabel: a known alias wins', () => {
  expect(mobileModelLabel('anthropic/sol-2')).toBe('sol');
  expect(mobileModelLabel('Fable Design')).toBe('fable');
});

test('mobileModelLabel: a curated display name (has spaces) is kept as-is', () => {
  expect(mobileModelLabel('Claude Opus 4.8')).toBe('Claude Opus 4.8');
});

test('mobileModelLabel: a technical id drops the vendor prefix and truncates', () => {
  expect(mobileModelLabel('anthropic/claude-opus-4-8')).toBe('opus-4-8');
  expect(mobileModelLabel('openai/gpt-5-turbo-preview')).toBe('5-turbo-pre…');
});

test('mobileModelLabel: empty/nullish is the empty string', () => {
  expect(mobileModelLabel('')).toBe('');
  expect(mobileModelLabel(null)).toBe('');
  expect(mobileModelLabel(undefined)).toBe('');
});

test('fmtTokens: below 1000 is a plain rounded integer', () => {
  expect(fmtTokens(0)).toBe('0');
  expect(fmtTokens(940)).toBe('940');
  expect(fmtTokens(999)).toBe('999');
});

test('fmtTokens: below 10k keeps one decimal', () => {
  expect(fmtTokens(1000)).toBe('1k');
  expect(fmtTokens(8700)).toBe('8.7k');
  expect(fmtTokens(1250)).toBe('1.3k');
  expect(fmtTokens(9940)).toBe('9.9k');
});

test('fmtTokens: 10k and above rounds to whole k (no 10.0k)', () => {
  expect(fmtTokens(9950)).toBe('10k');
  expect(fmtTokens(41200)).toBe('41k');
  expect(fmtTokens(999000)).toBe('999k');
});

test('fmtTokens: invalid/negative is zero', () => {
  expect(fmtTokens(-5)).toBe('0');
  expect(fmtTokens(NaN)).toBe('0');
  expect(fmtTokens(undefined)).toBe('0');
});
