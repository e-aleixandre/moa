// Tools whose arguments are TEXT SENT TO ANOTHER AGENT (a subagent, a session,
// another owner). The ledger row used to show only the tool's result ("Message
// queued"), so the one thing worth knowing — what was said — was invisible.
// The text is already in the call's arguments; this only names where it went
// and what it said, for the row (preview) and its detail (the whole message).
import { stripDecoration } from './owner-message.js';

function parseArgs(args) {
  if (args && typeof args === 'object') return args;
  if (typeof args !== 'string') return null;
  try {
    const parsed = JSON.parse(args);
    return parsed && typeof parsed === 'object' ? parsed : null;
  } catch {
    return null;
  }
}

// Same head the session chip falls back to (SessionChip's shortId).
const shortId = (id) => String(id || '').slice(0, 8);
const str = (value) => (typeof value === 'string' ? value : '');
const present = (text) => text.trim() !== '';

function titled(a) {
  const title = str(a.title).trim();
  const description = str(a.description).trim();
  if (title && description) return `**${title}**\n\n${description}`;
  return title ? `**${title}**` : description;
}

// toolMessage returns { target, text } for a call that sends text, or null for
// every other call (a list, a status check, a task without a body).
export function toolMessage(name, args) {
  const a = parseArgs(args);
  if (!a) return null;
  let target;
  let text;
  switch ((name || '').toLowerCase()) {
    case 'subagent_steer':
      target = shortId(a.job_id);
      text = str(a.message);
      break;
    case 'subagent':
      target = str(a.resume) ? shortId(a.resume) : 'new subagent';
      text = str(a.task);
      break;
    case 'sessions':
      if (a.action === 'send' || a.action === 'new') {
        target = a.action === 'new' ? 'new session' : shortId(a.session_id);
        text = str(a.text);
      } else if (a.action === 'answer') {
        target = shortId(a.session_id);
        text = Array.isArray(a.answers) ? a.answers.map((x, i) => `${i + 1}. ${x}`).join('\n') : '';
      } else {
        return null;
      }
      break;
    case 'tasks':
      if (a.action === 'comment') {
        target = `#${a.id}`;
        text = str(a.body);
      } else if (a.action === 'done') {
        target = `#${a.id}`;
        text = str(a.note);
      } else if (a.action === 'send') {
        target = str(a.to) || 'owner';
        text = titled(a);
      } else if (a.action === 'ask') {
        target = 'owner';
        text = titled(a);
      } else {
        return null;
      }
      break;
    default:
      return null;
  }
  return present(text) ? { target, text } : null;
}

// The row's one line: where it went, then the first lines flattened (the cell
// ellipsises at its own width).
export function messagePreview({ target, text }) {
  return `→ ${target} · ${messageLines(text)}`;
}

// The first lines of a text, flattened to one.
export function messageLines(text) {
  return String(text).split('\n').map(stripDecoration).filter(Boolean).join(' ');
}

// What a subagent was told, as the dock names it: a fresh task is its own
// subject, a resumed one says which job it continues.
export function subagentTaskLine(message) {
  return message.target === 'new subagent' ? messageLines(message.text) : messagePreview(message);
}
