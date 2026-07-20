import { test, expect } from 'bun:test';
import { fuseLedgerDetails } from './ledger-details.jsx';

// fuseLedgerDetails is the shared (desktop + mobile) step that attaches inline
// `detail:{node}` panels to a ledger's rows: an edit's unified diff sibling
// fuses into the LAST edit row; any other row with a text `body` gets an output
// panel; the live row never gets a static detail; source rows aren't mutated.

const row = (tool, extra = {}) => ({ tool, arg: { text: `${tool} arg` }, out: 'ok', status: 'ok', ...extra });
const diff = { type: 'diff', diffText: '@@ -1 +1 @@', filename: 'a.go' };

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

test('a body-less non-edit row gets no detail', () => {
  const out = fuseLedgerDetails([row('read', { id: 'r1' })], null);
  expect(out[0].detail).toBeUndefined();
});

test('the live row never carries a static detail', () => {
  const rows = [row('read', { id: 'r1', body: 'x' }), { tool: 'bash', arg: { text: 'go test' }, live: true, body: 'ignored' }];
  const out = fuseLedgerDetails(rows, null);
  expect(out[0].detail).toBeTruthy(); // done row with body → detail
  expect(out[1].detail).toBeUndefined(); // live row → none, even with a body
});

test('an edit row prefers its diff sibling over a body', () => {
  const rows = [row('edit', { id: 'e1', body: 'preview text' })];
  const out = fuseLedgerDetails(rows, diff);
  // the diff node wins (it's the real change); we don't attach both
  expect(out[0].detail).toBeTruthy();
});

test('source rows are not mutated', () => {
  const rows = [row('edit', { id: 'e1' })];
  const out = fuseLedgerDetails(rows, diff);
  expect(rows[0].detail).toBeUndefined(); // original untouched
  expect(out[0]).not.toBe(rows[0]); // a new object
});
