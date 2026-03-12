import { Marked } from 'marked';
import DOMPurify from 'dompurify';
import hljs from 'highlight.js';

const marked = new Marked({
  breaks: true,
  gfm: true,
});

// Customize renderer: wrap code blocks for CodeBlock component
const renderer = {
  code({ text, lang }) {
    let highlighted;
    if (lang && hljs.getLanguage(lang)) {
      highlighted = hljs.highlight(text, { language: lang }).value;
    } else {
      highlighted = hljs.highlightAuto(text).value;
    }
    const langLabel = lang || '';
    const langClass = langLabel ? ` lang-${langLabel}` : '';
    return `<div class="code-block${langClass}" data-lang="${langLabel}">
      <div class="code-block-header">
        <span class="code-block-lang">${langLabel}</span>
        <button class="code-block-copy" onclick="window.__copyCode(this)">Copy</button>
      </div>
      <pre><code class="hljs">${highlighted}</code></pre>
    </div>`;
  },
};

marked.use({ renderer });

// Global copy handler
window.__copyCode = function(btn) {
  const pre = btn.closest('.code-block').querySelector('pre code');
  if (!pre) return;
  navigator.clipboard.writeText(pre.textContent).then(() => {
    btn.textContent = 'Copied!';
    btn.classList.add('copied');
    setTimeout(() => {
      btn.textContent = 'Copy';
      btn.classList.remove('copied');
    }, 2000);
  });
};

export function renderMarkdown(text) {
  const raw = marked.parse(text);
  return DOMPurify.sanitize(raw, {
    ADD_TAGS: ['div', 'button', 'span'],
    ADD_ATTR: ['class', 'data-lang', 'onclick'],
  });
}
