import { test, expect, beforeEach } from 'bun:test';
import { ARTIFACTS_CLOSED } from './artifacts-model.js';
import { store, setState } from './store.js';
import { projectStream } from './stream-model.js';
import { normalizeHistory } from './ws/history.js';
import { handleWsToolEnd, handleWsToolStart } from './ws/tools.js';

const descriptor = {
  file_id: 'f1', name: 'report.md', size: 12, mime: 'text/markdown',
  url: '/api/sessions/s1/files/f1',
};
const result = `Sent report.md\n${JSON.stringify(descriptor)}`;
const cardFile = {
  name: descriptor.name, size: descriptor.size, mime: descriptor.mime, url: descriptor.url,
};

beforeEach(() => {
  setState({
    artifacts: ARTIFACTS_CLOSED,
    sessions: { s1: { id: 's1', messages: [], subagents: {} } },
  });
});

test('the live WebSocket pipeline keeps pending send_file progress, then replaces it with its card', () => {
  handleWsToolStart('s1', { tool_call_id: 'send-1', tool_name: 'send_file', args: { path: 'report.md' } });
  let blocks = projectStream(store.get().sessions.s1)[0].blocks;
  expect(blocks).toMatchObject([{ type: 'ledger', rows: [{ id: 'send-1', tool: 'send_file', live: true }] }]);

  handleWsToolEnd('s1', { tool_call_id: 'send-1', tool_name: 'send_file', result });
  blocks = projectStream(store.get().sessions.s1)[0].blocks;
  expect(blocks.map(block => block.type)).toEqual(['file']);
  expect(blocks[0].file).toMatchObject(cardFile);
});

test('the normalized history pipeline replaces completed send_file rows with cards', () => {
  const messages = normalizeHistory([
    {
      role: 'assistant', msg_id: 'call-1', content: [{
        type: 'tool_call', tool_call_id: 'send-1', tool_name: 'send_file', arguments: { path: 'report.md' },
      }],
    },
    {
      role: 'tool_result', tool_call_id: 'send-1', content: [{ type: 'text', text: result }],
    },
  ]);

  const blocks = projectStream({ messages })[0].blocks;
  expect(blocks.map(block => block.type)).toEqual(['file']);
  expect(blocks[0].file).toMatchObject(cardFile);
});
