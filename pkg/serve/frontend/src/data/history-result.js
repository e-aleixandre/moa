// history-result.js — reads back the text the `history` tool returned to the
// model (pkg/history) so its card can show what was retrieved: each message
// with who wrote it, when, and its text. The format is the tool's own, so the
// parse is exact; anything it does not recognise is kept as plain lines.

// [#12 id=abc 2026-10-13 08:07 author] text — author may hold a quoted owner
// name, which can contain "]".
const ITEM = /^(» |  )?\[#(\d+) id=(\S+?)(?: (\d{4}-\d{2}-\d{2} \d{2}:\d{2}))? ((?:[^"\]]|"(?:[^"\\]|\\.)*")*)\](?: \(from byte (\d+)\))?(?: (.*))?$/;

function parseItem(line) {
  const m = ITEM.exec(line);
  if (!m) return null;
  return {
    focus: m[1] === '» ',
    pos: Number(m[2]),
    id: m[3],
    date: m[4] || '',
    author: m[5],
    fromByte: m[6] ? Number(m[6]) : 0,
    text: m[7] || '',
  };
}

// parseHistoryResult returns { action, summary, items, notes } or null when
// the text is not a history result it can read.
export function parseHistoryResult(action, result) {
  if (!result) return null;
  const lines = String(result).replace(/\n$/, '').split('\n');
  if (action === 'read') {
    const head = parseItem(lines[0]);
    if (!head) return null;
    const rest = lines.slice(1);
    const notes = [];
    if (rest.length && /^\[\d+ of \d+ bytes shown; continue with offset=\d+\]$/.test(rest[rest.length - 1])) notes.push(rest.pop());
    head.text = rest.join('\n');
    return { action, summary: '', items: [head], notes };
  }
  const items = [];
  const notes = [];
  let summary = '';
  lines.forEach((line, i) => {
    const item = parseItem(line);
    if (item) items.push(item);
    else if (i === 0 && action === 'search') summary = line;
    else if (line.trim()) notes.push(line);
  });
  if (!items.length && !summary) return null;
  return { action: action === 'around' ? 'around' : 'search', summary, items, notes };
}

// historyOut is the row's magnitude: what a search found, or how many
// messages a window shows.
export function historyOut(parsed) {
  if (!parsed) return '';
  if (parsed.action === 'search') {
    const total = /^(\d+) match/.exec(parsed.summary);
    if (total) return total[1] === '1' ? '1 match' : `${total[1]} matches`;
  }
  if (parsed.action === 'around') return parsed.items.length === 1 ? '1 message' : `${parsed.items.length} messages`;
  return '';
}
