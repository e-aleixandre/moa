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
    entryId: 'e1', label: 'hi', role: 'assistant', source: '', editable: false,
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

test('isJumpable rejects an assistant tip and accepts everything else', () => {
  const tip = 'c';
  expect(isJumpable({ entryId: 'c', role: 'assistant', editable: false }, tip)).toBe(false);
  expect(isJumpable({ entryId: 'a', role: 'assistant', editable: false }, tip)).toBe(true);
});

test('isJumpable accepts a user message even as the tip: rewinding goes before it', () => {
  expect(isJumpable({ entryId: 'c', role: 'user', editable: true }, 'c')).toBe(true);
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

test('a point carries its source and only the user\'s own messages are editable', () => {
  expect(normalizeBranchPoint({ entry_id: 'a', role: 'user' }).editable).toBe(true);
  const owner = normalizeBranchPoint({ entry_id: 'b', role: 'user', source: 'owner' });
  expect(owner.source).toBe('owner');
  expect(owner.editable).toBe(false);
  expect(normalizeBranchPoint({ entry_id: 'c', role: 'user', source: 'subagent_parent' }).editable).toBe(false);
  expect(normalizeBranchPoint({ entry_id: 'd', role: 'assistant' }).editable).toBe(false);
});

test('an injected user point as the tip is not jumpable: there is nothing to edit', () => {
  expect(isJumpable(normalizeBranchPoint({ entry_id: 'c', role: 'user', source: 'owner' }), 'c')).toBe(false);
});
