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

// Customize renderer: wrap code blocks for CodeBlock component
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

// Wrap GFM tables in a horizontally-scrollable container so wide tables scroll
// sideways on narrow screens (mobile) instead of squashing their columns into
// tall, unreadable wrapped cells. marked emits well-formed, non-nested
// <table>…</table>, so a plain string wrap is safe here (and the wrapper div +
// class are already on the DOMPurify allow-list below).
export function wrapTables(html) {
  return html
    .replace(/<table>/g, '<div class="md-table-wrap"><table>')
    .replace(/<\/table>/g, '</table></div>');
}

export function renderMarkdown(text) {
  const raw = wrapTables(marked.parse(text));
  return DOMPurify.sanitize(raw, {
    ADD_TAGS: ['div', 'button', 'span'],
    ADD_ATTR: ['class', 'data-lang'],
  });
}
