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

// A steer sent into a RUNNING subagent used to fall through to the default
// branch and vanish: accepted, queued and delivered by the server, yet invisible
// until the transcript was refetched.
test('a nested steer appears in the subagent transcript', () => {
  const t = freshTarget();
  const b = newBuffers();
  applyNestedEvent(t, b, { type: 'steer', data: { id: 's1', msg_id: 'm1', text: 'look at the image' } });
  expect(t.messages).toHaveLength(1);
  expect(t.messages[0].role).toBe('user');
  expect(t.messages[0].content[0].text).toBe('look at the image');
});

test('a nested steer is deduplicated by msg_id, not by text', () => {
  const t = freshTarget();
  const b = newBuffers();
  const evt = { type: 'steer', data: { id: 's1', msg_id: 'm1', text: 'Hola?' } };
  applyNestedEvent(t, b, evt);
  applyNestedEvent(t, b, evt);
  expect(t.messages).toHaveLength(1);
  // The same words sent again are a different message and must both show.
  applyNestedEvent(t, b, { type: 'steer', data: { id: 's2', msg_id: 'm2', text: 'Hola?' } });
  expect(t.messages).toHaveLength(2);
});

// The delegated task is announced (user_message) from the point it reaches the
// child's history. Before this reducer accepted it the event fell through to
// the default branch, so a subagent opened before its first message_end showed
// activity with no encargo until a reconnect snapshot arrived.
test('a nested user_message shows the delegated task ahead of the child activity', () => {
  const t = freshTarget();
  const b = newBuffers();
  applyNestedEvent(t, b, {
    type: 'user_message',
    data: { msg_id: 'p1', text: 'Investiga el bug', custom: { source: 'subagent_parent' } },
  });
  applyNestedEvent(t, b, { type: 'message_end', data: { msg_id: 'a1', text: 'Empiezo' } });

  expect(t.messages).toHaveLength(2);
  expect(t.messages[0]).toMatchObject({
    role: 'user', _msg_id: 'p1', custom: { source: 'subagent_parent' },
  });
  expect(t.messages[0].content[0].text).toBe('Investiga el bug');
  expect(t.messages[1].role).toBe('assistant');
});

test('a nested user_message is deduplicated by msg_id, not by text', () => {
  const t = freshTarget();
  const b = newBuffers();
  const evt = { type: 'user_message', data: { msg_id: 'p1', text: 'Otra vez' } };
  applyNestedEvent(t, b, evt);
  applyNestedEvent(t, b, evt);
  expect(t.messages).toHaveLength(1);
  // The same words delegated again are a different message and must both show.
  applyNestedEvent(t, b, { type: 'user_message', data: { msg_id: 'p2', text: 'Otra vez' } });
  expect(t.messages).toHaveLength(2);
});

test('a nested user_message carries structured content when it has one', () => {
  const t = freshTarget();
  const b = newBuffers();
  applyNestedEvent(t, b, {
    type: 'user_message',
    data: { msg_id: 'p1', content: [{ type: 'text', text: 'mira' }, { type: 'image_ref', id: 'img1' }] },
  });
  expect(t.messages[0].content).toHaveLength(2);
});

test('ensureTarget tolerates null/partial input', () => {
  const t = ensureTarget(null);
  expect(t.messages).toEqual([]);
  const t2 = ensureTarget({ streamingText: 'x' });
  expect(Array.isArray(t2.messages)).toBe(true);
});

// A hydration that overlaps a turn splices the server's copy of it into the
// transcript; the message_end closing that same turn then arrives afterwards.
// Without id-based dedup the turn was rendered twice.
test('messageEnd does not re-append a turn the transcript already holds', () => {
  const t = freshTarget();
  const b = newBuffers();
  t.messages = [{ role: 'assistant', _msg_id: 'm1', content: [{ type: 'text', text: 'Empiezo' }] }];
  applyNestedEvent(t, b, { type: 'message_end', data: { msg_id: 'm1', text: 'Empiezo' } });
  expect(t.messages).toHaveLength(1);
  // A different turn with the same words is still its own message.
  applyNestedEvent(t, b, { type: 'message_end', data: { msg_id: 'm2', text: 'Empiezo' } });
  expect(t.messages).toHaveLength(2);
});

// The REST projection caps text at a byte budget (see safeDisplayText), so a
// hydrated row can be a PREFIX of the real answer. fullText is authoritative,
// so closing that turn must complete the row in place — deduplicating by
// dropping the event lost the rest of the response for good.
test('messageEnd completes a truncated snapshot row instead of discarding its text', () => {
  const t = freshTarget();
  const b = newBuffers();
  t.messages = [
    { role: 'user', _msg_id: 'm-task', content: [{ type: 'text', text: 'Investiga' }] },
    { role: 'assistant', _msg_id: 'm1', content: [{ type: 'text', text: 'short snapshot' }] },
  ];
  applyNestedEvent(t, b, {
    type: 'message_end',
    data: { msg_id: 'm1', text: 'short snapshot followed by the full live response' },
  });

  expect(t.messages).toHaveLength(2);
  const answer = t.messages[1];
  // Same row, same identity, same position — with the complete text.
  expect(answer._msg_id).toBe('m1');
  expect(answer.content[0].text).toBe('short snapshot followed by the full live response');
  expect(t.messages[0]._msg_id).toBe('m-task');
});

test('messageEnd completing an existing row respects text already materialized by a tool', () => {
  const t = freshTarget();
  const b = newBuffers();
  // A tool call materialized the head of this turn; the row was then hydrated
  // from REST holding the whole (truncated) turn under the same id.
  t.messages = [{ role: 'assistant', _msg_id: 'm1', content: [{ type: 'text', text: 'Analizando' }] }];
  b.materializedText = 'Analizando ';
  applyNestedEvent(t, b, { type: 'message_end', data: { msg_id: 'm1', text: 'Analizando el fichero entero' } });

  expect(t.messages).toHaveLength(1);
  // The row is the whole turn, not just the tail after the materialized head.
  expect(t.messages[0].content[0].text).toBe('Analizando el fichero entero');
  expect(b.materializedText).toBe('');
});

test('messageEnd with no text does not blank an existing row', () => {
  const t = freshTarget();
  const b = newBuffers();
  t.messages = [{ role: 'assistant', _msg_id: 'm1', content: [{ type: 'text', text: 'ya renderizado' }] }];
  applyNestedEvent(t, b, { type: 'message_end', data: { msg_id: 'm1', text: '' } });
  expect(t.messages[0].content[0].text).toBe('ya renderizado');
});

test('messageEnd without a msg_id keeps appending as before', () => {
  const t = freshTarget();
  const b = newBuffers();
  applyNestedEvent(t, b, { type: 'message_end', data: { text: 'uno' } });
  applyNestedEvent(t, b, { type: 'message_end', data: { text: 'uno' } });
  expect(t.messages).toHaveLength(2);
});
