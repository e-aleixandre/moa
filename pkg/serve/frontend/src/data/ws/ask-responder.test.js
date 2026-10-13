// ask-responder.test.js — who answered an ask_user reaches the row as the
// server recorded it, live and after a reload, and never from the answer text.
// Run with `bun test`.

import { test, expect } from 'bun:test';
import { store, setState } from '../store.js';
import { normalizeHistory, appendNormalizedHistoryDelta } from './history.js';
import { handleWsToolEnd } from './tools.js';

const call = { role: 'assistant', msg_id: 'a1', content: [{ type: 'tool_call', tool_name: 'ask_user', tool_call_id: 'ask-1', arguments: { questions: [{ question: 'Deploy?' }] } }] };
const result = (custom) => ({ role: 'tool_result', msg_id: 'r1', tool_name: 'ask_user', tool_call_id: 'ask-1', content: [{ type: 'text', text: 'No' }], ...(custom ? { custom } : {}) });
const serena = { kind: 'owner', id: 'o1', name: 'Serena' };

test('a reloaded ask_user row carries the recorded responder, and none when there is no record', () => {
  expect(normalizeHistory([call, result({ ask_responder: serena })])[0].askResponder).toEqual(serena);
  expect(normalizeHistory([call, result()])[0].askResponder).toBeUndefined();
});

test('a history delta whose call is already loaded attaches the responder to that row', () => {
  const prefix = normalizeHistory([call]);
  appendNormalizedHistoryDelta(prefix, [result({ ask_responder: { kind: 'automation' } })]);
  expect(prefix[0].askResponder).toEqual({ kind: 'automation' });
});

test('a live tool_end carries the responder to the row', () => {
  setState({ sessions: { s1: { messages: [{ _type: 'tool_start', tool_call_id: 'ask-1', tool_name: 'ask_user', status: 'running' }] } } });
  handleWsToolEnd('s1', { tool_call_id: 'ask-1', tool_name: 'ask_user', result: 'No', ask_responder: serena });
  expect(store.get().sessions.s1.messages[0].askResponder).toEqual(serena);
});
