// composer-suggest.js — pure helpers for the composer's slash-command and
// @-mention popups. Ported from pkg/serve/frontend/src/components/InputBar.jsx
// (COMMANDS, updateSuggestions, the @ walk-back in updateFileSuggestions,
// acceptFileMention's insertion, and the mobile-dash normalization in
// handleSendInner). Kept DOM-free (value + cursor in, plain data out) so the
// Composer just wires them to the textarea ref/fetch.

// Available commands for the suggestion popup. Copied 1:1 from InputBar.jsx —
// keep in sync with the server's command table if it changes.
export const COMMANDS = [
  { name: 'clear', desc: 'Clear conversation history' },
  { name: 'compact', desc: 'Compact conversation context' },
  { name: 'model', desc: 'Switch model', args: '<model>' },
  { name: 'thinking', desc: 'Set thinking level', args: '<off|low|medium|high|xhigh>' },
  { name: 'permissions', desc: 'Set permission mode', args: '<yolo|ask|auto>' },
  { name: 'plan', desc: 'Enter/exit plan mode', args: '[exit]' },
  { name: 'goal', desc: 'Autonomous maker→verifier loop', args: '<objective> [flags]|stop|status' },
  { name: 'tasks', desc: 'View/manage tasks', args: '[done <id> | reset]' },
  { name: 'verify', desc: 'Run verification checks' },
  { name: 'undo', desc: 'Undo last file change' },
  { name: 'path', desc: 'Manage path access scope', args: '[list|add <dir>|rm <dir>|scope workspace|unrestricted]' },
  { name: 'rename', desc: 'Rename this conversation', args: '<title>' },
  { name: 'schedule', desc: 'Schedule a prompt in this conversation', args: 'at <date> <time> [zone] -- <task> | in <duration> -- <task> | list | cancel <id>' },
];

// filterCommands returns the commands whose name starts with `filter`
// (case-insensitive). Empty filter matches everything (typing just "/").
export function filterCommands(filter) {
  const f = (filter || '').toLowerCase();
  return COMMANDS.filter((c) => c.name.startsWith(f));
}

// filterGoalFlags returns the /goal flags matching `token` (must start with
// "-"), excluding any flag already present as a whole token elsewhere in
// `fullText`. Returned entries carry `__flag: true` so the caller (and the
// popup renderer) can distinguish them from command entries.
export function filterGoalFlags(goalFlags, token, fullText) {
  if (!token || !token.startsWith('-')) return [];
  const filter = token.toLowerCase();
  const isBare = filter === '-' || filter === '--';
  return (goalFlags || [])
    .filter((f) => {
      if (!isBare && !f.name.toLowerCase().startsWith(filter)) return false;
      const re = new RegExp(`(^|\\s)${f.name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}(\\s|$)`);
      return !re.test(fullText || '');
    })
    .map((f) => ({ name: f.name, desc: f.desc, args: f.placeholder, __flag: true }));
}

// tokenAtCursor returns the whitespace-delimited token ending exactly at
// `cursor` (the run of non-space characters immediately before it).
export function tokenAtCursor(value, cursor) {
  let start = cursor;
  while (start > 0 && value[start - 1] !== ' ') start--;
  return value.slice(start, cursor);
}

// slashSuggestions computes the command/flag popup contents for the current
// textarea value + cursor position, mirroring InputBar's updateSuggestions.
// Returns null (hide the popup) or an array of suggestion entries (command
// entries, or __flag entries for "/goal --...").
export function slashSuggestions(value, cursor, goalFlags) {
  if (!value.startsWith('/') || value.includes('\n')) return null;
  const afterSlash = value.slice(1);
  if (afterSlash.includes(' ')) {
    if (value.startsWith('/goal ')) {
      const token = tokenAtCursor(value, cursor);
      if (token.startsWith('-')) {
        const matches = filterGoalFlags(goalFlags, token, value);
        if (matches.length > 0) return matches;
      }
    }
    return null;
  }
  const matches = filterCommands(afterSlash);
  return matches.length > 0 ? matches : null;
}

// findMentionToken walks backwards from `cursor` to locate an "@" that starts
// a file mention: it must be at the start of the text or preceded by
// whitespace, and the text between "@" and the cursor must not contain
// whitespace (a space ends the mention). Returns null when there's no active
// mention, or { atIdx, filter } — the "@" index and the text typed after it.
export function findMentionToken(value, cursor) {
  let atIdx = -1;
  for (let i = cursor - 1; i >= 0; i--) {
    if (value[i] === '@') { atIdx = i; break; }
    if (/\s/.test(value[i])) break;
  }
  if (atIdx < 0 || (atIdx > 0 && !/\s/.test(value[atIdx - 1]))) return null;
  const filter = value.slice(atIdx + 1, cursor);
  if (/\s/.test(filter)) return null;
  return { atIdx, filter };
}

// computeMentionInsertion returns the new textarea value + cursor position
// after accepting a file (or directory) mention, mirroring
// InputBar.acceptFileMention. For a directory, the "@" is kept and the path
// gets a trailing "/" so the caller can re-trigger the file fetch for that
// directory's contents. For a file, the "@" is dropped and the path is
// inserted followed by a space.
export function computeMentionInsertion(value, cursor, path, isDir) {
  let atIdx = -1;
  for (let i = cursor - 1; i >= 0; i--) {
    if (value[i] === '@') { atIdx = i; break; }
    if (/\s/.test(value[i])) break;
  }
  const before = atIdx >= 0 ? value.slice(0, atIdx) : value.slice(0, cursor);
  const after = value.slice(cursor);
  if (isDir) {
    const replacement = '@' + path + '/';
    return { value: before + replacement + after, cursor: before.length + replacement.length, retrigger: true };
  }
  return { value: before + path + ' ' + after, cursor: before.length + path.length + 1, retrigger: false };
}

// normalizeDashes fixes mobile keyboards autocorrecting a typed "--" into an
// em/en-dash ("—"/"–"), which breaks flag parsing (/goal … --max 3). Only a
// dash that starts a token (preceded by whitespace, followed by a letter) is
// rewritten; a real em-dash inside prose (word—word) is left untouched.
export function normalizeDashes(text) {
  return text.replace(/(^|\s)[\u2013\u2014](?=[A-Za-z])/g, '$1--');
}
