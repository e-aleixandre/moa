// markdown.test.js — run with `bun test`
//
// DOMPurify needs a DOM, which bun test doesn't provide, so these tests exercise
// the real marked pipeline (parseMarkdown) up to — but not including —
// sanitization. That's the part we customize (the table-wrapping renderer).
import { test, expect } from 'bun:test';
import DOMPurify from 'dompurify';
import { parseMarkdown, renderMarkdown, renderMarkdownWithCaret } from './markdown.js';

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

test('renderMarkdown reuses the sanitized HTML for identical transcript text', () => {
  const sanitize = DOMPurify.sanitize;
  let calls = 0;
  DOMPurify.sanitize = (html) => {
    calls++;
    return html;
  };
  try {
    const text = `cache-test-${crypto.randomUUID()}\n\n**stable transcript block**`;
    expect(renderMarkdown(text)).toBe(renderMarkdown(text));
    expect(calls).toBe(1);
  } finally {
    DOMPurify.sanitize = sanitize;
  }
});

test('the cache holds a whole long transcript, so a streaming frame re-renders nothing', () => {
  // Every streaming frame re-renders the ENTIRE mounted transcript. A cache
  // smaller than the mounted block count therefore evicts itself once per
  // frame and degrades to a 0% hit rate — measured at 505 misses per delta and
  // ~1200ms/frame on 4x-throttled mobile with a 500-turn session. This pins
  // the invariant that matters: a transcript far longer than anything the
  // capacity was sized for must still cost zero renders on the second frame.
  const blocks = Array.from({ length: 600 }, (_, i) => (
    `## Turn ${i} ${crypto.randomUUID()}\n\n`
    + 'The transcript keeps this completed response stable while a later response streams.\n\n'
    + '- the ledger rows fuse with the diff sibling that follows them\n'
    + '- the hydration anchor is captured before the snapshot swap\n'
  ));
  const sanitize = DOMPurify.sanitize;
  let calls = 0;
  DOMPurify.sanitize = (html) => {
    calls++;
    return html;
  };
  try {
    for (const block of blocks) renderMarkdown(block); // frame 1: cold
    calls = 0;
    for (const block of blocks) renderMarkdown(block); // frame 2: must be free
    expect(calls).toBe(0);
  } finally {
    DOMPurify.sanitize = sanitize;
  }
});

test('large fenced code skips synchronous highlighting', () => {
  const source = `\`\`\`javascript\n${'const x = 1;\n'.repeat(6000)}\`\`\``;
  const html = parseMarkdown(source);
  expect(html).toContain('const x = 1;');
  expect(html).not.toContain('hljs-keyword');
});
