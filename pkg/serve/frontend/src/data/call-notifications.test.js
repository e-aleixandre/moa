import { test, expect } from 'bun:test';
import { closeEndedCallNotifications } from './call-notifications.js';

function note(data) {
  return { data, closed: false, close() { this.closed = true; } };
}

test('a snapshot without a request closes its call notification, and only that', async () => {
  const live = note({ kind: 'call_request', request_id: 'r1', session_id: 's1' });
  const ended = note({ kind: 'call_request', request_id: 'r2', session_id: 's2' });
  // Same tag as a call, but a question or the missed-call notice: not ours.
  const question = note({ kind: 'ask', session_id: 's2' });
  const legacy = note({ session_id: 's3' });
  const registration = { getNotifications: async () => [live, ended, question, legacy] };
  await closeEndedCallNotifications([{ id: 'r1', state: 'ringing' }], registration);
  expect([live.closed, ended.closed, question.closed, legacy.closed]).toEqual([false, true, false, false]);
});

test('an unsupported browser or a failing registration is not an error', async () => {
  await closeEndedCallNotifications([], null);
  await closeEndedCallNotifications([], { getNotifications: async () => { throw new Error('x'); } });
});
