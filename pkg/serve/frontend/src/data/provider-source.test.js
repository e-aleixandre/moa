import { expect, test } from 'bun:test';
import { providerSourceLabel } from './provider-source.js';
import { normalizeHistory } from './ws/history.js';
import { newBuffers, reduceMessageEnd, reduceToolCallStart } from './conversation-reducer.js';
import { projectStream } from './stream-model.js';

const source = { kind: 'api_backup', usage_complete: true, estimated_cost: 0.0012, input_transformations: ['thinking_dropped:organization_binding_mismatch'] };
test('API cost is unknown rather than zero when usage or price is missing', () => {
  expect(providerSourceLabel({ kind: 'api_backup' })).toBe('API backup · API cost unknown');
  expect(providerSourceLabel({ kind: 'api_backup', estimated_cost: 0 })).toBe('API backup · API cost unknown');
  expect(providerSourceLabel(source)).toContain('estimated API cost $0.0012');
  expect(providerSourceLabel(source)).toContain('upstream dropped earlier thinking');
});
test('OAuth estimates are equivalent cost, not an API invoice', () => {
  expect(providerSourceLabel({ ...source, kind: 'oauth' })).toContain('equivalent $');
  expect(providerSourceLabel({ ...source, kind: 'oauth' })).not.toContain('API cost');
});
test('history and the shared child reducer retain source for text and tools-only responses', () => {
  const raw = [{ role: 'assistant', msg_id: 'a', provider_source: source, content: [{ type: 'tool_call', tool_call_id: 'tool-1', tool_name: 'read', arguments: {} }] }];
  expect(normalizeHistory(raw)[0].provider_source).toEqual(source);
  const target = { messages: [] }; const buffers = newBuffers();
  reduceToolCallStart(target, buffers, { tool_call_id: 'tool-1', tool_name: 'read' });
  reduceMessageEnd(target, buffers, '', 'a', 123, source);
  expect(target.messages[0].provider_source).toEqual(source);
  expect(target.messages[0]._msg_id).toBe('a');
  const result = projectStream({ messages: normalizeHistory(raw) });
  expect(result.flatMap((b) => b.blocks || []).some((b) => b.type === 'provider_source' && b.source.kind === 'api_backup')).toBe(true);
});
