// markdown.test.js — run with `bun test`
//
// DOMPurify needs a DOM, which bun test doesn't provide, so these tests exercise
// the real marked pipeline (parseMarkdown) up to — but not including —
// sanitization. That's the part we customize (the table-wrapping renderer).
import { test, expect } from 'bun:test';
import DOMPurify from 'dompurify';
import { parseMarkdown, renderMarkdownWithCaret } from './markdown.js';

test('a GFM table is wrapped in a horizontal-scroll container', () => {
  const html = parseMarkdown('| a | b |\n|---|---|\n| 1 | 2 |\n');
  expect(html).toContain('<div class="md-table-wrap">');
  expect(html).toContain('<table>');
  expect(html).toContain('</table></div>');
  // Balanced: exactly one wrapper open and close.
  expect(html.match(/md-table-wrap/g).length).toBe(1);
  expect(html.match(/<\/table><\/div>/g).length).toBe(1);
});

test('every table is wrapped when several are present', () => {
  const md = '| a |\n|---|\n| 1 |\n\ntext\n\n| c |\n|---|\n| 2 |\n';
  const html = parseMarkdown(md);
  expect(html.match(/md-table-wrap/g).length).toBe(2);
  expect(html.match(/<\/table><\/div>/g).length).toBe(2);
});

test('literal table tags inside a cell are escaped, not treated as markup', () => {
  // The old string-replace approach would have mangled a cell containing the
  // literal text "</table>"; the token renderer escapes it instead.
  const html = parseMarkdown('| a |\n|---|\n| `</table>` |\n');
  expect(html).toContain('&lt;/table&gt;');
  // Still exactly one real wrapper (the cell text did not create a second).
  expect(html.match(/md-table-wrap/g).length).toBe(1);
  expect(html.match(/<\/table><\/div>/g).length).toBe(1);
});

test('cell content and column alignment are preserved', () => {
  const html = parseMarkdown('| L | R |\n|:---|---:|\n| a | b |\n');
  expect(html).toContain('<th align="left">L</th>');
  expect(html).toContain('<th align="right">R</th>');
  expect(html).toContain('<td align="left">a</td>');
  expect(html).toContain('<td align="right">b</td>');
});

test('non-table markdown is untouched', () => {
  const html = parseMarkdown('# Title\n\nplain paragraph\n');
  expect(html).not.toContain('md-table-wrap');
});

test('a streaming caret stays inside the final prose paragraph', () => {
  // Bun has no DOM for DOMPurify, so preserve the rendered HTML in its
  // sanitizer seam while exercising the exported production helper.
  const sanitize = DOMPurify.sanitize;
  DOMPurify.sanitize = (html) => html;
  try {
    const html = renderMarkdownWithCaret('hello');
    const caret = '<span class="doc-cursor"></span>';
    expect(html).toContain('doc-cursor');
    expect(html.indexOf(caret)).toBeGreaterThan(html.indexOf('hello'));
    expect(html.indexOf(caret)).toBeLessThan(html.lastIndexOf('</p>'));
  } finally {
    DOMPurify.sanitize = sanitize;
  }
});

test('a streaming caret is passed through DOMPurify sanitization', () => {
  // Bun has no DOM for DOMPurify, so use the same sanitizer seam while also
  // pinning that the production helper cannot bypass sanitization.
  const sanitize = DOMPurify.sanitize;
  const calls = [];
  DOMPurify.sanitize = (html) => {
    calls.push(html);
    return html;
  };
  try {
    renderMarkdownWithCaret('hello');
    expect(calls).toHaveLength(1);
    expect(calls[0]).toContain('doc-cursor');
  } finally {
    DOMPurify.sanitize = sanitize;
  }
});
