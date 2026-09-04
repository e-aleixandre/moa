import { expect, test } from 'bun:test';
import { parsePreviewReference, feedbackMessage } from './preview-reference.js';

// The exact block LivePreview's feedbackMessage writes. Kept verbatim so a
// change to that format breaks here rather than silently in the transcript.
const block = [
  '[UI feedback · selected element in preview]',
  'page: /pricing (http://host:5173/pricing)',
  'element: button#buy.btn.btn-primary — text: "Comprar"',
  'ancestors: section#pricing > div.card.card--pro > div.card__footer',
  'selector: #pricing .card--pro button#buy',
  'attrs: data-plan="pro" aria-label="Comprar el plan Pro"',
].join('\n');

test('a feedback message splits into the comment and the reference', () => {
  const parsed = parsePreviewReference(`Este botón es demasiado grande\n\n${block}`);

  expect(parsed.comment).toBe('Este botón es demasiado grande');
  expect(parsed.reference.quote).toBe('«Comprar»');
  expect(parsed.reference.tagMark).toBe('');
  expect(parsed.reference.path).toBe('/pricing');
  expect(parsed.reference.url).toBe('http://host:5173/pricing');
  expect(parsed.reference.target).toBe('button#buy.btn.btn-primary');
  expect(parsed.reference.selector).toBe('#pricing .card--pro button#buy');
  expect(parsed.reference.attrs).toContain('aria-label=Comprar el plan Pro');
});

test('the disambiguator is the nearest meaningful ancestor, not a BEM element', () => {
  const parsed = parsePreviewReference(block);

  expect(parsed.reference.context).toBe('card--pro');
});

test('generic wrappers never win as the disambiguator', () => {
  const parsed = parsePreviewReference([
    '[UI feedback · selected element in preview]',
    'page: / (http://host/)',
    'element: a.link — text: "Docs"',
    'ancestors: div.container > div.row > nav.site-nav > div.wrapper',
  ].join('\n'));

  expect(parsed.reference.context).toBe('site-nav');
});

test('an element with no ancestors worth naming shows only the page', () => {
  const parsed = parsePreviewReference([
    '[UI feedback · selected element in preview]',
    'page: /about (http://host/about)',
    'element: h1 — text: "Sobre nosotros"',
    'ancestors: div.container > div.wrap',
  ].join('\n'));

  expect(parsed.reference.context).toBe('');
  expect(parsed.reference.path).toBe('/about');
});

test('an element with no visible text falls back to its tag and accessible name', () => {
  const parsed = parsePreviewReference([
    '[UI feedback · selected element in preview]',
    'page: / (http://host/)',
    'element: img.hero-shot',
    'ancestors: main > section.hero > figure',
    'attrs: alt="Captura del dashboard"',
  ].join('\n'));

  expect(parsed.reference.tagMark).toBe('img');
  expect(parsed.reference.quote).toBe('Captura del dashboard');
  expect(parsed.reference.context).toBe('hero');
});

test('an element with neither text nor accessible name still names itself', () => {
  const parsed = parsePreviewReference([
    '[UI feedback · selected element in preview]',
    'page: / (http://host/)',
    'element: div#overlay.backdrop',
  ].join('\n'));

  expect(parsed.reference.tagMark).toBe('div');
  expect(parsed.reference.quote).toBe('#overlay.backdrop');
});

test('a comment-less feedback message keeps an empty comment', () => {
  expect(parsePreviewReference(block).comment).toBe('');
});

test('a long visible text is trimmed for the spine', () => {
  const long = 'x'.repeat(200);
  const parsed = parsePreviewReference([
    '[UI feedback · selected element in preview]',
    'page: / (http://host/)',
    `element: button — text: ${JSON.stringify(long)}`,
  ].join('\n'));

  expect(parsed.reference.quote.length).toBeLessThan(70);
  expect(parsed.reference.quote.endsWith('…»')).toBe(true);
});

test('an ordinary message is not a reference', () => {
  expect(parsePreviewReference('Just a message')).toBeNull();
  expect(parsePreviewReference('')).toBeNull();
  expect(parsePreviewReference(undefined)).toBeNull();
});

// The producer and the reader must not drift: this asserts against what
// LivePreview actually sends, not a hand-written copy of the format.
test('what LivePreview sends parses back into a reference', () => {
  const message = feedbackMessage('Este botón es demasiado grande', {
    tag: 'button',
    id: 'buy',
    classes: ['btn', 'btn-primary'],
    text: 'Comprar',
    path: '/pricing',
    url: 'http://host:5173/pricing',
    ancestors: ['section#pricing', 'div.card.card--pro', 'div.card__footer'],
    selector: '#pricing .card--pro button#buy',
    attrs: { 'data-plan': 'pro', 'aria-label': 'Comprar el plan Pro' },
  });
  const parsed = parsePreviewReference(message);

  expect(parsed.comment).toBe('Este botón es demasiado grande');
  expect(parsed.reference.quote).toBe('«Comprar»');
  expect(parsed.reference.context).toBe('card--pro');
  expect(parsed.reference.path).toBe('/pricing');
  expect(parsed.reference.target).toBe('button#buy.btn.btn-primary');
});

test('a bare element with no comment still parses what LivePreview sends', () => {
  const message = feedbackMessage('', {
    tag: 'img',
    classes: ['hero-shot'],
    path: '/',
    url: 'http://host/',
    ancestors: ['main', 'section.hero', 'figure'],
    attrs: { alt: 'Captura del dashboard' },
  });
  const parsed = parsePreviewReference(message);

  expect(parsed.comment).toBe('');
  expect(parsed.reference.tagMark).toBe('img');
  expect(parsed.reference.quote).toBe('Captura del dashboard');
  expect(parsed.reference.context).toBe('hero');
});
