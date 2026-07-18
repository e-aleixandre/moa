// conversation-reducer.test.js — run with `bun test`
import { test, expect } from 'bun:test';
import {
  newBuffers, ensureTarget, applyNestedEvent,
  reduceTextDelta, reduceToolStart, reduceToolEnd, reduceMessageEnd,
  reduceToolCallStart, reduceMessageStart,
} from './conversation-reducer.js';

function freshTarget() {
  return { messages: [], streamingText: null, thinkingText: null };
}

test('textDelta accumulates into streamingText', () => {
  const t = freshTarget();
  const b = newBuffers();
  reduceTextDelta(t, b, 'Hel');
  reduceTextDelta(t, b, 'lo');
  expect(t.streamingText).toBe('Hello');
});

test('toolStart appends a running tool_start block', () => {
  const t = freshTarget();
  const b = newBuffers();
  reduceToolStart(t, b, { tool_call_id: 'tc1', tool_name: 'bash', args: { cmd: 'ls' } });
  expect(t.messages).toHaveLength(1);
  expect(t.messages[0]._type).toBe('tool_start');
  expect(t.messages[0].status).toBe('running');
  expect(t.messages[0].tool_name).toBe('bash');
});

test('toolEnd marks the block done with result', () => {
  const t = freshTarget();
  const b = newBuffers();
  reduceToolStart(t, b, { tool_call_id: 'tc1', tool_name: 'bash', args: {} });
  reduceToolEnd(t, b, { tool_call_id: 'tc1', result: 'output', is_error: false });
  expect(t.messages[0].status).toBe('done');
  expect(t.messages[0].result).toBe('output');
  expect(t.messages[0].streamingResult).toBeNull();
});

test('toolEnd error/rejected statuses', () => {
  const t = freshTarget();
  const b = newBuffers();
  reduceToolStart(t, b, { tool_call_id: 'a', tool_name: 'x', args: {} });
  reduceToolEnd(t, b, { tool_call_id: 'a', result: 'boom', is_error: true });
  expect(t.messages[0].status).toBe('error');

  const t2 = freshTarget();
  const b2 = newBuffers();
  reduceToolStart(t2, b2, { tool_call_id: 'b', tool_name: 'x', args: {} });
  reduceToolEnd(t2, b2, { tool_call_id: 'b', result: 'no', rejected: true });
  expect(t2.messages[0].status).toBe('rejected');
});

test('messageEnd with no materialized text uses fullText', () => {
  const t = freshTarget();
  const b = newBuffers();
  reduceTextDelta(t, b, 'partial');
  reduceMessageEnd(t, b, 'full authoritative text');
  const last = t.messages[t.messages.length - 1];
  expect(last.content[0].text).toBe('full authoritative text');
  expect(t.streamingText).toBeNull();
});

test('messageEnd derives tail from fullText when text was materialized', () => {
  const t = freshTarget();
  const b = newBuffers();
  // Stream some text, then a tool call materializes it.
  reduceTextDelta(t, b, 'Doing work. ');
  reduceToolCallStart(t, b, { tool_call_id: 'tc1', tool_name: 'bash' });
  // After the tool, more text streams.
  reduceTextDelta(t, b, 'Done.');
  reduceMessageEnd(t, b, 'Doing work. Done.');
  const texts = t.messages.filter(m => m.role === 'assistant').map(m => m.content[0].text);
  expect(texts).toEqual(['Doing work. ', 'Done.']);
});

test('messageEnd handles fullText starting with the literal "true"', () => {
  // Regression: materializedText must be a string, not a boolean, so
  // startsWith/slice work when the text starts with "true".
  const t = freshTarget();
  const b = newBuffers();
  reduceTextDelta(t, b, 'true story: ');
  reduceToolCallStart(t, b, { tool_call_id: 'tc1', tool_name: 'bash' });
  reduceTextDelta(t, b, 'the end');
  reduceMessageEnd(t, b, 'true story: the end');
  const texts = t.messages.filter(m => m.role === 'assistant').map(m => m.content[0].text);
  expect(texts).toEqual(['true story: ', 'the end']);
});

test('parity: same nested events applied to session-target and subagent-target', () => {
  const seq = [
    { type: 'message_start', data: {} },
    { type: 'text_delta', data: { delta: 'Analyzing ' } },
    { type: 'tool_call_start', data: { tool_call_id: 'tc1', tool_name: 'read' } },
    { type: 'tool_start', data: { tool_call_id: 'tc1', tool_name: 'read', args: { path: 'x' } } },
    { type: 'tool_update', data: { tool_call_id: 'tc1', delta: 'chunk1' } },
    { type: 'tool_end', data: { tool_call_id: 'tc1', result: 'file contents', is_error: false } },
    { type: 'text_delta', data: { delta: 'the file.' } },
    { type: 'message_end', data: { text: 'Analyzing the file.' } },
  ];

  const sessTarget = freshTarget();
  const sessBuf = newBuffers();
  const subTarget = freshTarget();
  const subBuf = newBuffers();

  for (const evt of seq) {
    applyNestedEvent(sessTarget, sessBuf, evt);
    applyNestedEvent(subTarget, subBuf, evt);
  }

  expect(subTarget.messages).toEqual(sessTarget.messages);
  // Sanity: we ended with two assistant texts + one tool block.
  const tool = sessTarget.messages.find(m => m._type === 'tool_start');
  expect(tool.status).toBe('done');
  expect(tool.result).toBe('file contents');
  const texts = sessTarget.messages.filter(m => m.role === 'assistant').map(m => m.content[0].text);
  expect(texts).toEqual(['Analyzing ', 'the file.']);
});

test('run_end marks generating tools as errored', () => {
  const t = freshTarget();
  const b = newBuffers();
  reduceToolCallStart(t, b, { tool_call_id: 'tc1', tool_name: 'bash' });
  expect(t.messages[0].status).toBe('generating');
  applyNestedEvent(t, b, { type: 'run_end', data: {} });
  expect(t.messages[0].status).toBe('error');
});

test('ensureTarget tolerates null/partial input', () => {
  const t = ensureTarget(null);
  expect(t.messages).toEqual([]);
  const t2 = ensureTarget({ streamingText: 'x' });
  expect(Array.isArray(t2.messages)).toBe(true);
});
