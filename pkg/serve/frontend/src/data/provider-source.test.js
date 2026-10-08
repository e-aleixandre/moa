import { expect, test } from 'bun:test';
import { normalizeHistory } from './ws/history.js';
import { projectStream } from './stream-model.js';

// The conversation never labels how a reply was paid for: the status line
// says when the API key is in use.
const source = { kind: 'api_backup', usage_complete: true, estimated_cost: 0.0012 };
test('replies carry no source or cost label, in history or live', () => {
  const raw = [
    { role: 'assistant', msg_id: 'a', provider_source: source, content: [{ type: 'text', text: 'hi' }] },
    { role: 'session_event', msg_id: 'n', custom: { type: 'provider_source_note', source: 'api_backup' }, content: [{ type: 'text', text: 'API backup prepared.' }] },
  ];
  const messages = normalizeHistory(raw);
  expect(messages.some((m) => m._type === 'system')).toBe(false);
  const blocks = projectStream({ messages }).flatMap((b) => [b, ...(b.blocks || [])]);
  expect(blocks.some((b) => b.type === 'provider_source')).toBe(false);
  expect(JSON.stringify(blocks)).not.toContain('API backup');
});
