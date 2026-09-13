// artifact-groups.test.js — run with `bun test`

import { test, expect } from 'bun:test';
import { artifactDayLabel, groupArtifactsByDay } from './artifact-groups.js';

const NOW = new Date(2026, 8, 13, 20, 0, 0).getTime();
const at = (y, m, d, h = 12) => new Date(y, m, d, h).toISOString();

test('labels: today, yesterday, then a short date', () => {
  expect(artifactDayLabel(at(2026, 8, 13, 1), NOW)).toBe('Today');
  expect(artifactDayLabel(at(2026, 8, 12, 23), NOW)).toBe('Yesterday');
  expect(artifactDayLabel(at(2026, 8, 9), NOW)).toMatch(/9/);
  expect(artifactDayLabel(at(2025, 8, 9), NOW)).toMatch(/2025/);
  expect(artifactDayLabel('', NOW)).toBe('Earlier');
  expect(artifactDayLabel('nope', NOW)).toBe('Earlier');
});

test('groups consecutive entries by day and keeps server order', () => {
  const items = [
    { id: 'a', updatedAt: at(2026, 8, 13, 18) },
    { id: 'b', updatedAt: at(2026, 8, 13, 9) },
    { id: 'c', updatedAt: at(2026, 8, 12) },
    { id: 'd', createdAt: at(2026, 8, 9) },
  ];
  const groups = groupArtifactsByDay(items, NOW);
  expect(groups.map((g) => g.label)).toEqual(['Today', 'Yesterday', artifactDayLabel(at(2026, 8, 9), NOW)]);
  expect(groups.map((g) => g.items.map((i) => i.id))).toEqual([['a', 'b'], ['c'], ['d']]);
});
