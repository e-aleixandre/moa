// preview-reference.js — the Live Preview element reference, both ways:
// feedbackMessage WRITES the block into a user message when you point at an
// element, parsePreviewReference READS it back so the transcript can paint it.
//
// What the agent receives is unchanged — it needs the full, unambiguous handle
// on the element. Only the painting changes. Parsing the message text, rather
// than carrying a structured field, is what makes messages already in the
// history render the same way as new ones.

export const PREVIEW_REFERENCE_MARKER = '[UI feedback · selected element in preview]';

// feedbackMessage — the text sent to the agent for a selected element. The
// block below the comment is machine-ish on purpose: the agent needs an
// unambiguous handle on the element, the user only writes the intent.
export function feedbackMessage(comment, el) {
  const classes = (el.classes || []).map((c) => `.${c}`).join('');
  const id = el.id ? `#${el.id}` : '';
  const text = el.text ? `  — text: ${JSON.stringify(el.text)}` : '';
  const attrs = Object.entries(el.attrs || {})
    .map(([k, v]) => `${k}=${JSON.stringify(String(v))}`)
    .join(' ');
  const lines = [
    PREVIEW_REFERENCE_MARKER,
    `page: ${el.path || '/'} (${el.url || ''})`,
    `element: ${el.tag}${id}${classes}${text}`,
  ];
  if (el.ancestors?.length) lines.push(`ancestors: ${el.ancestors.join(' > ')}`);
  if (el.selector) lines.push(`selector: ${el.selector}`);
  if (attrs) lines.push(`attrs: ${attrs}`);
  const body = lines.join('\n');
  return comment.trim() ? `${comment.trim()}\n\n${body}` : body;
}

// Generic wrappers say nothing about WHICH element you meant, so they never win
// as the disambiguator.
const GENERIC_CLASSES = new Set([
  'container', 'wrapper', 'wrap', 'row', 'col', 'column', 'content', 'inner',
  'main', 'box', 'grid', 'flex', 'section', 'item', 'block', 'group',
]);

function parseAttrs(line) {
  const attrs = {};
  const re = /([\w:-]+)="((?:[^"\\]|\\.)*)"/g;
  let m;
  while ((m = re.exec(line)) !== null) {
    try {
      attrs[m[1]] = JSON.parse(`"${m[2]}"`);
    } catch {
      attrs[m[1]] = m[2];
    }
  }
  return attrs;
}

// bestLabel picks the one class (or id) that identifies an ancestor: an id
// wins, then a BEM modifier (card--pro), then the last plain class. BEM
// elements (card__footer) lose to their own block, which is what a person
// would name out loud.
function bestLabel(ancestor) {
  const idMatch = ancestor.match(/#([\w-]+)/);
  if (idMatch) return `#${idMatch[1]}`;
  const classes = (ancestor.match(/\.[\w-]+/g) || []).map((c) => c.slice(1));
  const usable = classes.filter((c) => !c.includes('__') && !GENERIC_CLASSES.has(c.toLowerCase()));
  if (usable.length === 0) return '';
  const modifier = usable.find((c) => c.includes('--'));
  return modifier || usable[usable.length - 1];
}

// contextOf walks the ancestors from the innermost out and returns the first
// one that actually names something ("card--pro", "hero"). Empty when nothing
// around the element is distinctive — the reference then shows only the page.
function contextOf(ancestors) {
  for (let i = ancestors.length - 1; i >= 0; i--) {
    const label = bestLabel(ancestors[i]);
    if (label) return label;
  }
  return '';
}

function truncate(text, max = 64) {
  const clean = text.replace(/\s+/g, ' ').trim();
  return clean.length > max ? `${clean.slice(0, max - 1)}…` : clean;
}

// parsePreviewReference splits a user message into its comment and, when the
// feedback block is present, the reference to paint. Returns null for ordinary
// messages so callers can keep rendering them as they always did.
export function parsePreviewReference(text) {
  if (typeof text !== 'string' || !text.includes(PREVIEW_REFERENCE_MARKER)) return null;
  const lines = text.split('\n');
  const start = lines.findIndex((line) => line.trim() === PREVIEW_REFERENCE_MARKER);
  if (start < 0) return null;

  const comment = lines.slice(0, start).join('\n').trim();
  const fields = {};
  for (const line of lines.slice(start + 1)) {
    const sep = line.indexOf(': ');
    if (sep < 0) continue;
    const key = line.slice(0, sep).trim();
    if (key.includes(' ')) continue;
    fields[key] = line.slice(sep + 2).trim();
  }

  const pageLine = fields.page || '';
  const pageMatch = pageLine.match(/^(\S*)\s*(?:\((.*)\))?$/);
  const path = pageMatch?.[1] || '/';
  const url = pageMatch?.[2] || '';

  const elementLine = fields.element || '';
  const textSplit = elementLine.split(/\s+—\s+text:\s+/);
  const target = textSplit[0].trim();
  let visibleText = '';
  if (textSplit.length > 1) {
    try {
      visibleText = JSON.parse(textSplit[1]);
    } catch {
      visibleText = textSplit[1].replace(/^"|"$/g, '');
    }
  }

  const ancestors = fields.ancestors ? fields.ancestors.split(' > ').map((a) => a.trim()).filter(Boolean) : [];
  const attrs = fields.attrs ? parseAttrs(fields.attrs) : {};
  const tag = (target.match(/^[\w-]+/) || [''])[0];

  // What the reference reads as: the element's own words when it has any,
  // otherwise its accessible name behind a tag mark, so an <img> or an icon
  // button is still unambiguous.
  let quote = '';
  let tagMark = '';
  if (visibleText) {
    quote = `«${truncate(visibleText)}»`;
  } else {
    tagMark = tag || 'element';
    const named = attrs['aria-label'] || attrs.alt || attrs.title || attrs.placeholder || attrs.name || '';
    // No words at all: the element's own id/classes are the only handle left,
    // and the tag mark alone still says what you pointed at.
    quote = named ? truncate(named) : truncate(target.slice(tag.length));
  }

  const detailAttrs = Object.entries(attrs)
    .map(([key, value]) => `${key}=${value}`)
    .join(' · ');

  return {
    comment,
    reference: {
      quote,
      tagMark,
      context: contextOf(ancestors),
      path,
      url,
      target,
      selector: fields.selector || '',
      ancestors,
      attrs: detailAttrs,
    },
  };
}
