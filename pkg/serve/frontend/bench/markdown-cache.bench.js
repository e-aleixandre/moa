import DOMPurify from 'dompurify';
import { parseMarkdown, renderMarkdown } from '../src/data/util/markdown.js';

const turns = Array.from({ length: 50 }, (_, i) => (
  `## Turn ${i + 1}\n\n`
  + 'The transcript keeps this completed response stable while a later response streams. '
  + '`const count = items.length`\n\n'
  + '- preserves markdown\n- sanitizes generated HTML\n'
));
const frames = 60;
const sourceSizes = turns.map(text => text.length).sort((a, b) => a - b);
const percentile = (p) => sourceSizes[Math.floor((sourceSizes.length - 1) * p)];

function measure() {
  let calls = 0;
  const sanitize = DOMPurify.sanitize;
  DOMPurify.sanitize = html => { calls++; return html; };
  const run = (render) => {
    const started = performance.now();
    for (let frame = 0; frame < frames; frame++) {
      for (const turn of turns) render(turn);
    }
    return { calls, elapsed: performance.now() - started };
  };
  try {
    const before = run(text => DOMPurify.sanitize(parseMarkdown(text)));
    calls = 0;
    for (const turn of turns) renderMarkdown(turn);
    calls = 0;
    const after = run(renderMarkdown);
    return { before, after };
  } finally {
    DOMPurify.sanitize = sanitize;
  }
}

const result = measure();
console.log(`corpus: ${turns.length} turns; source median ${percentile(0.5)} chars, p95 ${percentile(0.95)} chars`);
console.log(`without cache: ${result.before.calls} parses/sanitizes in ${result.before.elapsed.toFixed(1)} ms`);
console.log(`warm cache: ${result.after.calls} parses/sanitizes in ${result.after.elapsed.toFixed(1)} ms`);
