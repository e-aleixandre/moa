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
const QUICK_MEANING = { 'Your call': 'Your call — go with your recommendation.' };

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
    else storage.setItem(draftKey(sessionId, fileId), JSON.stringify(notes));
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
  for (const note of notes) {
    const quote = norm(note.quote);
    if (note.anchor == null || !quote) continue;
    const probe = quote.includes(' … ') ? quote.split(' … ')[0] : quote;
    const hit = blocks[note.anchor]?.includes(probe) ? note.anchor : blocks.findIndex((b) => b.includes(probe));
    if (hit >= 0) out.set(note.id, hit);
  }
  return out;
}

// A comment line that looks like the format must not be read as the format.
const escapeLine = (line) => (/^(## |> )/.test(line) ? `\\${line}` : line);
const unescapeLine = (line) => (/^\\(## |> )/.test(line) ? line.slice(1) : line);

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
    lines.push(comment ? (QUICK_MEANING[comment] || comment).split('\n').map(escapeLine).join('\n') : '(no comment)');
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
