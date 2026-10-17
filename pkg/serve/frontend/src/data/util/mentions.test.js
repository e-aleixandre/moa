import { expect, test } from 'bun:test';
import {
  MENTIONS_MARKER, backspaceMention, cleanLabel, liveMentions, mentionRanges, mentionsBlock,
  parseMentions, plainMessageText, rehydrateMentions, serializeMentions, sessionMentionIds,
  uniqueLabel, withLiveMeta,
} from './mentions.js';

const infra = { label: 'Infra', type: 'owner', id: 'f98930ec401750fb', meta: { root: '/home/e/dev/infra' } };
const fix = { label: 'Fix login cookie', type: 'session', id: '3f61a326c0e1aa', meta: { title: 'Fix login cookie', state: 'saved' } };

test('no mention still written, no block: the text goes out exactly as typed', () => {
  expect(serializeMentions('hola', [])).toBe('hola');
  expect(serializeMentions('hola  \n', [])).toBe('hola  \n');
  expect(serializeMentions('ask Infra', [infra])).toBe('ask Infra');
  expect(mentionsBlock('x', [infra])).toBe('');
});

test('the block names type, id, project and the tool, per kind of sender', () => {
  const text = 'ask @Infra; also @Fix login cookie, it stalled';
  expect(serializeMentions(text, [infra, fix])).toBe([
    text,
    '',
    MENTIONS_MARKER,
    "@Infra = owner/f98930ec401750fb · project infra (/home/e/dev/infra) · only the project's owner can send it tasks",
    '@Fix login cookie = session/3f61a326c0e1aa · "Fix login cookie" · saved · read it with read_session session_id=3f61a326c0e1aa',
  ].join('\n'));
  const asOwner = serializeMentions(text, [infra, fix], { ownerConversation: true });
  expect(asOwner).toContain('send it tasks with tasks send to=f98930ec401750fb');
  expect(asOwner).toContain('read it with sessions action=read session_id=3f61a326c0e1aa');
});

test('only the mentions still written are listed, once, in text order', () => {
  const block = serializeMentions('@Fix login cookie then @Infra and @Infra', [infra, fix]);
  const lines = block.split('\n').slice(-2);
  expect(lines[0]).toStartWith('@Fix login cookie = session/');
  expect(lines[1]).toStartWith('@Infra = owner/');
  expect(liveMentions('@Infra @Infra', [infra])).toHaveLength(1);
});

test('a hand-edited label stops matching', () => {
  expect(liveMentions('ask @Infrax', [infra])).toEqual([]);
  expect(liveMentions('ask @Infr', [infra])).toEqual([]);
  expect(liveMentions('ask @Infra.', [infra])).toEqual([infra]);
  expect(liveMentions('mail me@Infra', [infra])).toEqual([]);
});

test('a mention inside code is code, not a mention', () => {
  expect(liveMentions('run `@Infra` now', [infra])).toEqual([]);
  expect(liveMentions('```\n@Infra\n```', [infra])).toEqual([]);
  expect(liveMentions('`x` and @Infra', [infra])).toEqual([infra]);
});

test('a longer label wins over its own prefix', () => {
  const other = { label: 'Infra 2', type: 'owner', id: 'o2', meta: {} };
  const ranges = mentionRanges('@Infra 2 and @Infra', [infra, other]);
  expect(ranges.map((r) => [r.start, r.end, r.mention.id])).toEqual([[0, 8, 'o2'], [13, 19, 'f98930ec401750fb']]);
});

test('labels: one line, cut at 60, and #shortid when two targets share a name', () => {
  expect(cleanLabel('  a\n b\t c ')).toBe('a b c');
  expect(cleanLabel('x'.repeat(100))).toHaveLength(60);
  expect(uniqueLabel('Infra', 'a1', [])).toBe('Infra');
  expect(uniqueLabel('Infra', 'a1', [{ label: 'Infra', id: 'a1' }])).toBe('Infra');
  expect(uniqueLabel('Infra', 'b2c3d4e5', [{ label: 'Infra', id: 'a1' }])).toBe('Infra #b2c3d4');
  expect(uniqueLabel('x'.repeat(100), 'b2c3d4e5', [{ label: 'x'.repeat(60), id: 'a1' }])).toHaveLength(60);
});

test('session ids for the request come from live session mentions only', () => {
  expect(sessionMentionIds('@Infra @Fix login cookie', [infra, fix])).toEqual([fix.id]);
  expect(sessionMentionIds('@Fix login cookiex', [fix])).toEqual([]);
  expect(sessionMentionIds('none', [fix])).toEqual([]);
});

test('the block reads the roster of the moment, not what the menu showed', () => {
  const [o, s] = withLiveMeta(
    [{ ...infra, meta: {} }, { ...fix, meta: { title: 'old', state: 'idle' } }],
    { owners: [{ id: infra.id, root: '/new/root' }], sessions: { [fix.id]: { title: 'Fix login (renamed)', state: 'running' } } },
  );
  expect(o.meta.root).toBe('/new/root');
  expect(s.meta).toEqual({ title: 'Fix login (renamed)', state: 'running' });
  const text = serializeMentions('@Fix login cookie', [s]);
  expect(text).toContain('"Fix login (renamed)" · running ·');
});

test('parse round-trips what serialize wrote', () => {
  const sent = serializeMentions('ask @Infra; read @Fix login cookie', [infra, fix]);
  const parsed = parseMentions(sent);
  expect(parsed.comment).toBe('ask @Infra; read @Fix login cookie');
  expect(parsed.mentions).toEqual([
    { label: 'Infra', type: 'owner', id: infra.id },
    { label: 'Fix login cookie', type: 'session', id: fix.id },
  ]);
  expect(plainMessageText(sent)).toBe('ask @Infra; read @Fix login cookie');
});

test('legacy and unrelated messages parse to null', () => {
  expect(parseMentions('just text')).toBeNull();
  expect(parseMentions(`typed ${MENTIONS_MARKER} inline`)).toBeNull();
  expect(parseMentions(`${MENTIONS_MARKER}\nnot a mention line`)).toBeNull();
  expect(parseMentions(`hi\n\n${MENTIONS_MARKER}\n`)).toBeNull();
  expect(parseMentions(`${MENTIONS_MARKER}\n@x = owner/a\n\nlater text`)).toBeNull();
  expect(plainMessageText('just text')).toBe('just text');
  expect(plainMessageText(undefined)).toBeUndefined();
});

test('a marker in the middle of a message does not hide what follows it', () => {
  const text = `see ${MENTIONS_MARKER}\nmore\n\nreal text`;
  expect(parseMentions(text)).toBeNull();
});

test('rewind: the comment comes back with owners, sessions only when the server vouches', () => {
  const sent = serializeMentions('ask @Infra; read @Fix login cookie', [infra, fix]);
  const trusted = rehydrateMentions(sent, [fix.id]);
  expect(trusted.comment).toBe('ask @Infra; read @Fix login cookie');
  expect(trusted.mentions.map((m) => m.id)).toEqual([infra.id, fix.id]);
  const untrusted = rehydrateMentions(sent, []);
  expect(untrusted.mentions.map((m) => m.id)).toEqual([infra.id]);
  expect(untrusted.comment).not.toContain(MENTIONS_MARKER);
  // The text alone never vouches for a session id the server did not list.
  expect(rehydrateMentions(sent, ['someone-else']).mentions.map((m) => m.id)).toEqual([infra.id]);
  expect(rehydrateMentions('plain', [fix.id])).toEqual({ comment: 'plain', mentions: [] });
});

test('a restored mention is serialized again into the same block', () => {
  const sent = serializeMentions('read @Fix login cookie', [fix]);
  const { comment, mentions } = rehydrateMentions(sent, [fix.id]);
  const again = serializeMentions(comment, withLiveMeta(mentions, { sessions: { [fix.id]: { title: 'Fix login cookie', state: 'saved' } } }));
  expect(again).toBe(sent);
});

test('Backspace at the end of a complete mention removes it whole', () => {
  expect(backspaceMention('ask @Infra', 10, [infra])).toEqual({ value: 'ask ', cursor: 4 });
  expect(backspaceMention('ask @Infra now', 10, [infra])).toEqual({ value: 'ask  now', cursor: 4 });
  expect(backspaceMention('ask @Infra', 9, [infra])).toBeNull();
  expect(backspaceMention('ask @Infra ', 11, [infra])).toBeNull();
  expect(backspaceMention('ask @Infrax', 11, [infra])).toBeNull();
  expect(backspaceMention('ask @Infra', 10, [])).toBeNull();
});
