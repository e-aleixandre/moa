// rewind-model.test.js — run with `bun test`
import { test, expect } from 'bun:test';
import {
  normalizeBranchPoint, normalizeBranchPoints, rewindSummary,
  currentPathTipId, isJumpable, formatRelativeTime,
} from './rewind-model.js';

test('normalizeBranchPoint maps snake_case to camelCase and coerces seconds to ms', () => {
  const p = normalizeBranchPoint({
    entry_id: 'e1', label: 'hi', role: 'assistant',
    timestamp: 1700000000, branch_count: 2, is_current_path: true,
  });
  expect(p).toEqual({
    entryId: 'e1', label: 'hi', role: 'assistant',
    timestampMs: 1700000000000, branchCount: 2, isCurrentPath: true,
  });
});

test('normalizeBranchPoint defaults role to user and tolerates a missing timestamp', () => {
  const p = normalizeBranchPoint({ entry_id: 'e2', label: '' });
  expect(p.role).toBe('user');
  expect(p.timestampMs).toBe(0);
  expect(p.branchCount).toBe(0);
  expect(p.isCurrentPath).toBe(false);
});

test('normalizeBranchPoints tolerates a non-array (empty list)', () => {
  expect(normalizeBranchPoints(null)).toEqual([]);
  expect(normalizeBranchPoints(undefined)).toEqual([]);
  expect(normalizeBranchPoints([])).toEqual([]);
});

test('rewindSummary counts points and sums branch_count', () => {
  const points = [
    { branchCount: 0 }, { branchCount: 2 }, { branchCount: 1 },
  ];
  expect(rewindSummary(points)).toEqual({ pointCount: 3, branchCount: 3 });
});

test('rewindSummary on an empty list', () => {
  expect(rewindSummary([])).toEqual({ pointCount: 0, branchCount: 0 });
});

test('currentPathTipId picks the LAST current-path point (the tip)', () => {
  const points = [
    { entryId: 'a', isCurrentPath: true },
    { entryId: 'b', isCurrentPath: false },
    { entryId: 'c', isCurrentPath: true },
  ];
  expect(currentPathTipId(points)).toBe('c');
});

test('currentPathTipId returns null when nothing is on the current path', () => {
  expect(currentPathTipId([{ entryId: 'a', isCurrentPath: false }])).toBe(null);
});

test('isJumpable rejects the tip and accepts everything else', () => {
  const tip = 'c';
  expect(isJumpable({ entryId: 'c' }, tip)).toBe(false);
  expect(isJumpable({ entryId: 'a' }, tip)).toBe(true);
});

test('formatRelativeTime renders coarse buckets', () => {
  const now = Date.now();
  expect(formatRelativeTime(now - 5000, now)).toBe('just now');
  expect(formatRelativeTime(now - 5 * 60000, now)).toBe('5m ago');
  expect(formatRelativeTime(now - 2 * 3600000, now)).toBe('2h ago');
  expect(formatRelativeTime(now - 3 * 86400000, now)).toBe('3d ago');
});

test('formatRelativeTime tolerates an invalid timestamp', () => {
  expect(formatRelativeTime(0)).toBe('');
  expect(formatRelativeTime(NaN)).toBe('');
});
