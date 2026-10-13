import { test, expect } from 'bun:test';
import { toolImageRefs } from './tool-images.js';
import { normalizeHistory, appendNormalizedHistoryDelta } from '../ws/history.js';
import { withLiveTools } from '../ws/init.js';
import { newBuffers, applyNestedEvent } from '../conversation-reducer.js';
import { projectStream } from '../stream-model.js';
import { fuseLedgerDetails } from './ledger-details.jsx';
import { LightboxBody } from '../../components/UserWaypoint/UserWaypoint.jsx';
import { THUMB_MAX_BYTES, ToolImages, toolImageEntries } from '../../components/ToolImages/ToolImages.jsx';

const REF = { type: 'image', attachment_id: 'att1', mime_type: 'image/png', attachment_size: 42 };
const WANT = [{ attachment_id: 'att1', mime_type: 'image/png', attachment_size: 42 }];

test('only stored images of a known type become references; bytes are never kept', () => {
  expect(toolImageRefs([{ type: 'text', text: 'x' }, { ...REF, data: 'AAAA' }])).toEqual(WANT);
  expect(toolImageRefs([{ type: 'image', data: 'AAAA', mime_type: 'image/png' }])).toBeUndefined();
  expect(toolImageRefs([{ ...REF, mime_type: 'image/svg+xml' }])).toBeUndefined();
  expect(toolImageRefs(undefined)).toBeUndefined();
});

const history = (content) => [
  { role: 'assistant', msg_id: 'a1', content: [{ type: 'tool_call', tool_call_id: 'tc1', tool_name: 'read', arguments: { path: '/tmp/a.png' } }] },
  { role: 'tool_result', tool_call_id: 'tc1', content },
];

test('history rebuilds the image reference of a finished read', () => {
  const [row] = normalizeHistory(history([REF]));
  expect(row).toMatchObject({ _type: 'tool_start', status: 'done', images: WANT });
  expect(normalizeHistory(history([{ type: 'text', text: 'ok' }]))[0].images).toBeUndefined();
});

test('a delta init completes a cached read row with its image', () => {
  const prefix = normalizeHistory([history([])[0]]);
  appendNormalizedHistoryDelta(prefix, [history([REF])[1]]);
  expect(prefix[0]).toMatchObject({ status: 'done', images: WANT });
});

test('live tool_end carries the image reference onto the row', () => {
  const target = { messages: [], streamingText: null, thinkingText: null };
  const buffers = newBuffers();
  applyNestedEvent(target, buffers, { type: 'tool_start', data: { tool_call_id: 'tc1', tool_name: 'read', args: { path: '/tmp/a.png' } } });
  applyNestedEvent(target, buffers, { type: 'tool_end', data: { tool_call_id: 'tc1', result: '', images: [REF] } });
  expect(target.messages[0].images).toEqual(WANT);
});

test('a reconnect snapshot of an ended call restores its image', () => {
  const rows = withLiveTools([], [{ tool_call_id: 'tc1', tool_name: 'read', status: 'done', result: '', images: [REF] }]);
  expect(rows[0].images).toEqual(WANT);
});

function readLedger(images) {
  const msg = { _type: 'tool_start', tool_call_id: 'tc1', tool_name: 'read', args: { path: '/tmp/a.png' }, status: 'done', result: '', images };
  return projectStream({ messages: [msg] })[0].blocks[0].rows;
}

function hasToolImages(node) {
  if (node == null || typeof node !== 'object') return false;
  if (Array.isArray(node)) return node.some(hasToolImages);
  return node.type === ToolImages || hasToolImages(node.props?.children);
}

test('the ledger row keeps the images; the picture opens with the row, only when a session is known', () => {
  const rows = readLedger(WANT);
  expect(rows[0].images).toEqual(WANT);
  const [fused] = fuseLedgerDetails(rows, [], 's1');
  expect(hasToolImages(fused.detail.node)).toBe(true);
  expect(fused.below).toBeUndefined();
  expect(hasToolImages(fuseLedgerDetails(rows, [])[0].detail?.node)).toBe(false);
  expect(hasToolImages(fuseLedgerDetails(readLedger(undefined), [], 's1')[0].detail?.node)).toBe(false);
});

test('the picture comes ahead of the row\'s text output inside the same detail', () => {
  const [row] = readLedger(WANT);
  const [fused] = fuseLedgerDetails([{ ...row, body: 'also text' }], [], 's1');
  const [first, second] = fused.detail.node.props.children;
  expect(first.type).toBe(ToolImages);
  expect(second).toBeTruthy();
});

test('a heavy image is a tile, a small one a thumbnail, an unfetchable one nothing', () => {
  const small = { attachment_id: 'a', mime_type: 'image/png', attachment_size: 4096 };
  const heavy = { attachment_id: 'b', mime_type: 'image/jpeg', attachment_size: THUMB_MAX_BYTES + 1 };
  const { entries, extra } = toolImageEntries([small, heavy], 's1', '/work/shot.png');
  expect(entries.map((e) => [e.kind, e.label])).toEqual([['thumb', 'shot.png (1 of 2)'], ['tile', 'shot.png (2 of 2)']]);
  expect(extra).toBe(0);
  expect(toolImageEntries([small], 's1', '/work/shot.png').entries[0].label).toBe('shot.png');
  expect(toolImageEntries([small], '', '/work/shot.png').entries).toEqual([]);
  expect(toolImageEntries([small, heavy], 's1', 'x', { a: true }).entries.map((e) => e.image.attachment_id)).toEqual(['b']);

});

// Review RED: with six images from one MCP result only four had a button.
test('images past the fourth stay reachable behind +N, without being fetched unasked', () => {
  const many = Array.from({ length: 6 }, (_, i) => ({ attachment_id: `att_${i}`, mime_type: 'image/png', attachment_size: 1024 }));
  const closed = toolImageEntries(many, 's1', '');
  expect(closed.entries).toHaveLength(4);
  expect(closed.extra).toBe(2);
  const open = toolImageEntries(many, 's1', '', {}, true);
  expect(open.entries).toHaveLength(6);
  expect(open.extra).toBe(0);
  expect(open.entries[5].label).toBe('Image (6 of 6)');
});

// Review RED: a pruned blob (404) left an empty dialog with no explanation.
test('a lightbox whose image failed says so instead of showing a broken image', () => {
  const text = (node) => (node == null ? '' : typeof node === 'string' ? node : Array.isArray(node) ? node.map(text).join('') : text(node.props?.children));
  const hasImg = (node) => node != null && typeof node === 'object' && (Array.isArray(node) ? node.some(hasImg) : node.type === 'img' || hasImg(node.props?.children));
  const ok = LightboxBody({ src: '/a', label: 'big.jpg', failed: false, onError() {} });
  expect(hasImg(ok)).toBe(true);
  const failed = LightboxBody({ src: '/a', label: 'big.jpg', failed: true, onError() {} });
  expect(hasImg(failed)).toBe(false);
  expect(text(failed)).toContain('Image unavailable');
});
