// mentions.js — @owner / @session mentions, both ways: serializeMentions WRITES
// the readable block into a message when it is sent, parseMentions READS it
// back so the transcript and the rewind can paint it.
//
// Same pattern as preview-reference.js: the reference travels INSIDE the text,
// under a marker, so the model gets an unambiguous handle (type, id and the
// tool to act with) and messages already in the history keep rendering as they
// always did. The block is NOT an authorization: sessions the user may have the
// agent read travel in the request's `session_mentions`, decided by the server.
//
// A mention in the composer is `{ label, type, id, meta }`:
//   type  'owner' | 'session'
//   label what is written after the @ (owner name / session title)
//   meta  owner: { root }  session: { title, state }

export const MENTIONS_MARKER = '[Mentions · moa]';
export const MENTION_LABEL_MAX = 60;
const SHORT_ID = 6;

// cleanLabel is the text written after the "@": one line, at most 60 chars.
export function cleanLabel(text) {
  return String(text ?? '').replace(/\s+/g, ' ').trim().slice(0, MENTION_LABEL_MAX).trim();
}

// uniqueLabel picks the label for a new mention. Two different targets with
// the same name get the id's first characters appended, so the text still says
// which is which. The same target reuses its label.
export function uniqueLabel(base, id, existing = []) {
  const label = cleanLabel(base) || 'untitled';
  const clash = existing.some((m) => m.label === label && m.id !== id);
  if (!clash) return label;
  const suffix = ` #${String(id).slice(0, SHORT_ID)}`;
  return cleanLabel(label.slice(0, MENTION_LABEL_MAX - suffix.length) + suffix);
}

// maskCode blanks fenced blocks and `inline code` (same length, so indexes
// still line up): a mention written inside code is code, not a mention.
export function maskCode(text) {
  const blank = (s) => s.replace(/[^\n]/g, ' ');
  return text
    .replace(/```[\s\S]*?(```|$)/g, blank)
    .replace(/`[^`\n]*`/g, blank);
}

const WORD = /[\p{L}\p{N}_]/u;

// mentionRanges finds where each mention is still written in `text`: a "@" at
// the start or after whitespace, the exact label, and no word character right
// after it. Longer labels win over their prefixes ("Infra 2" over "Infra").
// Returns non-overlapping [{ start, end, mention }] in text order.
export function mentionRanges(text, mentions) {
  if (!text || !mentions?.length) return [];
  const masked = maskCode(text);
  const found = [];
  for (const mention of mentions) {
    if (!mention?.label) continue;
    const needle = `@${mention.label}`;
    for (let from = masked.indexOf(needle); from >= 0; from = masked.indexOf(needle, from + 1)) {
      const end = from + needle.length;
      if (from > 0 && !/\s/.test(masked[from - 1])) continue;
      if (end < masked.length && WORD.test(masked[end])) continue;
      found.push({ start: from, end, mention });
    }
  }
  found.sort((a, b) => a.start - b.start || b.end - a.end);
  const out = [];
  let last = 0;
  for (const range of found) {
    if (range.start < last) continue;
    out.push(range);
    last = range.end;
  }
  return out;
}

// liveMentions are the draft's mentions whose @label is still in the text, once
// per label, in the order they appear. A hand-edited label stops matching and drops
// out here.
export function liveMentions(text, mentions) {
  const seen = new Set();
  const out = [];
  for (const { mention } of mentionRanges(text, mentions)) {
    if (seen.has(mention.label)) continue;
    seen.add(mention.label);
    out.push(mention);
  }
  return out;
}

// backspaceMention: Backspace with the caret right after a complete mention
// removes the whole mention. Returns { value, cursor } or null when the key
// should behave normally.
export function backspaceMention(text, caret, mentions) {
  const hit = mentionRanges(text, mentions).find((r) => r.end === caret);
  if (!hit) return null;
  return { value: text.slice(0, hit.start) + text.slice(hit.end), cursor: hit.start };
}

function baseName(path) {
  const clean = String(path || '').replace(/[\\/]+$/, '');
  return clean.slice(Math.max(clean.lastIndexOf('/'), clean.lastIndexOf('\\')) + 1);
}

function oneLine(value) {
  return String(value ?? '').replace(/\s+/g, ' ').trim();
}

function line(mention, { ownerConversation }) {
  const head = `@${mention.label} = ${mention.type}/${mention.id}`;
  const meta = mention.meta || {};
  if (mention.type === 'owner') {
    const root = oneLine(meta.root);
    const project = root ? `project ${baseName(root)} (${root})` : '';
    const hint = ownerConversation
      ? `send it tasks with tasks send to=${mention.id}`
      : "only the project's owner can send it tasks";
    return [head, project, hint].filter(Boolean).join(' · ');
  }
  const title = oneLine(meta.title) || mention.label;
  const state = oneLine(meta.state);
  const hint = ownerConversation
    ? `read it with sessions action=read session_id=${mention.id}`
    : `read it with read_session session_id=${mention.id}`;
  return [head, JSON.stringify(title), state, hint].filter(Boolean).join(' · ');
}

// mentionsBlock is what gets appended to a message: the marker and one line per
// mention still written in `text`, or '' when there is none. No mentions, no
// block. `ownerConversation` picks the tool the hints name (the sender's own).
export function mentionsBlock(text, mentions, { ownerConversation = false } = {}) {
  const live = liveMentions(text, mentions);
  if (live.length === 0) return '';
  return `\n\n${MENTIONS_MARKER}\n${live.map((m) => line(m, { ownerConversation })).join('\n')}`;
}

// serializeMentions returns the message text the agent receives: the comment
// plus its block. Without mentions the text goes out exactly as typed.
export function serializeMentions(text, mentions, opts) {
  const block = mentionsBlock(text, mentions, opts);
  return block ? text.replace(/\s+$/, '') + block : text;
}

// withLiveMeta refreshes what the block says about each target at send time:
// an owner's project root and a session's title and state are the roster's of
// that moment, not what the menu showed when it was picked (or nothing, for a
// mention restored from a rewind).
export function withLiveMeta(mentions, { owners = [], sessions = {} } = {}) {
  return (mentions || []).map((m) => {
    if (m.type === 'owner') {
      const owner = owners.find((o) => o.id === m.id);
      return owner?.root ? { ...m, meta: { ...m.meta, root: owner.root } } : m;
    }
    const sess = sessions[m.id];
    return sess
      ? { ...m, meta: { ...m.meta, title: sess.title || m.meta?.title, state: sess.state || m.meta?.state } }
      : m;
  });
}

// The session ids the user explicitly picked and still has written: what the
// request lists as `session_mentions` so the server can grant a read.
export function sessionMentionIds(text, mentions) {
  const ids = liveMentions(text, mentions).filter((m) => m.type === 'session').map((m) => m.id);
  return [...new Set(ids)];
}

const LINE_RE = /^@(.+?) = (owner|session)\/([A-Za-z0-9_-]+)(?: · (.*))?$/;

// parseMentions splits a message into its comment and the mentions its block
// lists: { comment, mentions: [{ label, type, id }] }. Returns null for an
// ordinary message (no trailing block), so callers render it as they always did.
// Only a block at the very end counts; the marker typed in the middle of a
// message is just text.
export function parseMentions(text) {
  if (typeof text !== 'string' || !text.includes(MENTIONS_MARKER)) return null;
  const lines = text.split('\n');
  let start = -1;
  for (let i = lines.length - 1; i >= 0; i--) {
    if (lines[i].trim() === MENTIONS_MARKER) { start = i; break; }
  }
  if (start < 0) return null;
  const mentions = [];
  for (const raw of lines.slice(start + 1)) {
    if (!raw.trim()) continue;
    const m = LINE_RE.exec(raw.trimEnd());
    if (!m) return null;
    mentions.push({ label: m[1], type: m[2], id: m[3] });
  }
  if (mentions.length === 0) return null;
  return { comment: lines.slice(0, start).join('\n').trim(), mentions };
}

// rehydrateMentions turns what a parsed block lists into the composer's draft
// mentions. Owners come back from the text (they grant nothing). A session only
// comes back when the server vouched for its id (`trustedSessionIds`): the text
// alone never reactivates a read.
export function rehydrateMentions(text, trustedSessionIds = []) {
  const parsed = parseMentions(text);
  if (!parsed) return { comment: text, mentions: [] };
  const trusted = new Set(trustedSessionIds || []);
  const mentions = parsed.mentions
    .filter((m) => m.type === 'owner' || trusted.has(m.id))
    .map((m) => ({ ...m, meta: {} }));
  return { comment: parsed.comment, mentions: liveMentions(parsed.comment, mentions) };
}

// plainMessageText is what a message reads as without its mentions block, for
// every place that shows or copies the text rather than painting pills.
export function plainMessageText(text) {
  const parsed = parseMentions(text);
  return parsed ? parsed.comment : text;
}
