import { test, expect } from 'bun:test';
import { opsProjectLabel, sessionStatusLabel } from './ops-data.js';

test('ops project labels retain only a safe concise path segment', () => {
  expect(opsProjectLabel('/home/moa/project')).toBe('project');
  expect(opsProjectLabel('')).toBe('Project');
});

test('ops session label uses only status fields', () => {
  expect(sessionStatusLabel({ activity: 'running', jobs: { bash: 1, subagents: 1 }, verification: 'passed' }))
    .toBe('running · 2 jobs · verify passed');
});
