import { test, expect } from 'bun:test';
import { clockMs, clockHHMM, clockDayLabel } from './clock.js';

// The two real transports, measured on a live session rather than assumed:
// the WebSocket sends core.Message with `timestamp` in epoch SECONDS, and the
// REST conversation DTO sends an RFC3339 string.

test('epoch seconds — the shape the WebSocket actually sends', () => {
  expect(clockMs(1789423841)).toBe(1789423841000);
});

test('epoch milliseconds are taken as they are, not multiplied again', () => {
  expect(clockMs(1789423841000)).toBe(1789423841000);
});

test('an RFC3339 string — the REST conversation DTO', () => {
  expect(clockMs('2026-09-15T08:10:41Z')).toBe(Date.parse('2026-09-15T08:10:41Z'));
});

test('a numeric string is the number it spells', () => {
  expect(clockMs('1789423841')).toBe(1789423841000);
});

test('no time is no time: nothing is invented', () => {
  for (const absent of [undefined, null, '', 0, -1, NaN, Infinity, 'not a date', {}, []]) {
    expect(clockMs(absent)).toBeNull();
    expect(clockHHMM(absent)).toBe('');
    expect(clockDayLabel(absent)).toBe('');
  }
});

test("Go's zero time.Time reads as absent, not as the year 1", () => {
  expect(clockMs('0001-01-01T00:00:00Z')).toBeNull();
  expect(clockHHMM('0001-01-01T00:00:00Z')).toBe('');
});

test('the hour is the 2-digit wall clock of the local day', () => {
  const at = new Date(2026, 8, 15, 9, 14, 0);
  expect(clockHHMM(Math.floor(at.getTime() / 1000)))
    .toBe(at.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }));
});

// The day label exists so the gutter column never widens: it rides ABOVE the
// hour, and only when the message is not from today.

test('today carries no day label — a transcript is read in the present', () => {
  const now = new Date(2026, 8, 15, 12, 0, 0).getTime();
  const earlierToday = new Date(2026, 8, 15, 9, 14, 0).getTime();
  expect(clockDayLabel(earlierToday, now)).toBe('');
});

test('yesterday is named', () => {
  const now = new Date(2026, 8, 15, 12, 0, 0).getTime();
  const yesterday = new Date(2026, 8, 14, 23, 52, 0).getTime();
  expect(clockDayLabel(yesterday, now)).toBe('yesterday');
});

test('older than yesterday falls back to a short date', () => {
  const now = new Date(2026, 8, 15, 12, 0, 0).getTime();
  const older = new Date(2026, 8, 2, 10, 0, 0).getTime();
  const label = clockDayLabel(older, now);
  expect(label).not.toBe('');
  expect(label).not.toBe('yesterday');
});

test('a future timestamp is not labelled yesterday by an unsigned subtraction', () => {
  const now = new Date(2026, 8, 15, 12, 0, 0).getTime();
  const tomorrow = new Date(2026, 8, 16, 9, 0, 0).getTime();
  expect(clockDayLabel(tomorrow, now)).toBe('');
});
