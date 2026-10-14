// patch-files.test.js — the diffs an apply_patch recorded reach its row live,
// after a reconnect and after a reload, so the card shows what was written.
// Run with `bun test`.

import { test, expect } from 'bun:test';
import { store, setState } from '../store.js';
import { normalizeHistory, appendNormalizedHistoryDelta } from './history.js';
import { handleWsToolEnd } from './tools.js';
import { withLiveTools } from './init.js';
import { projectStream } from '../stream-model.js';
import { projectedToolDetailNode } from '../util/ledger-details.jsx';

const files = [{ path: 'a.go', op: 'M', add: 1, del: 1, diff: '@@ -1 +1 @@\n   1 -x\n   1 +y' }];
const call = { role: 'assistant', msg_id: 'a1', content: [{ type: 'tool_call', tool_name: 'apply_patch', tool_call_id: 'p1', arguments: { _truncated: true } }] };
const result = (custom) => ({ role: 'tool_result', msg_id: 'r1', tool_name: 'apply_patch', tool_call_id: 'p1', content: [{ type: 'text', text: 'M a.go' }], ...(custom ? { custom } : {}) });
const ledgerRow = (messages) => projectStream({ messages })[0].blocks.find((b) => b.type === 'ledger').rows[0];

test('a reloaded apply_patch row carries the recorded diffs even when its arguments were cut', () => {
  const messages = normalizeHistory([call, result({ patch_files: files })]);
  expect(messages[0].patchFiles).toEqual(files);
  expect(ledgerRow(messages).patchFiles).toEqual(files);
  expect(normalizeHistory([call, result()])[0].patchFiles).toBeUndefined();
});

test('a history delta whose call is already loaded attaches the diffs to that row', () => {
  const prefix = normalizeHistory([call]);
  appendNormalizedHistoryDelta(prefix, [result({ patch_files: files })]);
  expect(prefix[0].patchFiles).toEqual(files);
});

test('a live tool_end carries the diffs to the row', () => {
  setState({ sessions: { s1: { messages: [{ _type: 'tool_start', tool_call_id: 'p1', tool_name: 'apply_patch', status: 'running' }] } } });
  handleWsToolEnd('s1', { tool_call_id: 'p1', tool_name: 'apply_patch', result: 'M a.go', patch_files: files });
  expect(store.get().sessions.s1.messages[0].patchFiles).toEqual(files);
});

test('an apply_patch restored from live_tools keeps its diffs', () => {
  const liveTools = [{ tool_call_id: 'p1', tool_name: 'apply_patch', args: {}, status: 'done', result: 'M a.go', patch_files: files }];
  expect(ledgerRow(withLiveTools([], liveTools)).patchFiles).toEqual(files);
});

test('a persisted subagent patch call opens into the recorded diffs', () => {
  const node = projectedToolDetailNode('apply_patch', '', { output: 'M a.go', patch_files: files });
  expect(node.props.files).toEqual(files);
  expect(node.props.rowOpen).toBe(true);
});

// A persisted subagent: the REST transcript lists files and counts without
// diffs, and the card must show them closed, not the cut start of the patch.
test('a persisted child apply_patch item shows its files closed and fetches diffs from the detail URL', async () => {
  const { normalizeConversationProjection } = await import('./history.js');
  const summary = [{ path: 'a.go', op: 'M', add: 1, del: 1, has_diff: true }, { path: 'b.go', op: 'D', add: 0, del: 2, has_diff: true }];
  const [msg] = normalizeConversationProjection([
    { id: 'tool:r:0', role: 'tool', tool: 'apply_patch', status: 'ok', target: '*** Begin Patch\n*** Update Fi', patch_files: summary },
  ], '/api/sessions/s/subagents/j');
  expect(msg.patchFiles).toEqual(summary);
  const row = ledgerRow([msg]);
  expect(row.patchFiles).toEqual(summary);
  expect(row.arg.text).toBe('2 files');
  expect(row.out).toBe('+1 −3');
  expect(row.patchDetailUrl).toBe('/api/sessions/s/subagents/j?detail=full&item_id=tool%3Ar%3A0');
  expect(row.detailUrl).toBeUndefined();
});

test('a live child tool_end keeps the recorded diffs', async () => {
  const { reduceToolEnd } = await import('../conversation-reducer.js');
  const target = reduceToolEnd({ messages: [{ _type: 'tool_start', tool_call_id: 'p1', tool_name: 'apply_patch', status: 'running' }] }, {}, { tool_call_id: 'p1', result: 'M a.go', patch_files: files });
  expect(target.messages[0].patchFiles).toEqual(files);
});

test('files past the transcript cap keep the real totals and say how many more', async () => {
  const { normalizeConversationProjection } = await import('./history.js');
  const listed = Array.from({ length: 100 }, (_, i) => ({ path: `f${i}`, op: 'M', add: 1, del: 0, has_diff: true }));
  const [msg] = normalizeConversationProjection([
    { id: 'tool:r:0', role: 'tool', tool: 'apply_patch', status: 'ok', patch_files: listed, patch_more: { files: 1, add: 1, del: 2 } },
  ], '/d');
  const row = ledgerRow([msg]);
  expect(row.arg.text).toBe('101 files');
  expect(row.out).toBe('+101 −2');
  expect(row.patchMore).toEqual({ files: 1, add: 1, del: 2 });
});
