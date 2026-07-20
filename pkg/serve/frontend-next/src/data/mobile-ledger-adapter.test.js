// mobile-ledger-adapter.test.js — run with `bun test`.
//
// Pins the ONLY data divergence between the desktop and mobile streams: the
// pure remap of a projectStream `ledger` block to MobileLedger props. Covers
// field renaming, resultString, mapToolToKind, buildDetail (bash / fused diff /
// degraded read) and the summary/icon aggregation.
import { test, expect } from 'bun:test';
import {
  adaptLedger, mapToolToKind, resultString, buildDetail,
} from './mobile-ledger-adapter.js';

// ── row/block fixture helpers (projectStream shapes) ────────────────────────
const row = (tool, text, out = '', status = 'ok', extra = {}) => ({
  tool, arg: { text }, out, status, id: `${tool}-${text}`, ...extra,
});
const ledger = (rows) => ({ type: 'ledger', id: 'ledger-0', rows });
const diff = (filename, diffText) => ({ type: 'diff', filename, diffText });

// ── mapToolToKind ───────────────────────────────────────────────────────────
test('mapToolToKind classifies read/edit/bash families and falls back', () => {
  expect(mapToolToKind('read')).toBe('read');
  expect(mapToolToKind('ls')).toBe('read');
  expect(mapToolToKind('grep')).toBe('read');
  expect(mapToolToKind('find')).toBe('read');
  expect(mapToolToKind('edit')).toBe('edit');
  expect(mapToolToKind('multiedit')).toBe('edit');
  expect(mapToolToKind('write')).toBe('edit');
  expect(mapToolToKind('bash')).toBe('bash');
  expect(mapToolToKind('fetch_content')).toBe('fetch_content');
  expect(mapToolToKind('')).toBe('tool');
});

// ── resultString ────────────────────────────────────────────────────────────
test('resultString: ok with no out → "ok", ok with out → out', () => {
  expect(resultString('', 'ok')).toBe('ok');
  expect(resultString('+5 −3', 'ok')).toBe('+5 −3');
});
test('resultString: error/rejected prefer own text then fall back', () => {
  expect(resultString('', 'err')).toBe('error');
  expect(resultString('boom', 'err')).toBe('boom');
  expect(resultString('', 'warn')).toBe('rejected');
});

// ── buildDetail ─────────────────────────────────────────────────────────────
test('buildDetail: bash row with body → bash output panel', () => {
  const r = row('bash', 'go test ./...', 'ok', 'ok', { body: '$ go test\nok' });
  expect(buildDetail(r, null)).toEqual({ type: 'bash', output: '$ go test\nok' });
});
test('buildDetail: edit row + sibling diff → fused diff panel', () => {
  const r = row('edit', 'ws.go', '+5 −3');
  const d = diff('ws.go', '@@ -1 +1 @@\n-a\n+b');
  expect(buildDetail(r, d)).toEqual({
    type: 'diff', diffText: '@@ -1 +1 @@\n-a\n+b', filename: 'ws.go',
  });
});
test('buildDetail: read multi-file degrades to bash panel with body', () => {
  const r = row('read', '2 files · pkg/serve', 'ok', 'ok', { body: 'pkg/serve/ws.go\npkg/serve/ws_test.go' });
  expect(buildDetail(r, null)).toEqual({
    type: 'bash', output: 'pkg/serve/ws.go\npkg/serve/ws_test.go',
  });
});
test('buildDetail: body-less row → null (inert)', () => {
  expect(buildDetail(row('read', 'a.js'), null)).toBeNull();
});

// ── adaptLedger: field renaming ─────────────────────────────────────────────
test('adaptLedger renames tool→name, arg.text→action, out/status→result', () => {
  const props = adaptLedger(ledger([
    row('read', 'a.js', 'ok', 'ok'),
    row('bash', 'ls', '', 'ok', { body: 'a\nb' }),
  ]));
  expect(props.rows[0]).toMatchObject({
    id: 'read-a.js', kind: 'read', name: 'read', action: 'a.js', result: 'ok', detail: null,
  });
  expect(props.rows[1]).toMatchObject({
    id: 'bash-ls', kind: 'bash', name: 'bash', action: 'ls', result: 'ok',
    detail: { type: 'bash', output: 'a\nb' },
  });
});

// ── adaptLedger: diff sibling fusion into the last edit row ──────────────────
test('adaptLedger fuses the diff sibling into the ledger last edit row', () => {
  const props = adaptLedger(
    ledger([
      row('read', 'a.js'),
      row('edit', 'ws.go', '+5 −3'),
    ]),
    diff('ws.go', '@@ -1 +1 @@\n-a\n+b'),
  );
  expect(props.rows[0].detail).toBeNull();
  expect(props.rows[1].detail).toEqual({
    type: 'diff', diffText: '@@ -1 +1 @@\n-a\n+b', filename: 'ws.go',
  });
  // The diff row starts expanded, so the whole ledger opens.
  expect(props.defaultOpen).toBe(true);
  expect(props.defaultOpenRowIds).toEqual(['edit-ws.go']);
});

// ── adaptLedger: summary + icons ────────────────────────────────────────────
test('adaptLedger summarizes rows by kind and derives distinct icon keys', () => {
  const props = adaptLedger(ledger([
    row('read', 'a.js'),
    row('read', 'b.js'),
    row('edit', 'c.js', '+1 −0'),
    row('bash', 'ls', '', 'ok', { body: 'x' }),
  ]));
  expect(props.summary).toBe('2 reads · 1 edit · 1 bash');
  expect(props.iconKeys).toEqual(['file', 'pencil', 'terminal']);
});

test('adaptLedger with no diff sibling leaves the ledger collapsed', () => {
  const props = adaptLedger(ledger([row('read', 'a.js')]));
  expect(props.defaultOpen).toBe(false);
  expect(props.defaultOpenRowIds).toEqual([]);
});

// ── adaptLedger: live row extraction (B·Tail) ───────────────────────────────
test('adaptLedger pulls a trailing live row out of rows/summary into liveRow', () => {
  const props = adaptLedger(ledger([
    row('read', 'a.js', '10 lines'),
    { tool: 'bash', arg: { text: 'go test ./...' }, out: '', status: 'ok', id: 't2', live: true, startedAt: 555 },
  ]));
  expect(props.rows).toHaveLength(1);
  expect(props.rows[0].id).toBe('read-a.js');
  expect(props.summary).toBe('1 read');
  expect(props.liveRow).toEqual({
    id: 't2', tool: 'bash', arg: { text: 'go test ./...' }, startedAt: 555,
  });
});

test('adaptLedger returns liveRow null when nothing is live', () => {
  const props = adaptLedger(ledger([row('read', 'a.js')]));
  expect(props.liveRow).toBeNull();
});
