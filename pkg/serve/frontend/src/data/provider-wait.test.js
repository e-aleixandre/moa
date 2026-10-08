import { test, expect, beforeEach, afterEach } from 'bun:test';
import { newerProviderExecution, providerWaitDetails } from './provider-wait.js';
import { activityPhase, activityText } from './util/activity.js';
import { foregroundLine, canStopForeground } from '../layout/LiveBar/LiveBar.jsx';
import { subagentView } from './subagent-view-model.js';
import { handleWsInit } from './ws/init.js';
import { handleWsProviderExecution } from './ws/session.js';
import { handleWsSubagentEvent, flushSubagentEvents } from './ws/subagents.js';
import { normalizeHistory, normalizeConversationProjection } from './ws/history.js';
import { store, setState } from './store.js';

const WAIT = { generation: 1, epoch: 4, phase: 'provider_wait', model: 'claude-opus-5-5', provider: 'anthropic', saved: true,
  wait: { kind: 'quota_confirmed', scope: 'seven_day', observed_at: '2026-10-07T15:47:54Z', reset_at: '2026-10-08T10:00:00Z', next_attempt_at: '2026-10-08T10:00:00Z' } };

let oldRAF;
beforeEach(() => {
  oldRAF = globalThis.requestAnimationFrame;
  globalThis.requestAnimationFrame = () => 1;
  setState({ sessions: { s1: { id: 's1', state: 'running', messages: [], subagents: {} } }, activeSession: null });
});
afterEach(() => {
  if (oldRAF) globalThis.requestAnimationFrame = oldRAF;
  else delete globalThis.requestAnimationFrame;
});

test('provider pre-stream and confirmed wait are not working or waiting for user', () => {
  const session = { state: 'running', providerExecution: WAIT, runStartedAtMs: 1, thinkingText: 'old partial' };
  expect(activityPhase(session)).toBe('provider_wait');
  expect(activityText(session)).toBe('Waiting for weekly quota');
  expect(foregroundLine(session, Date.now())).toMatchObject({ waiting: true, elapsed: '', phase: 'provider_wait' });
  expect(canStopForeground(session, { kind: 'foreground', waiting: true, phase: 'provider_wait' })).toBe(true);
  session.providerExecution = { ...WAIT, phase: 'awaiting_provider', wait: null };
  expect(activityText(session)).toBe('Waiting for provider');
  expect(canStopForeground(session, { kind: 'foreground', waiting: true, phase: 'awaiting_provider' })).toBe(true);
});

test('transport retry has no quota claim or announced reset', () => {
  const execution = { ...WAIT, wait: { kind: 'transport_retry', next_attempt_at: WAIT.wait.next_attempt_at } };
  expect(activityText({ state: 'running', providerExecution: execution })).toBe('Waiting to retry provider');
  const lines = providerWaitDetails(execution).join('\n');
  expect(lines).toContain('Next attempt:');
  expect(lines).not.toContain('Announced reset:');
  expect(lines).toContain('Change model to try now');
});

test('announced reset is distinct from next attempt and bound continuation explains Stop', () => {
  const lines = providerWaitDetails({ ...WAIT, bound: true }).join('\n');
  expect(lines).toContain('Announced reset:');
  expect(lines).toContain('Next attempt:');
  expect(lines).toContain('Stop, then continue');
  expect(lines).not.toMatch(/available|guarantee|recover.*at/i);
  expect(providerWaitDetails({ ...WAIT, phase: 'awaiting_provider' })).toEqual([]);
});

test('an old epoch or generation cannot resurrect a cleared wait', () => {
  const clear = { ...WAIT, epoch: 5, phase: '', wait: null };
  expect(newerProviderExecution(clear, WAIT)).toBe(clear);
  const fresh = { ...WAIT, generation: 2, epoch: 1, phase: 'awaiting_provider', wait: null };
  expect(newerProviderExecution(fresh, clear)).toBe(fresh);
  handleWsProviderExecution('s1', WAIT);
  handleWsProviderExecution('s1', clear);
  handleWsProviderExecution('s1', WAIT);
  expect(store.get().sessions.s1.providerExecution.phase).toBe('');
});

test('same-process init restores root and child wait while restart clears both', () => {
  handleWsInit('s1', { state: 'running', provider_execution: WAIT, messages: [], subagents: [{ job_id: 'J1', status: 'running', async: true, model: 'claude-opus-5-5', provider_execution: WAIT, messages: [] }] });
  expect(store.get().sessions.s1.providerExecution).toEqual(WAIT);
  expect(store.get().sessions.s1.subagents.J1.providerExecution).toEqual(WAIT);
  handleWsInit('s1', { state: 'idle', messages: [], subagents: [], provider_execution: { generation: 0, epoch: 0, phase: '' } });
  expect(activityPhase(store.get().sessions.s1)).toBe(null);
  expect(store.get().sessions.s1.subagents.J1).toBeUndefined();
});

test('nested child wait uses authoritative epochs and targeted config without changing parent', () => {
  handleWsInit('s1', { state: 'running', provider_execution: WAIT, messages: [], subagents: [{ job_id: 'J1', status: 'running', async: true, model: 'claude-opus-5-5', provider_execution: { ...WAIT, epoch: 8 }, messages: [] }] });
  handleWsSubagentEvent('s1', { job_id: 'J1', event: { type: 'provider_execution', data: WAIT } });
  handleWsSubagentEvent('s1', { job_id: 'J1', event: { type: 'config_change', data: { model: 'gpt-6-sol', thinking: 'low' } } });
  flushSubagentEvents();
  const session = store.get().sessions.s1;
  expect(session.subagents.J1.providerExecution.epoch).toBe(8);
  expect(session.subagents.J1.model).toBe('gpt-6-sol');
  expect(session.providerExecution.model).toBe('claude-opus-5-5');
  expect(subagentView(session, 'J1').action).toBe('Waiting for weekly quota');
  expect(subagentView(session, 'J1').elapsed).toBeUndefined();
});

test('the real wait note normalizes as one descriptive system row, never a user turn', () => {
  const raw = { role: 'session_event', msg_id: 'N1', content: [{ type: 'text', text: 'Continue explicitly if interrupted.' }], custom: { source: 'provider_wait', type: 'provider_wait_note' } };
  expect(normalizeHistory([raw])).toEqual([{ _type: 'system', _msg_id: 'N1', timestamp: undefined, text: 'Continue explicitly if interrupted.' }]);
  expect(normalizeConversationProjection([{ role: 'system', source: 'provider_wait', id: 'N1', text: 'Continue explicitly if interrupted.' }])[0]._type).toBe('system');
});

test('an interrupted sidecar is terminal and cannot appear as live provider work', () => {
  const session = { id: 's1', messages: [], subagents: { J1: { jobId: 'J1', status: 'interrupted', model: 'claude-opus-5-5', messages: [] } } };
  const view = subagentView(session, 'J1');
  expect(view.terminal).toBe(true);
  expect(view.outcome).toBe('interrupted');
  expect(view.error).toContain('Continue explicitly');
  expect(view.action).toBeUndefined();
});
