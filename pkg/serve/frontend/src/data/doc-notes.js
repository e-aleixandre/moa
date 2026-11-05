// doc-notes.js — notes taken while reading an artifact, both ways:
// docNotesMessage WRITES the one user message a batch of notes becomes, and
// parseDocNotes READS it back so the transcript can paint each note as a quote
// with its reply. Same contract as preview-reference.js: the agent receives
// plain, unambiguous text; only the painting changes, and old messages render
// the same way as new ones because the text itself is parsed.
//
// Everything here is DOM-free so it can be tested without a browser.

export const DOC_NOTES_MARKER = '[Document notes';
export const DRAFT_PREFIX = 'moa.docnotes.v1.';
const QUOTE_MAX = 600;

export const BLOCK_SELECTOR = 'li, p, h1, h2, h3, h4, h5, h6, blockquote, pre, tr, figure, dt, dd';
export const INTERACTIVE_SELECTOR = 'a, button, input, select, textarea, summary, label, [role="button"], .dn-quick';

export const QUICK_REPLIES = ['Yes', 'No', 'Your call'];
// "Your call" must reach the agent as an instruction, not as a bare phrase.
const YOUR_CALL = 'Your call';
const YOUR_CALL_MEANING = 'Your call — go with your recommendation.';

export function draftKey(sessionId, fileId) {
  return `${DRAFT_PREFIX}${sessionId}.${fileId}`;
}

export function loadNotes(sessionId, fileId, storage = globalThis.localStorage) {
  try {
    const raw = storage.getItem(draftKey(sessionId, fileId));
    const parsed = raw ? JSON.parse(raw) : [];
    return Array.isArray(parsed) ? parsed : [];
  } catch (_) {
    return [];
  }
}

export function saveNotes(sessionId, fileId, notes, storage = globalThis.localStorage) {
  try {
    if (!notes.length) storage.removeItem(draftKey(sessionId, fileId));
    // The full-size crop of an area note stays in memory: a draft keeps only
    // its thumbnail and rectangle, so ten of them cannot fill the storage.
    else storage.setItem(draftKey(sessionId, fileId), JSON.stringify(notes.map(({ crop, ...rest }) => rest)));
  } catch (_) { /* a full storage loses the draft, never the send */ }
}

export function clip(text, max = QUOTE_MAX) {
  const clean = String(text || '').replace(/\s+/g, ' ').trim();
  if (clean.length <= max) return clean;
  const head = Math.floor(max * 0.7);
  return `${clean.slice(0, head).trim()} … ${clean.slice(clean.length - (max - head - 3)).trim()}`;
}

// questionMarker — F3: a paragraph that opens with a numbered marker in bold
// ("**P3.**") and asks something gets quick replies. `lead` is the text of
// that bold opening, `text` the whole paragraph.
export const QUESTION_LEAD = /^(P[\w-]*\d+)\.?$/;
export function questionMarker(lead, text) {
  const match = String(lead || '').trim().match(QUESTION_LEAD);
  if (!match || !String(text || '').includes('?')) return null;
  return match[1];
}

const norm = (text) => String(text || '').replace(/\s+/g, ' ').trim();

// resolveAnchors — where each text note lands in the document as it is NOW.
// A draft stores the block index it was taken on, but a republished document
// shifts the indices: the quote is the truth, the index only the first guess.
// Notes whose quote no longer exists are left out (they stay in the tray).
export function resolveAnchors(blockTexts, notes) {
  const blocks = blockTexts.map(norm);
  const out = new Map();
  // A selection can run across blocks, so a quote may sit in a few contiguous
  // ones; text from `start` on, joined, is where it is looked for.
  const SPAN = 8;
  const startsAt = (start, quote) => {
    let text = '';
    for (let i = start; i < blocks.length && i < start + SPAN; i += 1) {
      text = text ? `${text} ${blocks[i]}` : blocks[i];
      // The quote must BEGIN in the first block; one that begins in a later
      // block belongs to that block, not to this start.
      const at = text.indexOf(quote);
      if (at >= 0) return at < blocks[start].length;
    }
    return false;
  };
  for (const note of notes) {
    const quote = norm(note.quote);
    if (note.anchor == null || !quote) continue;
    let hit = startsAt(note.anchor, quote) ? note.anchor : -1;
    for (let i = 0; hit < 0 && i < blocks.length; i += 1) if (startsAt(i, quote)) hit = i;
    if (hit >= 0) out.set(note.id, hit);
  }
  return out;
}

// A comment line that looks like the format must not be read as the format.
// Backslashes in front of a reserved prefix are escaped too, so a literal
// "\## x" survives the trip.
const escapeLine = (line) => (/^\\*(## |> )/.test(line) ? `\\${line}` : line);
const unescapeLine = (line) => (/^\\+(## |> )/.test(line) ? line.slice(1) : line);

// docNotesMessage — the message a batch becomes. Area notes (F2) carry their
// crop as an image attachment; the note names which one, in order.
export function docNotesMessage({ name, url }, notes) {
  const attachments = [];
  const parts = notes.map((note, index) => {
    let where = note.where || '';
    if (note.kind === 'area' && note.crop) {
      attachments.push({
        name: `${baseName(name)}-note-${index + 1}.jpg`,
        mime: 'image/jpeg',
        data: note.crop.replace(/^data:[^,]+,/, ''),
        isImage: true,
      });
      where = [where || 'area', `image ${attachments.length}`].join(' · ');
    }
    const lines = [`## ${index + 1}${where ? ` · ${where}` : ''}`];
    if (note.quote) lines.push(`> ${clip(note.quote)}`);
    const comment = String(note.comment || '').trim();
    lines.push(comment ? (comment === YOUR_CALL ? YOUR_CALL_MEANING : comment).split('\n').map(escapeLine).join('\n') : '(no comment)');
    return lines.join('\n');
  });
  const head = `${DOC_NOTES_MARKER} · ${name} · ${notes.length} ${notes.length === 1 ? 'note' : 'notes'}]`;
  return { text: [head, `file: ${url}`, '', parts.join('\n\n')].join('\n'), attachments };
}

function baseName(name) {
  return String(name || 'document').replace(/\.[^.]+$/, '').replace(/[^\w.-]+/g, '-').slice(0, 40);
}

// parseDocNotes — the transcript's reading of that message. Returns null for
// any other message, so the waypoint keeps rendering it as it always did.
export function parseDocNotes(text) {
  if (typeof text !== 'string' || !text.startsWith(DOC_NOTES_MARKER)) return null;
  const lines = text.split('\n');
  const headMatch = lines[0].match(/^\[Document notes · (.+) · (\d+) notes?\]$/);
  if (!headMatch) return null;
  const fileLine = lines.find((line) => line.startsWith('file: ')) || '';
  const notes = [];
  let current = null;
  for (const line of lines.slice(1)) {
    const heading = line.match(/^## (\d+)(?: · (.*))?$/);
    if (heading) {
      current = { n: Number(heading[1]), where: heading[2] || '', quote: '', comment: [] };
      notes.push(current);
      continue;
    }
    if (!current) continue;
    if (line.startsWith('> ') && !current.comment.length && !current.quote) current.quote = line.slice(2);
    else current.comment.push(unescapeLine(line));
  }
  return {
    name: headMatch[1],
    url: fileLine.slice('file: '.length).trim(),
    notes: notes.map((note) => ({ ...note, comment: note.comment.join('\n').trim() })),
  };
}

// textInRect — the text of a PDF page that falls inside a box, for the note
// of a dragged area. `items` are pdf.js text items; `toViewport` maps a point
// of the page's user space to the viewport the `rect` is measured in. An item
// belongs to the box when its centre does: a word the box only grazes is not
// what the reader pointed at. Reading order: lines top to bottom, each left to
// right; a scanned page has no items and gives "".
export function textInRect(items, toViewport, rect) {
  const inside = [];
  for (const it of items || []) {
    if (!it || typeof it.str !== 'string' || !it.str.trim() || !Array.isArray(it.transform)) continue;
    const [x, y] = [it.transform[4], it.transform[5]];
    const height = it.height || Math.abs(it.transform[3]) || 0;
    const [x1, y1] = toViewport(x, y);
    const [x2, y2] = toViewport(x + (it.width || 0), y + height);
    const left = Math.min(x1, x2);
    const right = Math.max(x1, x2);
    const top = Math.min(y1, y2);
    const bottom = Math.max(y1, y2);
    const cx = (left + right) / 2;
    const cy = (top + bottom) / 2;
    if (cx >= rect.left && cx <= rect.right && cy >= rect.top && cy <= rect.bottom) {
      inside.push({ str: it.str.trim(), left, cy, h: bottom - top });
    }
  }
  inside.sort((a, b) => a.cy - b.cy);
  const lines = [];
  for (const it of inside) {
    const line = lines[lines.length - 1];
    if (line && Math.abs(it.cy - line.cy) < Math.max(line.h, it.h) / 2) line.items.push(it);
    else lines.push({ cy: it.cy, h: it.h, items: [it] });
  }
  return lines
    .map((line) => line.items.sort((a, b) => a.left - b.left).map((it) => it.str).join(' '))
    .join('\n');
}
