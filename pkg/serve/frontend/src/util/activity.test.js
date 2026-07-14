import { test, expect } from 'bun:test';
import {
  gerundFor,
  formatElapsed,
  activityPhase,
  activityLabel,
  WORKING_GERUNDS,
} from './activity.js';

test('gerundFor rotates by 4s buckets and wraps', () => {
  expect(gerundFor(0)).toBe(WORKING_GERUNDS[0]);
  expect(gerundFor(1000)).toBe(WORKING_GERUNDS[0]);
  expect(gerundFor(3999)).toBe(WORKING_GERUNDS[0]);
  expect(gerundFor(4000)).toBe(WORKING_GERUNDS[1]);
  expect(gerundFor(8000)).toBe(WORKING_GERUNDS[2]);
  // wraps around after the full list
  expect(gerundFor(WORKING_GERUNDS.length * 4000)).toBe(WORKING_GERUNDS[0]);
});

test('gerundFor is stable for missing/negative elapsed', () => {
  expect(gerundFor(undefined)).toBe(WORKING_GERUNDS[0]);
  expect(gerundFor(-500)).toBe(WORKING_GERUNDS[0]);
});

test('formatElapsed renders compact durations', () => {
  expect(formatElapsed(0)).toBe('0s');
  expect(formatElapsed(8000)).toBe('8s');
  expect(formatElapsed(59000)).toBe('59s');
  expect(formatElapsed(60000)).toBe('1m');
  expect(formatElapsed(63000)).toBe('1m03s');
  expect(formatElapsed(134000)).toBe('2m14s');
  expect(formatElapsed(3600000)).toBe('1h');
  expect(formatElapsed(3660000)).toBe('1h01m');
  expect(formatElapsed(-1)).toBe('');
});

test('activityPhase classifies coarse phases', () => {
  expect(activityPhase(null)).toBe(null);
  expect(activityPhase({ state: 'idle' })).toBe(null);
  expect(activityPhase({ state: 'running' })).toBe('working');
  expect(activityPhase({ state: 'running', thinkingText: 'hmm' })).toBe('thinking');
  expect(activityPhase({ state: 'permission' })).toBe('waiting');
  // ask_user keeps the run 'running' but blocks on the user via pendingAsk.
  expect(activityPhase({ state: 'running', pendingAsk: { id: 'a' } })).toBe('waiting');
  expect(activityPhase({ state: 'running', compacting: true })).toBe('compacting');
  expect(activityPhase({ state: 'running', autoVerifying: true })).toBe('verifying');
});

test('compacting/verifying take priority over run phase', () => {
  expect(activityPhase({ state: 'running', thinkingText: 'x', compacting: true })).toBe('compacting');
  expect(activityPhase({ state: 'running', autoVerifying: true })).toBe('verifying');
});

test('activityLabel maps phases to copy', () => {
  expect(activityLabel('thinking', 0)).toBe('Thinking');
  expect(activityLabel('waiting', 0)).toBe('Waiting for you');
  expect(activityLabel('compacting', 0)).toBe('Compacting context');
  expect(activityLabel('verifying', 0)).toBe('Running auto-verify');
  expect(activityLabel('working', 0)).toBe(WORKING_GERUNDS[0]);
  expect(activityLabel('working', 8000)).toBe(WORKING_GERUNDS[2]);
  expect(activityLabel(null, 0)).toBe(null);
});
