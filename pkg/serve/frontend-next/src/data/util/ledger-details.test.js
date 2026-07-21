import { test, expect } from 'bun:test';
import { fuseLedgerDetails } from './ledger-details.jsx';

// fuseLedgerDetails is the shared (desktop + mobile) step that attaches inline
// `detail:{node}` panels to a ledger's rows: an edit's unified diff sibling
// fuses into the LAST edit row; any other row with a text `body` gets an output
// panel; the live row never gets a static detail; source rows aren't mutated.

const row = (tool, extra = {}) => ({ tool, arg: { text: `${tool} arg` }, out: 'ok', status: 'ok', ...extra });
const diff = { type: 'diff', diffText: '@@ -1 +1 @@', filename: 'a.go' };

function detailClasses(node, classes = []) {
  if (!node || typeof node !== 'object') return classes;
  if (node.props?.className) classes.push(node.props.className);
  const children = node.props?.children;
  for (const child of Array.isArray(children) ? children : [children]) detailClasses(child, classes);
  return classes;
}

function detailText(node) {
  if (node == null) return '';
  if (typeof node === 'string') return node;
  if (Array.isArray(node)) return node.map(detailText).join('');
  return node.props?.code || detailText(node.props?.children);
}

test('a diff sibling fuses into the LAST edit row only', () => {
  const rows = [row('read'), row('edit', { id: 'e1' }), row('grep'), row('edit', { id: 'e2' })];
  const out = fuseLedgerDetails(rows, diff);
  expect(out[1].detail).toBeUndefined(); // earlier edit untouched
  expect(out[3].detail).toBeTruthy(); // last edit gets the diff
  expect(out[3].detail.node).toBeTruthy();
  expect(out[0].detail).toBeUndefined();
  expect(out[2].detail).toBeUndefined();
});

test('a row with a text body gets an output detail (no diff sibling)', () => {
  const rows = [row('bash', { id: 'b1', body: '$ ls\na.js' })];
  const out = fuseLedgerDetails(rows, null);
  expect(out[0].detail).toBeTruthy();
  expect(out[0].detail.node).toBeTruthy();
});

test('a bash command detail puts the full command before its output', () => {
  const command = 'cd app && \\\n+  bun test';
  const out = fuseLedgerDetails([row('bash', { command, body: '12 tests passed' })], null);
  const node = out[0].detail.node;
  expect(detailText(node)).toBe(`$ ${command}12 tests passed`);
  expect(detailClasses(node)).toContain('doc-mono tg-cmd');
  expect(detailClasses(node)).toContain('tg-detail-divider');
});

test('a command-only bash row gets a command detail without a divider', () => {
  const out = fuseLedgerDetails([row('bash', { command: 'true' })], null);
  expect(detailText(out[0].detail.node)).toBe('$ true');
  expect(detailClasses(out[0].detail.node)).not.toContain('tg-detail-divider');
});

test('an input-first row shows its full input before output with a divider', () => {
  const path = '/workspace/a/very/long/path/that/the/header/will/ellipsis/configuration.json';
  const out = fuseLedgerDetails([row('read', { inputLine: path, body: 'contents' })], null);
  const node = out[0].detail.node;
  expect(detailText(node)).toBe(`${path}contents`);
  expect(detailText(node).indexOf(path)).toBeLessThan(detailText(node).indexOf('contents'));
  expect(detailClasses(node)).toContain('doc-mono tg-input');
  expect(detailClasses(node)).toContain('tg-detail-divider');
});

test('an input-first row without output remains expandable without a divider', () => {
  const path = '/workspace/configuration.json';
  const out = fuseLedgerDetails([row('read', { inputLine: path })], null);
  expect(out[0].detail).toBeTruthy();
  expect(detailText(out[0].detail.node)).toBe(path);
  expect(detailClasses(out[0].detail.node)).not.toContain('tg-detail-divider');
});

test('a grep input line appears before its output', () => {
  const inputLine = 'TODO · pkg/serve · include:*.go · literal';
  const out = fuseLedgerDetails([row('grep', { inputLine, body: 'pkg/serve/server.go: TODO' })], null);
  expect(detailText(out[0].detail.node)).toBe(`${inputLine}pkg/serve/server.go: TODO`);
  expect(detailClasses(out[0].detail.node)).toContain('doc-mono tg-input');
});

test('a non-bash output-only row remains an output detail', () => {
  const out = fuseLedgerDetails([row('read', { body: 'contents' })], null);
  expect(detailClasses(out[0].detail.node)).toContain('flush');
  expect(detailClasses(out[0].detail.node)).not.toContain('tg-cmd');
});

test('a body-less non-edit row gets no detail', () => {
  const out = fuseLedgerDetails([row('read', { id: 'r1' })], null);
  expect(out[0].detail).toBeUndefined();
});

test('the live row never carries a static detail', () => {
  const live = {
    tool: 'write',
    arg: { text: 'notes.txt' },
    live: true,
    body: 'ignored',
    livePreview: { kind: 'text', lines: ['partial content'], start: 0 },
  };
  const rows = [row('read', { id: 'r1', body: 'x' }), live];
  const out = fuseLedgerDetails(rows, null);
  expect(out[0].detail).toBeTruthy(); // done row with body → detail
  expect(out[1].detail).toBeUndefined(); // live row → none, even with a body
  expect(out[1]).toBe(live); // livePreview stays on the projection-owned row
});

test('an edit row prefers its diff sibling over a body', () => {
  const rows = [row('edit', { id: 'e1', body: 'preview text' })];
  const out = fuseLedgerDetails(rows, diff);
  // the diff node wins (it's the real change); we don't attach both
  expect(out[0].detail).toBeTruthy();
  expect(detailClasses(out[0].detail.node)).toContain('flush');
  expect(detailClasses(out[0].detail.node)).not.toContain('tg-cmd');
});

test('source rows are not mutated', () => {
  const rows = [row('edit', { id: 'e1' })];
  const out = fuseLedgerDetails(rows, diff);
  expect(rows[0].detail).toBeUndefined(); // original untouched
  expect(out[0]).not.toBe(rows[0]); // a new object
});
