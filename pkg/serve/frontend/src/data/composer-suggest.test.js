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

test('filterCommands exposes handoff', () => {
  expect(filterCommands('han')).toEqual([{
    name: 'handoff',
    desc: 'Start a fresh session with a generated handoff',
    args: '[--model <spec>] [--thinking <level>]',
  }]);
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

test('slashSuggestions treats mobile em/en-dashes as goal flag prefixes', () => {
  const flags = [{ name: '--max', desc: 'Max', placeholder: '<n>' }];
  expect(slashSuggestions('/goal do it —m', 14, flags)).toEqual([
    { name: '--max', desc: 'Max', args: '<n>', __flag: true },
  ]);
  expect(slashSuggestions('/goal do it –m', 14, flags)).toEqual([
    { name: '--max', desc: 'Max', args: '<n>', __flag: true },
  ]);
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

// --- skills in the slash menu ---

test('slash menu lists a session skill after the built-ins', () => {
  const skills = [{ name: 'deploy', skill: 'deploy', description: 'Ship it' }];
  const out = slashSuggestions('/dep', 4, [], skills);
  expect(out.length).toBe(1);
  expect(out[0].name).toBe('deploy');
  expect(out[0].__skill).toBe(true);
});

// A skill must never displace a command: the server resolves skills only after
// the registry, and the menu has to show the same precedence.
test('a colliding skill appears under skill: while the command keeps its name', () => {
  const skills = [{ name: 'skill:compact', skill: 'compact', description: 'A skill' }];
  const names = slashSuggestions('/c', 2, [], skills).map((c) => c.name);
  expect(names).toContain('compact');
  const all = slashSuggestions('/', 1, [], skills).map((c) => c.name);
  expect(all).toContain('skill:compact');
  // The built-in comes first: same precedence the server applies.
  expect(all.indexOf('compact')).toBeLessThan(all.indexOf('skill:compact'));
});

test('slash menu still works with no skills', () => {
  expect(slashSuggestions('/comp', 5, [], []).length).toBe(1);
  expect(slashSuggestions('/comp', 5, []).length).toBe(1);
});

// --- @ menu: owners, sessions, files ------------------------------------------

const {
  ownerSuggestions, sessionSuggestions, flattenMentionGroups, computeLabelInsertion, pickMention,
} = await import('./composer-suggest.js');

const roster = [
  { id: 'o1', name: 'Moa', root: '/dev/moa', session_id: 's-moa', session_state: 'idle' },
  { id: 'o2', name: 'Infra', root: '/dev/infra', session_id: 's-infra', session_state: 'saved' },
  { id: 'o3', name: 'iRead', root: '/dev/iread-app', session_id: 's-iread' },
  { id: 'o4', name: 'Winerim', root: '/dev/winerim' },
  { id: 'o5', name: 'Winerim Web', root: '/dev/winerim-web' },
  { id: 'o6', name: 'Inbox', root: '/dev/inbox' },
];

test('owners: fuzzy by name and by root, name matches first, at most four', () => {
  expect(ownerSuggestions(roster, '').map((o) => o.label)).toEqual(['Moa', 'Infra', 'iRead', 'Winerim']);
  expect(ownerSuggestions(roster, 'in').map((o) => o.label)).toEqual(['Infra', 'Inbox', 'Winerim', 'Winerim Web']);
  expect(ownerSuggestions(roster, 'iread-app').map((o) => o.label)).toEqual(['iRead']);
  expect(ownerSuggestions(roster, 'zzz')).toEqual([]);
});

test('owners: shows project and saved, hides the owner whose conversation this is', () => {
  const [infra] = ownerSuggestions(roster, 'inf');
  expect(infra).toMatchObject({ kind: 'owner', id: 'o2', project: 'infra', saved: true });
  expect(ownerSuggestions(roster, 'inf', { excludeSessionId: 's-infra' })).toEqual([]);
});

test('owners: a folder path is files only', () => {
  expect(ownerSuggestions(roster, 'src/')).toEqual([]);
});

test('sessions: shaped from the endpoint, saved wins as the state, max five', () => {
  const rows = Array.from({ length: 7 }, (_, i) => ({ id: `s${i}`, title: `T${i}`, state: 'idle', updated: 'x', saved: i === 1 }));
  const out = sessionSuggestions(rows);
  expect(out).toHaveLength(5);
  expect(out[1]).toMatchObject({ kind: 'session', id: 's1', label: 'T1', state: 'saved', saved: true });
  expect(sessionSuggestions({ error: 'x' })).toEqual([]);
});

test('groups lay out Owners, Sessions, Files; an empty group has no header', () => {
  const flat = flattenMentionGroups({
    files: [{ path: 'a.go', is_dir: false }, { path: 'src', is_dir: true }],
    owners: [{ kind: 'owner', id: 'o2' }],
    sessions: [],
  });
  expect(flat.map((i) => [i.group, i.kind, !!i.first])).toEqual([
    ['Owners', 'owner', true], ['Files', 'file', true], ['Files', 'file', false],
  ]);
  expect(flattenMentionGroups(null)).toEqual([]);
  expect(flattenMentionGroups({ owners: [], sessions: [], files: [] })).toEqual([]);
});

test('accepting an owner or session keeps the @ and adds label plus a space', () => {
  expect(computeLabelInsertion('ask @in', 7, 'Infra')).toEqual({ value: 'ask @Infra ', cursor: 11 });
  expect(computeLabelInsertion('@in and more', 3, 'Infra')).toEqual({ value: '@Infra  and more', cursor: 7 });
});

test('pickMention records what the block needs and dedupes the label', () => {
  const owner = ownerSuggestions(roster, 'inf')[0];
  expect(pickMention(owner)).toEqual({ label: 'Infra', type: 'owner', id: 'o2', meta: { root: '/dev/infra' } });
  const session = sessionSuggestions([{ id: 'abcdef123', title: 'Infra', state: 'idle' }])[0];
  expect(pickMention(session, [pickMention(owner)])).toEqual({
    label: 'Infra #abcdef', type: 'session', id: 'abcdef123', meta: { title: 'Infra', state: 'idle' },
  });
});
