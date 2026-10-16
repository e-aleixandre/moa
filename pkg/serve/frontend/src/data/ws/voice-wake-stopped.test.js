// voice-wake-stopped.test.js — the record a Stop leaves for a minutes wake it
// dropped carries no content and says nothing to the owner: it must not become
// a row, live, on reload or in the projection. Run with `bun test`.

import { test, expect } from 'bun:test';
import { store, setState } from '../store.js';
import { handleWsUserMessage } from './stream.js';
import { normalizeHistory } from './history.js';
import { projectStream } from '../stream-model.js';

const custom = { source: 'voice_call', kind: 'minutes_wake_stopped', call_id: 'c1', delivery_id: 'c1/minutes/wake' };

test('a reloaded stop record is not a row', () => {
  expect(normalizeHistory([{ role: 'session_event', msg_id: 'w-1', timestamp: 1, content: [], custom }])).toEqual([]);
});

test('a live stop record is not a row', () => {
  setState({ sessions: { s1: { messages: [] } } });
  handleWsUserMessage('s1', { msg_id: 'w-1', timestamp: 1, text: '', custom });
  expect(store.get().sessions.s1.messages).toEqual([]);
});

test('the projection draws nothing for it and keeps the call block around it', () => {
  const user = (c, id) => ({ role: 'user', _msg_id: id, timestamp: 1, content: [], custom: c });
  const blocks = projectStream({ messages: [
    user({ source: 'voice_call', kind: 'relay', call_id: 'c1', model_summary: 'x' }, 'a'),
    user(custom, 'b'),
  ] });
  expect(blocks.map((b) => b.kind)).toEqual(['voice_call']);
  expect(blocks[0].exchanges).toHaveLength(1);
});
