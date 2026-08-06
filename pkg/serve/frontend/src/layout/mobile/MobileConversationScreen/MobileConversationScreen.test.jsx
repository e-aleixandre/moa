import { test, expect } from 'bun:test';
import { aggregateAttention, attentionTone, mobileTitleChipPresentation, newResultSessions, nextMobileTitleRipple } from './attention-model.js';
import { sessionDisplayDotState } from '../../../data/util/format.js';

test('drawerSessions puts a running session with an unread result in New results', () => {
  const sessions = {
    s1: { id: 's1', state: 'idle', unseen: true, subagents: { child: { status: 'running' } } },
  };

  const newResults = newResultSessions(Object.values(sessions));
  expect(newResults.map((session) => session.id)).toEqual(['s1']);
});

test('aggregateAttention gives urgent sessions priority over unread results', () => {
  const attention = aggregateAttention({
    result: { id: 'result', state: 'running', unseen: true },
    permission: { id: 'permission', state: 'permission', unseen: true },
  }, 'active');

  expect(attention.urgent).toBe(1);
  expect(attention.unseen).toBe(1);
  expect(attentionTone(attention)).toBe('permission');
});

test('aggregateAttention ignores errors and requests already seen by the user', () => {
  const attention = aggregateAttention({
    error: { id: 'error', state: 'error', unseen: false },
    permission: { id: 'permission', state: 'permission', unseen: false },
    ask: { id: 'ask', state: 'running', pendingAsk: { id: 'a1' }, unseen: false },
  }, 'active');

  expect(attention.urgent).toBe(0);
  expect(attention.unseen).toBe(0);
  expect(attentionTone(attention)).toBeNull();
});

test('aggregateAttention counts unseen errors and requests as urgent', () => {
  const attention = aggregateAttention({
    error: { id: 'error', state: 'error', unseen: true },
    permission: { id: 'permission', state: 'permission', unseen: true },
    ask: { id: 'ask', state: 'running', pendingAsk: { id: 'a1' }, unseen: true },
  }, 'active');

  expect(attention.urgent).toBe(3);
  expect(attention.unseen).toBe(0);
  expect(attention.error).toBe(1);
  expect(attention.permission).toBe(2);
  expect(attentionTone(attention)).toBe('error');
});

test('mobile title presentation uses the winning state and arrival sequence', () => {
  expect(mobileTitleChipPresentation({ unseen: 1, arrival: 4 })).toMatchObject({
    tone: 'unseen', hasAttention: true, arrival: 4,
  });
  expect(mobileTitleChipPresentation({ urgent: 1, permission: 1, arrival: 5 })).toMatchObject({
    tone: 'permission', arrival: 5,
  });
  expect(mobileTitleChipPresentation({ urgent: 1, error: 1, arrival: 6 })).toMatchObject({
    tone: 'error', arrival: 6,
  });
  expect(mobileTitleChipPresentation({ urgent: 1, unseen: 2, error: 1, arrival: 7 }).tone).toBe('error');
});

test('mobile title presentation keeps the arrival sequence stable when attention is removed', () => {
  const current = mobileTitleChipPresentation({ urgent: 2, permission: 2, arrival: 8 });
  const acknowledged = mobileTitleChipPresentation({ urgent: 1, permission: 1, arrival: 8 });
  const arrival = mobileTitleChipPresentation({ urgent: 2, permission: 2, arrival: 9 });

  expect(acknowledged.arrival).toBe(current.arrival);
  expect(arrival.arrival).toBeGreaterThan(current.arrival);
});

test('mobile title ripple does not restart when existing attention is acknowledged', () => {
  const started = nextMobileTitleRipple(0, 0, { urgent: 2, permission: 2, arrival: 8 });
  const acknowledged = nextMobileTitleRipple(started.arrival, started.ripple, { urgent: 1, permission: 1, arrival: 8 });
  const later = nextMobileTitleRipple(acknowledged.arrival, acknowledged.ripple, { urgent: 2, permission: 2, arrival: 9 });

  expect(acknowledged).toEqual(started);
  expect(later.ripple).toBe(started.ripple + 1);
});

test('a first unread occurrence in another session advances the chip arrival', () => {
  const first = aggregateAttention({
    a: { id: 'a', state: 'idle', unseen: true, attentionArrival: 1 },
  }, 'active');
  const second = aggregateAttention({
    a: { id: 'a', state: 'idle', unseen: true, attentionArrival: 1 },
    b: { id: 'b', state: 'idle', unseen: true, attentionArrival: 2 },
  }, 'active');

  expect(second.arrival).toBeGreaterThan(first.arrival);
  expect(nextMobileTitleRipple(first.arrival, 1, second).ripple).toBe(2);
});

test('session display colours remain urgent after their attention was seen', () => {
  expect(sessionDisplayDotState({ state: 'error', unseen: false })).toBe('error');
  expect(sessionDisplayDotState({ state: 'permission', unseen: false })).toBe('permission');
  expect(sessionDisplayDotState({ state: 'running', pendingAsk: { id: 'a1' }, unseen: false })).toBe('permission');
});
