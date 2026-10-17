// composer-suggest.js — pure helpers for the composer's slash-command and
// @-mention popups. Ported from pkg/serve/frontend/src/components/InputBar.jsx
// (COMMANDS, updateSuggestions, the @ walk-back in updateFileSuggestions,
// acceptFileMention's insertion, and the mobile-dash normalization in
// handleSendInner). Kept DOM-free (value + cursor in, plain data out) so the
// Composer just wires them to the textarea ref/fetch.

import { fuzzyMatch } from './fuzzy.js';
import { cleanLabel, uniqueLabel } from './util/mentions.js';

// Available commands for the suggestion popup. Copied 1:1 from InputBar.jsx —
// keep in sync with the server's command table if it changes.
export const COMMANDS = [
  { name: 'secret', desc: 'Send secrets without typing values in the chat', args: '[alias …]' },
  { name: 'clear', desc: 'Clear conversation history' },
  { name: 'compact', desc: 'Compact conversation context', args: '[focus]' },
  { name: 'prepare-compact', desc: 'Let the agent write down unsaved work, then compact' },
  { name: 'handoff', desc: 'Start a fresh session with a generated handoff', args: '[--model <spec>] [--thinking <level>]' },
  { name: 'model', desc: 'Switch model', args: '<model>' },
  { name: 'thinking', desc: 'Set thinking level', args: '<off|low|medium|high|xhigh>' },
  { name: 'permissions', desc: 'Set permission mode', args: '<yolo|ask|auto>' },
  { name: 'goal', desc: 'Autonomous maker→verifier loop', args: '<objective> [flags]|stop|status' },
  { name: 'tasks', desc: 'View/manage tasks', args: '[done <id> | reset]' },
  { name: 'verify', desc: 'Run verification checks' },
  { name: 'undo', desc: 'Undo last file change' },
  { name: 'path', desc: 'Manage path access scope', args: '[list|add <dir>|rm <dir>|scope workspace|unrestricted]' },
  { name: 'rename', desc: 'Rename this conversation', args: '<title>' },
  { name: 'reload', desc: 'Re-read AGENTS.md, skills and memory index' },
];

// filterCommands returns the commands whose name starts with `filter`
// (case-insensitive). Empty filter matches everything (typing just "/").
//
// `skills` are the session's user-invocable skills, fetched from the server.
// They are listed after the built-ins: the server resolves an unknown slash
// command against skills only, so a skill can never shadow a command, and the
// menu shows the same precedence.
export function filterCommands(filter, skills) {
  const f = (filter || '').toLowerCase();
  const builtins = COMMANDS.filter((c) => c.name.startsWith(f));
  const skillEntries = (skills || [])
    .filter((s) => s.name.startsWith(f))
    .map((s) => ({ name: s.name, desc: s.description || '', __skill: true }));
  return builtins.concat(skillEntries);
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
export function slashSuggestions(value, cursor, goalFlags, skills) {
  if (!value.startsWith('/') || value.includes('\n')) return null;
  const afterSlash = value.slice(1);
  if (afterSlash.includes(' ')) {
    if (value.startsWith('/goal ')) {
      const token = normalizeDashes(tokenAtCursor(value, cursor));
      if (token.startsWith('-')) {
        const matches = filterGoalFlags(goalFlags, token, normalizeDashes(value));
        if (matches.length > 0) return matches;
      }
    }
    return null;
  }
  const matches = filterCommands(afterSlash, skills);
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

export const MENTION_MAX_OWNERS = 4;
export const MENTION_MAX_SESSIONS = 5;

function projectName(root) {
  const clean = String(root || '').replace(/[\\/]+$/, '');
  return clean.slice(Math.max(clean.lastIndexOf('/'), clean.lastIndexOf('\\')) + 1);
}

// ownerSuggestions filters the owners roster for the @ menu: fuzzy on the name
// and on the project's root, the name matching first, at most four. The owner
// whose conversation this is (its session_id) cannot mention itself. A filter with "/" is a
// folder path, which is the Files group's alone.
export function ownerSuggestions(owners, filter, { excludeSessionId = '' } = {}) {
  if (filter.includes('/')) return [];
  const q = filter.toLowerCase();
  const scored = [];
  for (const owner of owners || []) {
    if (!owner?.id || (excludeSessionId && owner.session_id === excludeSessionId)) continue;
    const name = String(owner.name || '').toLowerCase();
    const root = String(owner.root || '').toLowerCase();
    let rank;
    if (!q || name.startsWith(q)) rank = 0;
    else if (name.includes(q)) rank = 1;
    else if (fuzzyMatch(q, name)) rank = 2;
    else if (root.includes(q) || fuzzyMatch(q, projectName(root))) rank = 3;
    else continue;
    scored.push({ rank, owner });
  }
  scored.sort((a, b) => a.rank - b.rank); // stable: roster order within a rank
  return scored.slice(0, MENTION_MAX_OWNERS).map(({ owner }) => ({
    kind: 'owner',
    id: owner.id,
    label: cleanLabel(owner.name),
    project: projectName(owner.root),
    root: owner.root || '',
    saved: owner.session_state === 'saved',
    owner,
  }));
}

// sessionSuggestions shapes the mention-sessions endpoint's answer.
export function sessionSuggestions(rows) {
  return (Array.isArray(rows) ? rows : []).slice(0, MENTION_MAX_SESSIONS).map((row) => ({
    kind: 'session',
    id: row.id,
    label: cleanLabel(row.title) || 'untitled',
    title: row.title || '',
    state: row.saved ? 'saved' : (row.state || ''),
    updated: row.updated,
    saved: !!row.saved,
  }));
}

export const MENTION_GROUPS = [
  { key: 'owners', title: 'Owners' },
  { key: 'sessions', title: 'Sessions' },
  { key: 'files', title: 'Files' },
];

// flattenMentionGroups lays the groups out in menu order (Owners, Sessions,
// Files) as one list the cursor walks. An empty or missing group contributes
// nothing, so it has no header either. Files keep their endpoint shape plus a
// kind; with `dirOnly` (a "dir/" filter) only the Files group is shown.
export function flattenMentionGroups(groups) {
  const out = [];
  if (!groups) return out;
  for (const { key, title } of MENTION_GROUPS) {
    const items = groups[key];
    if (!items || items.length === 0) continue;
    items.forEach((item, i) => {
      out.push({ ...item, kind: key === 'files' ? 'file' : item.kind, group: title, first: i === 0 });
    });
  }
  return out;
}

// computeLabelInsertion accepts an owner/session mention: unlike a file, the
// "@" stays and the label follows it, then a space for the next word.
export function computeLabelInsertion(value, cursor, label) {
  let atIdx = -1;
  for (let i = cursor - 1; i >= 0; i--) {
    if (value[i] === '@') { atIdx = i; break; }
    if (/\s/.test(value[i])) break;
  }
  const before = atIdx >= 0 ? value.slice(0, atIdx) : value.slice(0, cursor);
  const after = value.slice(cursor);
  const replacement = '@' + label + ' ';
  return { value: before + replacement + after, cursor: before.length + replacement.length };
}

// pickMention builds the draft mention for an accepted owner/session row.
export function pickMention(item, existing = []) {
  const label = uniqueLabel(item.label, item.id, existing);
  if (item.kind === 'owner') {
    return { label, type: 'owner', id: item.id, meta: { root: item.root } };
  }
  return { label, type: 'session', id: item.id, meta: { title: item.title, state: item.state } };
}
