// composer-suggest.test.js — run with `bun test`
import { test, expect } from 'bun:test';
import {
  COMMANDS, filterCommands, filterGoalFlags, tokenAtCursor, slashSuggestions,
  findMentionToken, computeMentionInsertion, normalizeDashes,
} from './composer-suggest.js';

test('filterCommands with empty filter returns all commands', () => {
  expect(filterCommands('')).toEqual(COMMANDS);
});

test('filterCommands matches by prefix, case-insensitive', () => {
  const names = filterCommands('mo').map((c) => c.name);
  expect(names).toEqual(['model']);
  expect(filterCommands('MOD').map((c) => c.name)).toEqual(['model']);
});

test('filterCommands with no match returns empty array', () => {
  expect(filterCommands('zzz')).toEqual([]);
});

test('tokenAtCursor returns the run of non-space chars before the cursor', () => {
  expect(tokenAtCursor('/goal foo --max', 15)).toBe('--max');
  expect(tokenAtCursor('/goal foo --max', 9)).toBe('foo');
  expect(tokenAtCursor('/goal ', 6)).toBe('');
});

test('filterGoalFlags only matches tokens starting with -', () => {
  const flags = [{ name: '--max', desc: 'Max iterations', placeholder: '<n>' }, { name: '--verbose', desc: 'Verbose' }];
  expect(filterGoalFlags(flags, 'max', '')).toEqual([]);
  expect(filterGoalFlags(flags, '--m', '')).toEqual([{ name: '--max', desc: 'Max iterations', args: '<n>', __flag: true }]);
});

test('filterGoalFlags with a bare dash suggests every flag', () => {
  const flags = [{ name: '--max', desc: 'Max' }, { name: '--verbose', desc: 'V' }];
  expect(filterGoalFlags(flags, '-', '').length).toBe(2);
  expect(filterGoalFlags(flags, '--', '').length).toBe(2);
});

test('filterGoalFlags excludes flags already present in the text', () => {
  const flags = [{ name: '--max', desc: 'Max' }, { name: '--verbose', desc: 'V' }];
  const names = filterGoalFlags(flags, '--', '/goal do it --max 3 --v').map((f) => f.name);
  expect(names).toEqual(['--verbose']);
});

test('slashSuggestions returns null for non-slash or multiline text', () => {
  expect(slashSuggestions('hello', 5, [])).toBeNull();
  expect(slashSuggestions('/clear\nmore', 6, [])).toBeNull();
});

test('slashSuggestions filters command names before the first space', () => {
  const s = slashSuggestions('/comp', 5, []);
  expect(s.map((c) => c.name)).toEqual(['compact']);
});

test('slashSuggestions hides once a non-goal command has arguments', () => {
  expect(slashSuggestions('/model gpt-5 ', 13, [])).toBeNull();
});

test('slashSuggestions offers /goal flags once the token starts with -', () => {
  const flags = [{ name: '--max', desc: 'Max', placeholder: '<n>' }];
  const s = slashSuggestions('/goal do the thing --', 21, flags);
  expect(s).toEqual([{ name: '--max', desc: 'Max', args: '<n>', __flag: true }]);
});

test('slashSuggestions returns null for /goal args with no dash token', () => {
  expect(slashSuggestions('/goal do the thing', 18, [{ name: '--max', desc: 'Max' }])).toBeNull();
});

test('findMentionToken finds an @ preceded by whitespace or start-of-text', () => {
  expect(findMentionToken('see @src/fo', 11)).toEqual({ atIdx: 4, filter: 'src/fo' });
  expect(findMentionToken('@fo', 3)).toEqual({ atIdx: 0, filter: 'fo' });
});

test('findMentionToken returns null when @ is mid-word or filter has whitespace', () => {
  expect(findMentionToken('foo@bar', 7)).toBeNull();
  expect(findMentionToken('see @src fo', 11)).toBeNull();
});

test('findMentionToken returns null with no @ in range', () => {
  expect(findMentionToken('hello world', 11)).toBeNull();
});

test('computeMentionInsertion for a file drops the @ and appends a space', () => {
  const r = computeMentionInsertion('see @src/fo', 11, 'src/foo.js', false);
  expect(r).toEqual({ value: 'see src/foo.js ', cursor: 15, retrigger: false });
});

test('computeMentionInsertion for a directory keeps the @ and adds a trailing slash', () => {
  const r = computeMentionInsertion('see @src', 8, 'src', true);
  expect(r).toEqual({ value: 'see @src/', cursor: 9, retrigger: true });
});

test('normalizeDashes rewrites a leading em/en-dash token into --', () => {
  expect(normalizeDashes('/goal do it \u2014max 3')).toBe('/goal do it --max 3');
  expect(normalizeDashes('/goal do it \u2013max 3')).toBe('/goal do it --max 3');
});

test('normalizeDashes leaves prose em-dashes untouched', () => {
  expect(normalizeDashes('word\u2014word')).toBe('word\u2014word');
});
