// question-call.test.js — the call an open question escalates to, as the pin
// and the For you row say it, and its setting. Run with `bun test`.

import { test, expect } from 'bun:test';
import { questionCall } from './for-you.js';
import { callsPatch, questionCallMinutesValue } from './owner-calls.js';

const now = Date.parse('2026-10-19T08:00:00Z');
const at = (min) => new Date(now + min * 60000).toISOString();

test('a pending call counts down by the minute', () => {
  expect(questionCall({ id: 'q1', call_state: 'pending', call_at: at(8) }, now)).toEqual({ state: 'pending', text: 'Calls you in 8 min', tone: 'amber' });
  expect(questionCall({ id: 'q1', call_state: 'pending', call_at: at(7.2) }, now).text).toBe('Calls you in 8 min');
  expect(questionCall({ id: 'q1', call_state: 'pending', call_at: at(0.1) }, now).text).toBe('Calls you in 1 min');
  expect(questionCall({ id: 'q1', call_state: 'pending', call_at: at(-1) }, now).text).toBe('Calls you in 1 min');
});

test('a call hours away says when', () => {
  expect(questionCall({ id: 'q1', call_state: 'pending', call_at: at(600) }, now).text).toMatch(/^Calls you at /);
});

test('calling and missed are said in words; missed is red', () => {
  expect(questionCall({ id: 'q1', call_state: 'calling' }, now)).toEqual({ state: 'calling', text: 'Calling you…', tone: 'amber' });
  expect(questionCall({ id: 'q1', call_state: 'missed' }, now)).toEqual({ state: 'missed', text: 'Missed call', tone: 'red' });
});

test('a live call request about the question is "calling" before any refresh', () => {
  const item = { id: 'q1', call_state: 'pending', call_at: at(1) };
  expect(questionCall(item, now, [{ id: 'r1', question_id: 'q1' }]).state).toBe('calling');
  expect(questionCall(item, now, [{ id: 'r2', question_id: 'q2' }]).state).toBe('pending');
});

test('nothing when the question will not call', () => {
  for (const item of [{ id: 'q1' }, { id: 'q1', call_state: 'cancelled' }, { id: 'q1', call_state: 'interrupted' }, { id: 'q1', call_state: 'pending' }, null]) {
    expect(questionCall(item, now)).toBeNull();
  }
});

test('the setting takes whole minutes from 5 to 240 and travels with the calls block', () => {
  expect(questionCallMinutesValue('15')).toEqual({ minutes: 15 });
  for (const bad of ['', '4', '241', '7.5', 'x']) expect(questionCallMinutesValue(bad).error).toBe('Enter 5 to 240 minutes.');
  expect(callsPatch({ ring_seconds: 30 }, { question_call_minutes: 20 })).toEqual({ calls: { ring_seconds: 30, question_call_minutes: 20 } });
  expect(callsPatch({ question_call_minutes: 20 }, { ring_seconds: 60 })).toEqual({ calls: { ring_seconds: 60, question_call_minutes: 20 } });
});
