import { beforeEach, expect, test } from 'bun:test';
import {
  armReadAnchor, consumeReadAnchor, hasReadAnchor, readAnchorBlockID, __resetReadAnchorsForTests,
} from './stream-read-anchor.js';

const prose = { kind: 'document', id: 'doc-answer', blocks: [{ type: 'prose', text: 'Answer' }] };
const tool = { kind: 'document', id: 'doc-tool', blocks: [{ type: 'ledger', rows: [] }] };

function session(overrides = {}) {
  return {
    id: 's1', state: 'idle', unseen: true,
    messages: [{ role: 'user' }, { role: 'assistant', content: 'Answer' }],
    ...overrides,
  };
}

beforeEach(() => __resetReadAnchorsForTests());

test('an unread completed result anchors its final prose document', () => {
  expect(readAnchorBlockID(session(), [prose])).toBe('doc-answer');
});

test('a running session never gets a read anchor', () => {
  const running = session({ state: 'running' });
  expect(readAnchorBlockID(running, [prose])).toBeNull();
  expect(armReadAnchor(running)).toBe(false);
});

test('a transcript without an assistant message has no read anchor', () => {
  expect(readAnchorBlockID(session({ messages: [{ role: 'user' }] }), [tool])).toBeNull();
});

test('a final user message has no read anchor', () => {
  expect(readAnchorBlockID(session({
    messages: [{ role: 'assistant', content: 'Done' }, { role: 'user', content: 'One more thing' }],
  }), [prose])).toBeNull();
});

test('a tool ending falls back to its rendered assistant document when no later prose exists', () => {
  const endedInTool = session({
    messages: [
      { role: 'user', content: 'Do it' },
      { role: 'assistant', content: 'Starting' },
      { _type: 'tool_start', tool_name: 'bash', status: 'done' },
    ],
  });
  expect(readAnchorBlockID(endedInTool, [prose, tool])).toBe('doc-tool');
});

test('the anchored selection survives acknowledgement after the latch was armed', () => {
  const unread = session();
  armReadAnchor(unread);
  expect(readAnchorBlockID({ ...unread, unseen: false }, [prose])).toBe('doc-answer');
});

test('the read anchor latch is consumed once', () => {
  armReadAnchor(session());
  expect(hasReadAnchor('s1')).toBe(true);
  expect(consumeReadAnchor('s1')).toBe(true);
  expect(consumeReadAnchor('s1')).toBe(false);
});
