import { test, expect } from 'bun:test';
import { store, setState } from '../store.js';
import { handleWsCommand } from '../ws-handlers.js';

// The server omits `messages` when the new branch is empty (first-message
// rewind). Absent must mean empty, or the old transcript stays on screen.
test('a branch event without messages empties the transcript', () => {
  setState({ sessions: { review: { id: 'review', messages: [
    { role: 'user', content: [{ type: 'text', text: 'First' }], _msg_id: 'first' },
    { role: 'assistant', content: [{ type: 'text', text: 'answer' }], _msg_id: 'answer' },
  ], subagents: {}, historyTruncated: true } } });
  handleWsCommand('review', { command: 'branch' });
  expect(store.get().sessions.review.messages).toEqual([]);
  expect(store.get().sessions.review.historyTruncated).toBe(false);
});
