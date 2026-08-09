import { expect, test } from 'bun:test';
import {
  __resetAttentionArrivalsForTests,
  attentionArrival,
  forgetAttentionArrival,
  retainAttentionArrivals,
} from './attention-arrivals.js';

test('attention arrivals are global across sessions and deduplicate old occurrences', () => {
  __resetAttentionArrivalsForTests();
  const first = attentionArrival('a', 1);
  const second = attentionArrival('b', 1);
  expect(second).toBeGreaterThan(first);
  expect(attentionArrival('a', 1)).toBe(first);
  expect(attentionArrival('a', 0)).toBe(0);
});

test('per-session high water never replays historical occurrences', () => {
  __resetAttentionArrivalsForTests();
  const newest = attentionArrival('a', 5000);
  expect(attentionArrival('a', 1)).toBe(newest);
  expect(attentionArrival('a', 5000)).toBe(newest);
  expect(attentionArrival('a', 5001)).toBeGreaterThan(newest);
  forgetAttentionArrival('a');
  expect(attentionArrival('a', 1)).toBeGreaterThan(newest);
});

test('a restarted namespace makes a lower sequence a fresh arrival', () => {
  __resetAttentionArrivalsForTests();
  const beforeRestart = attentionArrival('a', 5000, 'server-a');
  const afterRestart = attentionArrival('a', 1, 'server-b');
  expect(afterRestart).toBeGreaterThan(beforeRestart);
  expect(attentionArrival('a', 1, 'server-b')).toBe(afterRestart);
});

test('a plain reconnect in the same namespace does not replay arrivals', () => {
  __resetAttentionArrivalsForTests();
  const first = attentionArrival('a', 7, 'server-a');
  expect(attentionArrival('a', 7, 'server-a')).toBe(first);
  expect(attentionArrival('a', 6, 'server-a')).toBe(first);
});

test('retaining the session list prunes deleted session arrival state', () => {
  __resetAttentionArrivalsForTests();
  const first = attentionArrival('deleted', 99, 'server-a');
  retainAttentionArrivals(new Set());
  expect(attentionArrival('deleted', 1, 'server-a')).toBeGreaterThan(first);
});
