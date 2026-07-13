// markdown.test.js — run with `bun test`
//
// DOMPurify needs a DOM, which bun test doesn't provide, so these tests exercise
// the marked pipeline up to (but not including) sanitization — enough to cover
// the table-wrapping renderer, which is the part we customize.
import { test, expect } from 'bun:test';
import { Marked } from 'marked';
import hljs from 'highlight.js';

// Rebuild the same marked instance renderMarkdown uses (same options + the table
// renderer under test). Kept in sync with markdown.js by construction.
function makeMarked() {
  const marked = new Marked({ breaks: true, gfm: true });
  marked.use({
    renderer: {
      code({ text, lang }) {
        const highlighted = lang && hljs.getLanguage(lang)
          ? hljs.highlight(text, { language: lang }).value
          : hljs.highlightAuto(text).value;
        return `<pre><code>${highlighted}</code></pre>`;
      },
      table(token) {
        let header = '';
        let cell = '';
        for (let j = 0; j < token.header.length; j++) cell += this.tablecell(token.header[j]);
        header += this.tablerow({ text: cell });
        let body = '';
        for (let j = 0; j < token.rows.length; j++) {
          const row = token.rows[j];
          cell = '';
          for (let k = 0; k < row.length; k++) cell += this.tablecell(row[k]);
          body += this.tablerow({ text: cell });
        }
        if (body) body = `<tbody>${body}</tbody>`;
        return '<div class="md-table-wrap"><table>\n<thead>\n' + header + '</thead>\n' + body + '</table></div>\n';
      },
    },
  });
  return marked;
}

const marked = makeMarked();

test('a GFM table is wrapped in a horizontal-scroll container', () => {
  const html = marked.parse('| a | b |\n|---|---|\n| 1 | 2 |\n');
  expect(html).toContain('<div class="md-table-wrap">');
  expect(html).toContain('<table>');
  expect(html).toContain('</table></div>');
  // Balanced: exactly one wrapper open and close.
  expect(html.match(/md-table-wrap/g).length).toBe(1);
  expect(html.match(/<\/table><\/div>/g).length).toBe(1);
});

test('every table is wrapped when several are present', () => {
  const md = '| a |\n|---|\n| 1 |\n\ntext\n\n| c |\n|---|\n| 2 |\n';
  const html = marked.parse(md);
  expect(html.match(/md-table-wrap/g).length).toBe(2);
  expect(html.match(/<\/table><\/div>/g).length).toBe(2);
});

test('literal table tags inside a cell are escaped, not treated as markup', () => {
  // The old string-replace approach would have mangled a cell containing the
  // literal text "</table>"; the token renderer escapes it instead.
  const html = marked.parse('| a |\n|---|\n| `</table>` |\n');
  expect(html).toContain('&lt;/table&gt;');
  // Still exactly one real wrapper (the cell text did not create a second).
  expect(html.match(/md-table-wrap/g).length).toBe(1);
  expect(html.match(/<\/table><\/div>/g).length).toBe(1);
});

test('non-table markdown is untouched', () => {
  const html = marked.parse('# Title\n\nplain paragraph\n');
  expect(html).not.toContain('md-table-wrap');
});
