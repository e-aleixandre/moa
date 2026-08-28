import { Marked } from 'marked';
import DOMPurify from 'dompurify';
import { copyToClipboard } from './format.js';
// Divergence from the ported engine: import highlight.js core +
// a selective language subset instead of the full package. The full import
// pulls ~984KB of languages into the bundle; core + this subset covers the
// languages moa's assistant emits most. This is a DELIBERATE subset (not the
// exact CodeBlock list — CodeBlock also registers html/rust/yaml/sql/diff/
// dockerfile); unregistered fences fall back to escaped plain text below, so
// they lose highlighting but never break or inject. Register once at module
// load. (Documented divergence: a highlight-related bugfix in the old SPA's
// markdown.js does NOT replicate mechanically here — the import differs.)
import hljs from 'highlight.js/lib/core';
import javascript from 'highlight.js/lib/languages/javascript';
import typescript from 'highlight.js/lib/languages/typescript';
import python from 'highlight.js/lib/languages/python';
import go from 'highlight.js/lib/languages/go';
import bash from 'highlight.js/lib/languages/bash';
import json from 'highlight.js/lib/languages/json';
import xml from 'highlight.js/lib/languages/xml';
import css from 'highlight.js/lib/languages/css';
import markdownLang from 'highlight.js/lib/languages/markdown';

hljs.registerLanguage('javascript', javascript);
hljs.registerLanguage('typescript', typescript);
hljs.registerLanguage('python', python);
hljs.registerLanguage('go', go);
hljs.registerLanguage('bash', bash);
hljs.registerLanguage('json', json);
hljs.registerLanguage('xml', xml);
hljs.registerLanguage('css', css);
hljs.registerLanguage('markdown', markdownLang);

const marked = new Marked({
  breaks: true,
  gfm: true,
});

// Highlighting is synchronous and can monopolize a mobile browser on a large
// tool result. Plain escaped code remains safe and readable without it.
const HIGHLIGHT_MAX_BYTES = 64 * 1024;

// The cache must hold EVERY prose block currently mounted, not a recent
// window: each streaming frame re-renders the whole transcript, so a cache
// smaller than the mounted block count degrades to a 0% hit rate and evicts
// itself once per frame. Measured on a 500-turn session paged back to 440
// blocks: a 300-entry cache produced 505 misses per streaming delta and
// ~1200ms/frame on 4x-throttled mobile; sized above the block count the same
// frame costs ~200ms with a 100% hit rate. Those 624 blocks hold 1.7MB of
// cached HTML, so the byte ceiling — not the entry count — is what actually
// bounds memory here.
const MARKDOWN_CACHE_MAX_ENTRIES = 2000;
const MARKDOWN_CACHE_MAX_SOURCE_BYTES = 16 * 1024;
const MARKDOWN_CACHE_MAX_BYTES = 8 * 1024 * 1024;
const markdownCache = new Map();
let markdownCacheBytes = 0;

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
    if (text.length <= HIGHLIGHT_MAX_BYTES && lang && hljs.getLanguage(lang)) {
      highlighted = hljs.highlight(text, { language: lang }).value;
    } else {
      // core build: no highlightAuto (it needs every language registered).
      // Fall back to escaped plain text for unknown/absent languages.
      highlighted = escapeHtml(text);
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
  copyToClipboard(pre.textContent).then((copied) => {
    if (!copied) return;
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

function renderAndSanitizeMarkdown(text) {
  const raw = marked.parse(text);
  return DOMPurify.sanitize(raw, {
    ADD_TAGS: ['div', 'button', 'span'],
    ADD_ATTR: ['class', 'data-lang'],
  });
}

export function renderMarkdown(text) {
  // Long tool results are unlikely to be revisited and would otherwise let
  // cached source plus HTML consume disproportionate memory.
  if (text.length > MARKDOWN_CACHE_MAX_SOURCE_BYTES) return renderAndSanitizeMarkdown(text);

  const cached = markdownCache.get(text);
  if (cached) {
    markdownCache.delete(text);
    markdownCache.set(text, cached);
    return cached.html;
  }

  const html = renderAndSanitizeMarkdown(text);
  const bytes = 2 * (text.length + html.length);
  if (bytes > MARKDOWN_CACHE_MAX_BYTES) return html;

  while (markdownCache.size >= MARKDOWN_CACHE_MAX_ENTRIES
    || markdownCacheBytes + bytes > MARKDOWN_CACHE_MAX_BYTES) {
    const oldest = markdownCache.entries().next().value;
    if (!oldest) break;
    markdownCache.delete(oldest[0]);
    markdownCacheBytes -= oldest[1].bytes;
  }
  markdownCache.set(text, { html, bytes });
  markdownCacheBytes += bytes;
  return html;
}

// Append the caret to the markdown source so marked keeps it in the final
// inline context instead of placing it after the prose block.
export function renderMarkdownWithCaret(text) {
  if (!text) return '<span class="doc-cursor"></span>';
  // The streaming source changes for every token, so caching it would evict
  // stable transcript entries without producing cache hits.
  return renderAndSanitizeMarkdown(`${text}<span class="doc-cursor"></span>`);
}
