// subagents-daybreak.test.js — a child's card follows the Daybreak state its
// configuration change reports.
import { test, expect, beforeEach, afterEach } from 'bun:test';
import { handleWsInit } from './init.js';
import { handleWsSubagentEvent, flushSubagentEvents } from './subagents.js';
import { store, setState } from '../store.js';

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

function child(daybreak) {
  handleWsInit('s1', { state: 'running', messages: [], subagents: [{ job_id: 'J1', status: 'running', async: true, model: 'gpt-6-sol', daybreak, messages: [] }] });
}

test('a config change that turns Daybreak off clears the sunrise', () => {
  child(true);
  handleWsSubagentEvent('s1', { job_id: 'J1', event: { type: 'config_change', data: { model: 'gpt-6.1-sol', thinking: 'low', daybreak: false } } });
  flushSubagentEvents();
  expect(store.get().sessions.s1.subagents.J1).toMatchObject({ model: 'gpt-6.1-sol', daybreak: false });
});

test('a config change that turns Daybreak on shows it', () => {
  child(false);
  handleWsSubagentEvent('s1', { job_id: 'J1', event: { type: 'config_change', data: { model: 'gpt-6-sol', thinking: 'low', daybreak: true } } });
  flushSubagentEvents();
  expect(store.get().sessions.s1.subagents.J1.daybreak).toBe(true);
});

test('a config change without the field keeps the card as it was', () => {
  child(true);
  handleWsSubagentEvent('s1', { job_id: 'J1', event: { type: 'config_change', data: { model: 'gpt-6-luna', thinking: 'low' } } });
  flushSubagentEvents();
  expect(store.get().sessions.s1.subagents.J1.daybreak).toBe(true);
});
