import { expect, test } from 'bun:test';
import { parseHistoryResult, historyOut } from './history-result.js';

const search = [
  '2 matches for "puerto canario", oldest first.',
  '[#1 id=e3b48e8e001c8594 2026-10-13 08:07 user] Apunta esto: el puerto canario es 7413',
  '[#5 id=8949baa3b63bfe82 2026-10-13 08:09 ask_user answer, answered by the owner agent "Se]na"] No',
  'Showing 2 of 9. Continue with after=8949baa3b63bfe82. Use read or around with an id for the full text.',
  '',
].join('\n');

test('a search reads back as its summary, each message with author, date and text, and the continuation', () => {
  const parsed = parseHistoryResult('search', search);
  expect(parsed.summary).toBe('2 matches for "puerto canario", oldest first.');
  expect(parsed.items).toEqual([
    { focus: false, pos: 1, id: 'e3b48e8e001c8594', date: '2026-10-13 08:07', author: 'user', fromByte: 0, text: 'Apunta esto: el puerto canario es 7413' },
    { focus: false, pos: 5, id: '8949baa3b63bfe82', date: '2026-10-13 08:09', author: 'ask_user answer, answered by the owner agent "Se]na"', fromByte: 0, text: 'No' },
  ]);
  expect(parsed.notes).toEqual(['Showing 2 of 9. Continue with after=8949baa3b63bfe82. Use read or around with an id for the full text.']);
  expect(historyOut(parsed)).toBe('2 matches');
});

test('an around window keeps its order and marks the requested message', () => {
  const parsed = parseHistoryResult('around', '  [#1 id=a user] before\n» [#2 id=b assistant] the one\n  [#3 id=c tool read] after\n');
  expect(parsed.items.map((i) => [i.pos, i.focus, i.text])).toEqual([[1, false, 'before'], [2, true, 'the one'], [3, false, 'after']]);
  expect(historyOut(parsed)).toBe('3 messages');
});

test('a read keeps the whole multi-line text and its continuation note apart', () => {
  const parsed = parseHistoryResult('read', '[#4 id=d 2026-10-13 08:07 assistant] (from byte 12288)\nline one\n\nline two\n[12288 of 30000 bytes shown; continue with offset=24576]');
  expect(parsed.items[0]).toMatchObject({ pos: 4, fromByte: 12288, text: 'line one\n\nline two' });
  expect(parsed.notes).toEqual(['[12288 of 30000 bytes shown; continue with offset=24576]']);
});

test('zero matches is a summary with no messages; text it cannot read is null', () => {
  const parsed = parseHistoryResult('search', '0 matches for "x", oldest first. Zero matches does not prove absence: try other words.\n');
  expect(parsed.items).toEqual([]);
  expect(historyOut(parsed)).toBe('0 matches');
  expect(parseHistoryResult('read', 'no message with id "x" in this session')).toBeNull();
});
