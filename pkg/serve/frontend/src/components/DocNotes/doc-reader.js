// doc-reader.js — wires the reader's own document (the Markdown article or the
// sandboxed HTML frame) to the notes hook. A tap on a block picks the whole
// block; a text selection (a long press on a phone) picks exactly the selected
// words; numbered questions get quick replies.
import { useEffect, useRef } from 'preact/hooks';
import {
  BLOCK_SELECTOR, INTERACTIVE_SELECTOR, QUICK_REPLIES, clip, questionMarker, resolveAnchors,
} from '../../data/doc-notes.js';
import { NOTES_MARKS, NOTES_PICK, NOTES_QUICK, NOTES_READY } from './notes-bridge.js';

function useLatest(value) {
  const ref = useRef(value);
  ref.current = value;
  return ref;
}

const clean = (text) => String(text || '').replace(/\s+/g, ' ').trim();

// What a tapped block reads as: a table row is its cells read across, and a
// list item is its own text, not the nested list under it.
function blockQuote(el) {
  if (el.tagName === 'TR') {
    return Array.from(el.children).map((cell) => clean(cell.textContent)).filter(Boolean).join(' | ');
  }
  if (el.querySelector('ul, ol')) {
    const copy = el.cloneNode(true);
    copy.querySelectorAll('ul, ol').forEach((list) => list.remove());
    return clean(copy.textContent);
  }
  return clean(el.textContent);
}

// The heading path above a block ("Preguntas abiertas › P3"): the nearest h2
// and, under it, the nearest h3. The agent's handle on the place.
function whereOf(root, el) {
  let h2 = '';
  let h3 = '';
  for (const heading of root.querySelectorAll('h1, h2, h3')) {
    if (heading === el) break;
    // eslint-disable-next-line no-bitwise
    if (!(heading.compareDocumentPosition(el) & Node.DOCUMENT_POSITION_FOLLOWING)) break;
    if (heading.tagName === 'H2') { h2 = clean(heading.textContent); h3 = ''; }
    else if (heading.tagName === 'H3') h3 = clean(heading.textContent);
  }
  return [h2, h3].filter(Boolean).join(' › ');
}

function pickOf(root, block) {
  return { kind: 'text', anchor: Number(block.dataset.dn), quote: blockQuote(block), where: whereOf(root, block) };
}

// The marks for the notes against the document as it is now.
// `resolved` places notes by their quote (Markdown, whose blocks the host can
// read); without it the block index the note was taken at is trusted (HTML,
// whose blocks live in the frame).
function marksOf(resolved, dn) {
  const noted = [];
  const sent = [];
  const quick = [];
  for (const note of dn.notes) {
    const index = resolved ? resolved.get(note.id) : note.anchor;
    if (index == null) continue;
    if (note.sentAt) { sent.push(index); continue; }
    noted.push(index);
    if (QUICK_REPLIES.includes(note.comment)) quick.push({ index, reply: note.comment });
  }
  const picked = dn.pick && dn.pick.anchor != null
    ? (dn.pick.id && resolved?.has(dn.pick.id) ? resolved.get(dn.pick.id) : dn.pick.anchor)
    : null;
  return { picked, noted, sent, quick };
}

export function useMarkdownNotes(articleRef, dn, html) {
  const dnRef = useLatest(dn);

  // Number the blocks and hang quick replies under numbered questions.
  useEffect(() => {
    const root = articleRef.current;
    if (!root || !html || !dnRef.current) return;
    root.querySelectorAll(BLOCK_SELECTOR).forEach((el, index) => { el.dataset.dn = String(index); });
    for (const p of root.querySelectorAll('p')) {
      const lead = p.firstElementChild;
      if (!lead || !['STRONG', 'B'].includes(lead.tagName) || !questionMarker(lead.textContent, p.textContent)) continue;
      if (p.nextElementSibling?.classList.contains('dn-quick-row')) continue;
      const row = document.createElement('div');
      row.className = 'dn-quick-row';
      row.dataset.for = p.dataset.dn;
      for (const reply of QUICK_REPLIES) {
        const button = document.createElement('button');
        button.type = 'button';
        button.className = 'dn-quick';
        button.textContent = reply;
        button.dataset.reply = reply;
        row.appendChild(button);
      }
      p.after(row);
    }
  }, [html]);

  // Tap → block; tap on a quick reply → that reply as the note.
  useEffect(() => {
    const root = articleRef.current;
    if (!root || !html) return undefined;
    const onClick = (event) => {
      const current = dnRef.current;
      if (!current) return;
      const quick = event.target.closest('.dn-quick');
      if (quick) {
        const block = root.querySelector(`[data-dn="${quick.parentElement.dataset.for}"]`);
        if (block) current.quick(pickOf(root, block), quick.dataset.reply);
        return;
      }
      if (event.target.closest(INTERACTIVE_SELECTOR)) return;
      const selection = window.getSelection();
      if (selection && !selection.isCollapsed && selection.toString().trim()) return;
      const block = event.target.closest('[data-dn]');
      if (!block || !root.contains(block)) return;
      current.choose(pickOf(root, block));
    };
    // A selection settles after the handles stop moving; the note follows it.
    let timer = null;
    const onSelection = () => {
      clearTimeout(timer);
      timer = setTimeout(() => {
        const current = dnRef.current;
        const selection = window.getSelection();
        if (!current || !selection || selection.isCollapsed) return;
        const text = clean(selection.toString());
        if (text.length < 2) return;
        const node = selection.anchorNode;
        const el = node?.nodeType === 1 ? node : node?.parentElement;
        const block = el?.closest?.('[data-dn]');
        if (!block || !root.contains(block)) return;
        current.choose({ ...pickOf(root, block), quote: text, precise: true });
      }, 350);
    };
    root.addEventListener('click', onClick);
    document.addEventListener('selectionchange', onSelection);
    return () => {
      clearTimeout(timer);
      root.removeEventListener('click', onClick);
      document.removeEventListener('selectionchange', onSelection);
    };
  }, [html]);

  // Marks follow the notes: the block being written, drafts, sent ones.
  useEffect(() => {
    const root = articleRef.current;
    if (!root || !html || !dn) return;
    const blocks = Array.from(root.querySelectorAll('[data-dn]'));
    blocks.forEach((el) => el.classList.remove('dn-picked', 'dn-noted', 'dn-sent'));
    root.querySelectorAll('.dn-quick.is-on').forEach((el) => el.classList.remove('is-on'));
    const resolved = resolveAnchors(blocks.map(blockQuote), dn.notes);
    // A republished document shifts the blocks: bring the drafts with it.
    dn.reanchor(resolved);
    const marks = marksOf(resolved, dn);
    const at = (index) => root.querySelector(`[data-dn="${index}"]`);
    marks.sent.forEach((i) => at(i)?.classList.add('dn-sent'));
    marks.noted.forEach((i) => at(i)?.classList.add('dn-noted'));
    marks.quick.forEach(({ index, reply }) => {
      for (const button of root.querySelectorAll(`.dn-quick-row[data-for="${index}"] .dn-quick`)) {
        if (button.dataset.reply === reply) button.classList.add('is-on');
      }
    });
    if (marks.picked != null) at(marks.picked)?.classList.add('dn-picked');
  }, [html, dn?.notes, dn?.pick]);
}

const text = (value, max) => (typeof value === 'string' ? clip(value, max) : '');

// The HTML reader is a sandboxed frame: the picking runs inside it (the bridge)
// and reaches the host by message. Only the frame's own window is believed, and
// each field is re-checked here: the document is the author's, not ours.
export function useHtmlNotes(frameRef, dn, active) {
  const dnRef = useLatest(dn);
  useEffect(() => {
    if (!active) return undefined;
    const onMessage = (event) => {
      const frame = frameRef.current;
      if (!frame || event.source !== frame.contentWindow) return;
      const data = event.data || {};
      const current = dnRef.current;
      if (data.type === NOTES_READY) { postMarks(frame, dnRef.current); return; }
      if (!current || !Number.isInteger(data.index) || data.index < 0) return;
      if (data.type === NOTES_PICK) {
        const quote = text(data.quote);
        if (!quote) return;
        current.choose({ kind: 'text', anchor: data.index, quote, where: text(data.where, 200), precise: data.precise === true });
      } else if (data.type === NOTES_QUICK && QUICK_REPLIES.includes(data.reply)) {
        const quote = text(data.quote);
        if (!quote) return;
        current.quick({ kind: 'text', anchor: data.index, quote, where: text(data.where, 200) }, data.reply);
      }
    };
    window.addEventListener('message', onMessage);
    return () => window.removeEventListener('message', onMessage);
  }, [active]);
  useEffect(() => {
    if (active && frameRef.current) postMarks(frameRef.current, dn);
  }, [active, dn?.notes, dn?.pick]);
}

function postMarks(frame, dn) {
  if (dn) frame.contentWindow?.postMessage({ type: NOTES_MARKS, ...marksOf(null, dn) }, '*');
}
