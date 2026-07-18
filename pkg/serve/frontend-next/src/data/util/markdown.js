import { Marked } from 'marked';
import DOMPurify from 'dompurify';
import hljs from 'highlight.js';

const marked = new Marked({
  breaks: true,
  gfm: true,
});

// Escape untrusted text before interpolating it into HTML attributes/markup.
// The code fence info-string (lang) is attacker-controllable when the assistant
// quotes file/web content, so it must never break out of the attribute.
function escapeHtml(s) {
  return s.replace(/[&<>"']/g, (c) => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
  }[c]));
}

// Customize renderer: wrap code blocks for CodeBlock component, and wrap GFM
// tables in a horizontal-scroll container.
const renderer = {
  code({ text, lang }) {
    let highlighted;
    if (lang && hljs.getLanguage(lang)) {
      highlighted = hljs.highlight(text, { language: lang }).value;
    } else {
      highlighted = hljs.highlightAuto(text).value;
    }
    const langLabel = escapeHtml(lang || '');
    const langClass = langLabel ? ` lang-${langLabel}` : '';
    return `<div class="code-block${langClass}" data-lang="${langLabel}">
      <div class="code-block-header">
        <span class="code-block-lang">${langLabel}</span>
        <button class="code-block-copy" type="button">Copy</button>
      </div>
      <pre><code class="hljs">${highlighted}</code></pre>
    </div>`;
  },
  // Wrap the default table rendering in a scrollable container so wide tables
  // scroll horizontally on narrow screens (mobile) instead of squashing their
  // columns into tall, unreadable wrapped cells. Rendering through marked's own
  // table token (rather than post-processing the HTML string) keeps cell
  // content correctly escaped and can't mismatch on raw-HTML tables.
  table(token) {
    let header = '';
    let cell = '';
    for (let j = 0; j < token.header.length; j++) {
      cell += this.tablecell(token.header[j]);
    }
    header += this.tablerow({ text: cell });
    let body = '';
    for (let j = 0; j < token.rows.length; j++) {
      const row = token.rows[j];
      cell = '';
      for (let k = 0; k < row.length; k++) {
        cell += this.tablecell(row[k]);
      }
      body += this.tablerow({ text: cell });
    }
    if (body) body = `<tbody>${body}</tbody>`;
    return '<div class="md-table-wrap"><table>\n<thead>\n' + header + '</thead>\n' + body + '</table></div>\n';
  },
};

marked.use({ renderer });

// Copy handler via event delegation, so code blocks carry no inline onclick —
// that let DOMPurify's ADD_ATTR whitelist onclick on *any* element, turning
// quoted assistant HTML into an XSS sink.
function copyCode(btn) {
  const pre = btn.closest('.code-block')?.querySelector('pre code');
  if (!pre) return;
  navigator.clipboard.writeText(pre.textContent).then(() => {
    btn.textContent = 'Copied!';
    btn.classList.add('copied');
    setTimeout(() => {
      btn.textContent = 'Copy';
      btn.classList.remove('copied');
    }, 2000);
  });
}

if (typeof document !== 'undefined') {
  document.addEventListener('click', (e) => {
    const btn = e.target.closest?.('.code-block-copy');
    if (btn) copyCode(btn);
  });
}

// parseMarkdown runs the configured marked pipeline (with our custom code/table
// renderers) WITHOUT sanitization. Exposed so tests can exercise the real
// renderer; production always goes through renderMarkdown, which sanitizes.
export function parseMarkdown(text) {
  return marked.parse(text);
}

export function renderMarkdown(text) {
  const raw = marked.parse(text);
  return DOMPurify.sanitize(raw, {
    ADD_TAGS: ['div', 'button', 'span'],
    ADD_ATTR: ['class', 'data-lang'],
  });
}
