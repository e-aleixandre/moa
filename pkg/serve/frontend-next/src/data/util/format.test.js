// format.test.js — run with `bun test`
import { test, expect } from 'bun:test';
import { formatDiff, toolPreview, sessionDotState, isRecentSession, RECENT_DAYS, mobileModelLabel, modelCodename, fmtTokens, contextWindowLabel, sessionTitle } from './format.js';

test('sessionTitle keeps a title and falls back to Untitled', () => {
  expect(sessionTitle({ title: 'Build dashboard' })).toBe('Build dashboard');
  expect(sessionTitle({ title: '' })).toBe('Untitled');
  expect(sessionTitle({ title: '   ' })).toBe('Untitled');
  expect(sessionTitle({})).toBe('Untitled');
  expect(sessionTitle()).toBe('Untitled');
});

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

test('toolPreview bounds live server diffs before checking for hunks', () => {
  const result = [
    '@@ -1,3000 +1,3000 @@',
    ...Array.from({ length: 3000 }, (_, i) => `+line ${i + 1}`),
  ].join('\n');
  const p = toolPreview('edit', {}, result, 'running');

  expect(p.kind).toBe('diff');
  expect(p.text.length).toBeLessThanOrEqual(20000 + '@@ -1 +1 @@\n'.length);
  expect(p.text).toContain('+line 3000');
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

test('modelCodename: capitalizes a known codename anywhere in the string', () => {
  expect(modelCodename('Claude Opus 4.8')).toBe('Opus');
  expect(modelCodename('Claude Fable 5')).toBe('Fable');
  expect(modelCodename('Claude Sonnet 5')).toBe('Sonnet');
  expect(modelCodename('GPT-5.6 Sol')).toBe('Sol');
  expect(modelCodename('gpt-5.6-terra')).toBe('Terra');
  expect(modelCodename('anthropic/claude-opus-4-8')).toBe('Opus');
});

test('modelCodename: empty when no known codename / nullish', () => {
  expect(modelCodename('gpt-5-turbo-preview')).toBe('');
  expect(modelCodename('')).toBe('');
  expect(modelCodename(null)).toBe('');
});

test('mobileModelLabel: a known codename wins, capitalized', () => {
  expect(mobileModelLabel('anthropic/sol-2')).toBe('Sol');
  expect(mobileModelLabel('Fable Design')).toBe('Fable');
  expect(mobileModelLabel('Claude Opus 4.8')).toBe('Opus');
});

test('mobileModelLabel: a curated display name (no codename) is kept as-is', () => {
  expect(mobileModelLabel('GPT-5.3 Codex')).toBe('GPT-5.3 Codex');
});

test('mobileModelLabel: a technical id (no codename) drops the vendor prefix and truncates', () => {
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

test('contextWindowLabel: rounds near-whole millions to "NM ctx"', () => {
  expect(contextWindowLabel(1_000_000)).toBe('1M ctx');
  expect(contextWindowLabel(1_050_000)).toBe('1M ctx');
});

test('contextWindowLabel: sub-million uses "NK ctx"', () => {
  expect(contextWindowLabel(400_000)).toBe('400K ctx');
  expect(contextWindowLabel(200_000)).toBe('200K ctx');
});

test('contextWindowLabel: missing/invalid is the empty string', () => {
  expect(contextWindowLabel(0)).toBe('');
  expect(contextWindowLabel(undefined)).toBe('');
  expect(contextWindowLabel(-1)).toBe('');
  expect(contextWindowLabel(NaN)).toBe('');
});
