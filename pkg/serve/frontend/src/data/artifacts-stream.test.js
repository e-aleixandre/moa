// artifacts-stream.test.js — the send_file block the drawer reads from: the
// existing descriptor keys stay exactly as they were, and the optional
// title/description ride along additively. Run with `bun test`.

import { test, expect } from 'bun:test';
import { projectStream } from './stream-model.js';

const sendFile = (json) => ({
  _type: 'tool_start', tool_call_id: 't1', tool_name: 'send_file', args: {},
  status: 'done', result: `Sent report.md\n${JSON.stringify(json)}`,
});

function fileBlock(message) {
  const blocks = projectStream({ messages: [message] });
  for (const block of blocks) {
    const found = (block.blocks || []).find((b) => b.type === 'file');
    if (found) return found;
  }
  return null;
}

function documentBlocks(message) {
  return projectStream({ messages: [message] })[0].blocks;
}

const base = { file_id: 'f1', name: 'report.md', size: 1234, mime: 'text/markdown', url: '/api/sessions/s1/files/f1' };

test('an old descriptor without the new keys projects exactly as before', () => {
  expect(fileBlock(sendFile(base)).file).toEqual({
    name: 'report.md', size: 1234, mime: 'text/markdown', url: '/api/sessions/s1/files/f1',
  });
});

test('a delivered descriptor of every format is a card without a duplicate tool row', () => {
  for (const [name, mime] of [
    ['report.md', 'text/markdown'],
    ['diagram.png', 'image/png'],
    ['archive.zip', 'application/zip'],
    ['report.pdf', 'application/pdf'],
  ]) {
    const blocks = documentBlocks(sendFile({ ...base, name, mime }));
    expect(blocks.map(block => block.type)).toEqual(['file']);
    expect(blocks[0].file).toMatchObject({ name, mime });
  }
});

test('optional title and description are carried into the block', () => {
  const block = fileBlock(sendFile({ ...base, title: 'Final report', description: 'What was asked for' }));
  expect(block.file.title).toBe('Final report');
  expect(block.file.description).toBe('What was asked for');
  expect(block.file.url).toBe('/api/sessions/s1/files/f1');
});

test('non-string metadata is ignored rather than rendered', () => {
  const block = fileBlock(sendFile({ ...base, title: 42, description: { a: 1 } }));
  expect('title' in block.file).toBe(false);
  expect('description' in block.file).toBe(false);
});

test('a failed send_file still projects no file block', () => {
  const failed = { ...sendFile(base), status: 'error' };
  expect(fileBlock(failed)).toBeNull();
  expect(documentBlocks(failed)[0].rows).toMatchObject([{ tool: 'send_file', status: 'err' }]);
});

test('a rejected, cancelled, or invalid delivery keeps its feedback row', () => {
  const rejected = { ...sendFile(base), status: 'rejected' };
  const cancelled = { ...sendFile(base), status: 'cancelled' };
  const invalid = { ...sendFile(base), result: 'Sent report.md\nnot a descriptor' };
  expect(documentBlocks(rejected)[0].rows).toMatchObject([{ tool: 'send_file', status: 'warn' }]);
  expect(documentBlocks(cancelled)[0].rows).toMatchObject([{ tool: 'send_file', status: 'ok' }]);
  expect(documentBlocks(invalid)[0].rows).toMatchObject([{ tool: 'send_file', status: 'ok' }]);
});

test('a completed delivery is omitted without disturbing mixed tool order', () => {
  const blocks = projectStream({ messages: [
    { _type: 'tool_start', tool_call_id: 'read-1', tool_name: 'read', args: { path: 'before.md' }, status: 'done', result: 'before' },
    sendFile(base),
    { _type: 'tool_start', tool_call_id: 'grep-1', tool_name: 'grep', args: { pattern: 'after' }, status: 'done', result: 'after' },
  ] })[0].blocks;

  expect(blocks.map(block => block.type)).toEqual(['ledger', 'file', 'ledger']);
  expect(blocks[0].rows.map(row => row.id)).toEqual(['read-1']);
  expect(blocks[2].rows.map(row => row.id)).toEqual(['grep-1']);
});
