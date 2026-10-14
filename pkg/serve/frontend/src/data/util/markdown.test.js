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
    const caret = '<span class="zl-caret"></span>';
    expect(html).toContain('zl-caret');
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
    expect(calls[0]).toContain('zl-caret');
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

// A session id in inline code is tagged so the transcript can mount a chip on
// it; the tag is text-only, so cached HTML never goes stale with the roster.
const SESSION_ID = '5d18914e4e62ac2206e1d77f';

test('a session id in inline code is tagged as a session reference', () => {
  const html = parseMarkdown(`falta elegir en \`${SESSION_ID}\`.`);
  expect(html).toContain(`<code class="session-ref">${SESSION_ID}</code>`);
});

test('inline code that is not exactly a session id stays plain code', () => {
  for (const text of [SESSION_ID.slice(0, 23), `${SESSION_ID}0`, SESSION_ID.toUpperCase(), `id ${SESSION_ID}`]) {
    expect(parseMarkdown(`\`${text}\``)).not.toContain('session-ref');
  }
});

test('a session id in prose or inside a fenced code block is not tagged', () => {
  expect(parseMarkdown(`sesión ${SESSION_ID} suelta`)).not.toContain('session-ref');
  const html = parseMarkdown(`\`\`\`\n${SESSION_ID}\n\`\`\`\n`);
  expect(html).toContain(SESSION_ID);
  expect(html).not.toContain('session-ref');
});

// --- user messages ---------------------------------------------------------
//
// Only fenced code, `code`, **bold** and links render; everything else stays
// as typed. The battery is pasted text of the kind that real messages carry.

import { parseUserMarkdown, renderUserMarkdown } from './markdown.js';

// What a reader sees outside code blocks: tags dropped, entities decoded.
function visible(html) {
  return html
    .replace(/<div class="code-block[\s\S]*?<\/pre>\s*<\/div>/g, '')
    .replace(/<[^>]+>/g, '')
    .replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&quot;/g, '"')
    .replace(/&#39;/g, "'").replace(/&amp;/g, '&');
}

const PASTED = [
  'cp /tmp/*/logs/* /backup',
  '{"a": "_foo_", "b": "*x*", "c": "snake_case_name"}',
  '404.\n500.\nfoo',
  '1. one\n2. two\n- a\n- b\n* c',
  '# title\n## two\ntitle\n=====\n---\n***',
  '> quoted\n> more',
  '| a | b |\n|---|---|\n| 1 | 2 |',
  '2026-10-14  ERROR   <foo>  a < b &amp; c &lt;tag&gt;',
  '\\*not em\\* and C:\\path\\new',
  '    indented\n\tx\n\n\n\ntrailing  \nlines',
  '![x](http://t.example/p.png) <img src=x onerror=alert(1)> <script>x</script>',
  '[ref]: http://x\n[a][ref] [b] footnote[^1]',
  '~~strike~~ ~one~ &copy; &#35; &nbsp;',
  'a  \nb\\\nc',
  '$ ls *.go 2 * 3 * 4 _x_ _',
  'class A:\n    def __init__(self): pass\nif __name__ == "__main__": print(a.__dict__)',
  '__init__ __main__ __dict__',
  '  at Foo._bar_ (file.js:1:2)\n  at *anonymous* <anonymous>\n\n\nERROR: _id_ = 5',
];

// Known limits, accepted by the owner:
// - `**debug**` inside pasted JSON renders as bold; unavoidable if bold renders.
// - Selecting and copying bold text yields the rendered text, as in any chat;
//   only the code blocks' Copy button gives the original source.
// Known limit, accepted: a glob with two `**` such as `a/**/b y c/**/d` reads
// as bold ("/b y c/") and loses those asterisks; bold and literal `**` cannot
// both hold, so it is not in the battery.
test('pasted text keeps every visible character', () => {
  for (const text of PASTED) {
    expect(visible(parseUserMarkdown(text))).toBe(text);
  }
});

test('the sanitized render keeps them too', () => {
  const prev = DOMPurify.sanitize;
  DOMPurify.sanitize = (html) => html;
  try {
    for (const text of PASTED) expect(visible(renderUserMarkdown(text))).toBe(text);
  } finally {
    DOMPurify.sanitize = prev;
  }
});

test('user markdown renders fenced code, inline code, bold and links only', () => {
  const html = parseUserMarkdown('**b** and `d`\n\n[l](https://e.com) <https://a.com> https://b.com\n\n```\nx := 1 * 2\n```\n');
  expect(html).toContain('<strong>b</strong>');
  expect(html).toContain('<code>d</code>');
  expect(html).toContain('<a href="https://e.com">l</a>');
  expect(html).toContain('<a href="https://a.com">https://a.com</a>');
  expect(html).toContain('<a href="https://b.com">https://b.com</a>');
  expect(html).toContain('class="code-block');
  expect(html).toContain('code-block-copy');
  expect(html).toContain('x := 1 * 2');
});

test('italics, lists, headings, quotes, tables and images do not render', () => {
  const html = parseUserMarkdown('*a* _b_\n\n- x\n\n1. y\n\n# h\n\n> q\n\n| a |\n|---|\n| 1 |\n\n![i](http://t/p.png)');
  expect(html).not.toMatch(/<(em|ul|ol|li|h\d|blockquote|table|img)\b/);
});

test('a large user message is cached, not re-parsed on every render', () => {
  const sanitize = DOMPurify.sanitize;
  let calls = 0;
  DOMPurify.sanitize = (html) => {
    calls++;
    return html;
  };
  try {
    const text = `${crypto.randomUUID()} ` + 'line of pasted log\n'.repeat(1500);
    expect(text.length).toBeGreaterThan(16 * 1024);
    renderUserMarkdown(text);
    renderUserMarkdown(text);
    expect(calls).toBe(1);
  } finally {
    DOMPurify.sanitize = sanitize;
  }
});

test('assistant markdown is unchanged by the user mode', () => {
  expect(parseMarkdown('# h')).toContain('<h1');
  expect(parseMarkdown('<b>x</b>')).toContain('<b>x</b>');
  expect(parseMarkdown('*a*')).toContain('<em>a</em>');
});
