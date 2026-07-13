// markdown.test.js — run with `bun test`
import { test, expect } from 'bun:test';
import { wrapTables } from './markdown.js';

test('wrapTables wraps a table in a horizontal-scroll container', () => {
  const html = '<table><thead><tr><th>a</th></tr></thead></table>';
  const out = wrapTables(html);
  expect(out).toBe(
    '<div class="md-table-wrap"><table><thead><tr><th>a</th></tr></thead></table></div>',
  );
});

test('wrapTables wraps every table when several are present', () => {
  const html = '<table><tr><td>1</td></tr></table><p>x</p><table><tr><td>2</td></tr></table>';
  const out = wrapTables(html);
  expect(out.match(/md-table-wrap/g).length).toBe(2);
  expect(out.match(/<\/div>/g).length).toBe(2);
});

test('wrapTables leaves table-less HTML untouched', () => {
  const html = '<p>no tables here</p>';
  expect(wrapTables(html)).toBe(html);
});
