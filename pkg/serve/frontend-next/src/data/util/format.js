import { countUnifiedDiffRows } from './unified-diff.js';

export function truncateText(text, max = 2000) {
  if (!text) return '';
  if (text.length <= max) return text;
  return text.substring(0, max) + '\n… (truncated)';
}

export function sessionTitle(session) {
  const t = (session?.title || "").trim();
  return t ? session.title : "Untitled";
}

export function formatArgs(args) {
  if (!args) return '';
  try {
    return typeof args === 'string' ? args : JSON.stringify(args, null, 2);
  } catch {
    return String(args);
  }
}

/** Classify a tool name into a verb for coloring and display. */
export function toolVerb(name) {
  if (!name) return { verb: name, cls: '' };
  const n = name.toLowerCase();
  if (n === 'read' || n === 'Read')          return { verb: 'read',   cls: 'read' };
  if (n === 'write' || n === 'Write')        return { verb: 'write',  cls: 'write' };
  if (n === 'edit' || n === 'Edit')          return { verb: 'edit',   cls: 'edit' };
  if (n === 'bash' || n === 'Bash')          return { verb: 'bash',   cls: 'bash' };
  if (n === 'grep' || n === 'Grep')          return { verb: 'grep',   cls: 'search' };
  if (n === 'find')                          return { verb: 'find',   cls: 'search' };
  if (n === 'ls')                            return { verb: 'ls',     cls: 'read' };
  if (n === 'fetch_content')                 return { verb: 'fetch',  cls: 'fetch' };
  if (n === 'web_search')                    return { verb: 'search', cls: 'search' };
  if (n === 'ask_user')                      return { verb: '❓ questions', cls: 'ask-user' };
  if (n === 'send_file')                     return { verb: '📤 send', cls: 'send-file' };
  if (n === 'subagent')                      return { verb: '⚡ subagent', cls: 'subagent' };
  return { verb: name, cls: '' };
}

/** Extract a short path/command string from tool args for the header. */
export function toolPath(name, args) {
  if (!args) return '';
  const a = typeof args === 'string' ? tryParse(args) : args;
  if (!a) return '';
  const n = (name || '').toLowerCase();

  if (n === 'read' || n === 'write' || n === 'edit' || n === 'multiedit' || n === 'ls')
    return a.path || '';
  if (n === 'send_file') {
    const p = a.path || '';
    return p.length > 80 ? p.split('/').pop() : p;
  }
  if (n === 'bash')
    return shortenCmd(a.command || '');
  if (n === 'grep' || n === 'find')
    return a.pattern || a.glob || '';
  if (n === 'fetch_content')
    return a.url || '';
  if (n === 'web_search')
    return a.query || '';
  if (n === 'ask_user') {
    const qs = a.questions;
    if (Array.isArray(qs) && qs.length > 0) {
      const text = qs[0].question || '';
      return text.length > 80 ? text.substring(0, 77) + '…' : text;
    }
    return '';
  }
  if (n === 'subagent') {
    let task = a.task || '';
    if (task.length > 80) task = task.substring(0, 77) + '…';
    const badges = [];
    if (a.model) badges.push(a.model);
    if (a.thinking) badges.push('thinking:' + a.thinking);
    if (Array.isArray(a.tools) && a.tools.length > 0) badges.push(a.tools.join(','));
    if (badges.length > 0) task += '  [' + badges.join(' · ') + ']';
    return task;
  }

  // Fallback: first short string value
  for (const v of Object.values(a)) {
    if (typeof v === 'string' && v.length > 0) {
      return v.length > 80 ? v.substring(0, 80) + '…' : v;
    }
  }
  return '';
}

/** Extract the complete tool input shown above a finished tool's output. */
export function toolInputLine(name, args) {
  if (!args) return '';
  const a = typeof args === 'string' ? tryParse(args) : args;
  if (!a) return '';
  const n = (name || '').toLowerCase();

  if (n === 'read' || n === 'write') return a.path || '';
  if (n === 'ls') return a.path || '.';
  if (n === 'fetch_content') return a.url || '';
  if (n === 'web_search') return a.query || '';
  if (n === 'grep' || n === 'find') {
    const pattern = a.pattern || a.glob || '';
    if (!pattern) return '';
    let input = `${pattern} · ${a.path || '.'}`;
    if (n === 'grep') {
      if (a.include) input += ` · include:${a.include}`;
      if (a.fixed_strings) input += ' · literal';
    } else if (a.type) {
      input += ` · type:${a.type}`;
    }
    return input;
  }
  return '';
}

/** Extract the most relevant content for the tool preview.
 * startLine: real 1-based file line for edit previews (0/undefined → number from 1). */
export function toolPreview(name, args, result, status, startLine) {
  const n = (name || '').toLowerCase();
  const a = typeof args === 'string' ? tryParse(args) : (args || {});
  const live = status === 'running' || status === 'generating';

  // For write/edit tools, show the content being written.
  if (n === 'write' && a.content)
    return { text: a.content, kind: 'input' };
  if (n === 'edit') {
    // Use server-computed diff (has real file line numbers).
    const liveResult = live && result ? liveUnifiedDiffPreview(result) : null;
    if (live ? liveResult : result && result.includes('@@')) {
      return live
        ? { text: liveResult.text, kind: 'diff', totalLines: liveResult.totalLines }
        : { text: result, kind: 'diff' };
    }
    // Fallback for old results without diff.
    const oldText = a.oldText || a.old_text || '';
    const newText = a.newText || a.new_text || '';
    if (oldText || newText) {
      return {
        text: live ? formatLiveDiff(oldText, newText, startLine) : formatDiff(oldText, newText, startLine),
        kind: 'diff',
      };
    }
  }
  if (n === 'multiedit') {
    const liveResult = live && result ? liveUnifiedDiffPreview(result) : null;
    if (live ? liveResult : result && result.includes('@@')) {
      return live
        ? { text: liveResult.text, kind: 'diff', totalLines: liveResult.totalLines }
        : { text: result, kind: 'diff' };
    }
    if (Array.isArray(a.edits)) {
      // A streaming multiedit only needs its newest argument fragments. Bound
      // the number before formatting so a large call cannot build every diff.
      const edits = live ? a.edits.slice(-PREVIEW_LINES) : a.edits;
      const diffs = edits
        .map((edit) => {
          if (!edit || typeof edit !== 'object') return '';
          const oldText = edit.oldText || edit.old_text || '';
          const newText = edit.newText || edit.new_text || '';
          return oldText || newText
            ? (live ? formatLiveDiff(oldText, newText, startLine) : formatDiff(oldText, newText, startLine))
            : '';
        })
        .filter(Boolean);
      if (diffs.length > 0) {
        return { text: live ? tailLiveDiffInput(diffs.join('\n')) : diffs.join('\n'), kind: 'diff' };
      }
    }
  }

  // Answered questions get a dedicated detail panel in the activity ledger.
  if (n === 'ask_user') {
    return { text: result ? truncateText(String(result), 40) : '', kind: 'ask-user' };
  }

  // send_file is rendered by FileCard component — skip only on success so
  // errors (e.g. file not found) still show the raw message.
  if (n === 'send_file' && status === 'done') return null;

  // For everything else, show the result
  if (result) return { text: result, kind: 'output' };

  return null;
}

const PREVIEW_LINES = 12;
const LIVE_DIFF_MAX_LINES = 400;
const LIVE_DIFF_MAX_CHARS = 20000;

/** Split preview text showing the first N lines, hiding the rest. */
export function splitPreview(text, maxLines = PREVIEW_LINES) {
  if (!text) return { visible: '', hidden: 0, total: 0 };
  const lines = text.split('\n');
  const total = lines.length;
  if (total <= maxLines) return { visible: text, hidden: 0, total };
  return {
    visible: lines.slice(0, maxLines).join('\n'),
    hidden: total - maxLines,
    total,
  };
}

/** Split preview text showing the last N lines (tail mode for streaming). */
export function splitPreviewTail(text, maxLines = PREVIEW_LINES) {
  if (!text) return { visible: '', hidden: 0, total: 0 };
  const lines = text.split('\n');
  const total = lines.length;
  if (total <= maxLines) return { visible: text, hidden: 0, total };
  return {
    visible: lines.slice(-maxLines).join('\n'),
    hidden: total - maxLines,
    total,
  };
}

/** Format a simple unified-style diff between old and new text with line numbers.
 * startLine is the real 1-based file line where oldText starts (defaults to 1). */
export function formatDiff(oldText, newText, startLine = 1) {
  const base = (Number.isInteger(startLine) && startLine > 1) ? startLine - 1 : 0;
  const oldLines = oldText ? oldText.split('\n') : [];
  const newLines = newText ? newText.split('\n') : [];
  const lines = [];
  const maxContext = 3;

  // Find common prefix/suffix to reduce noise.
  let prefixLen = 0;
  while (prefixLen < oldLines.length && prefixLen < newLines.length && oldLines[prefixLen] === newLines[prefixLen]) {
    prefixLen++;
  }
  let suffixLen = 0;
  while (suffixLen < oldLines.length - prefixLen && suffixLen < newLines.length - prefixLen &&
         oldLines[oldLines.length - 1 - suffixLen] === newLines[newLines.length - 1 - suffixLen]) {
    suffixLen++;
  }

  const contextStart = Math.max(0, prefixLen - maxContext);
  const pad = n => String(n).padStart(3);

  // Context before
  for (let i = contextStart; i < prefixLen; i++) {
    lines.push(`${pad(base + i + 1)}   ${oldLines[i]}`);
  }

  // Removed lines
  for (let i = prefixLen; i < oldLines.length - suffixLen; i++) {
    lines.push(`${pad(base + i + 1)} - ${oldLines[i]}`);
  }

  // Added lines
  let newStart = prefixLen;
  for (let i = prefixLen; i < newLines.length - suffixLen; i++) {
    lines.push(`${pad(base + newStart + 1 + (i - prefixLen))} + ${newLines[i]}`);
  }

  // Context after
  const afterStart = oldLines.length - suffixLen;
  const afterEnd = Math.min(oldLines.length, afterStart + maxContext);
  for (let i = afterStart; i < afterEnd; i++) {
    lines.push(`${pad(base + i + 1)}   ${oldLines[i]}`);
  }

  return lines.join('\n');
}

// formatLiveDiff bounds streamed edit arguments before formatting so each
// projection stays small while a large edit call is still arriving.
function formatLiveDiff(oldText, newText, startLine) {
  return tailLiveDiffInput(formatDiff(
    tailLiveDiffInput(oldText),
    tailLiveDiffInput(newText),
    startLine,
  ));
}

function tailLiveDiffInput(text) {
  const charTail = tailLiveDiffChars(text);
  return splitPreviewTail(charTail, LIVE_DIFF_MAX_LINES).visible;
}

function tailLiveDiffChars(text) {
  return text.length > LIVE_DIFF_MAX_CHARS ? text.slice(-LIVE_DIFF_MAX_CHARS) : text;
}

// Bounds a growing server diff before checking or parsing it. Detection and
// parsed text share one identical tail, avoiding a second truncation.
function liveUnifiedDiffPreview(text) {
  const charTail = tailLiveDiffChars(text);
  if (!charTail.includes('@@') && !/(^|\n)[+-](?!\+\+\+ |-- )/.test(charTail)) return null;
  return { text: tailLiveUnifiedDiff(charTail), totalLines: countUnifiedDiffRows(text) };
}

// Keep a hunk marker when a long unified diff's tail no longer contains one.
// The parser needs it to retain +/- line types; line numbers are not rendered
// by the live window, so a synthetic marker is sufficient for the bounded tail.
function tailLiveUnifiedDiff(text) {
  if (!text || /(^|\n)@@ /.test(text)) return text;
  const marker = '@@ -1 +1 @@\n';
  return `${marker}${text}`;
}

function shortenCmd(cmd) {
  if (!cmd) return '';
  // Trim and take first line if multiline
  const first = cmd.trim().split('\n')[0];
  return first.length > 100 ? first.substring(0, 100) + '…' : first;
}

function tryParse(s) {
  try { return JSON.parse(s); } catch { return null; }
}

export function escapeHtml(str) {
  const div = document.createElement('div');
  div.textContent = str;
  return div.innerHTML;
}

/** Strip provider prefix from model string for display. */
export function shortModel(model) {
  if (!model) return '';
  const parts = model.split('/');
  return parts.length > 1 ? parts.slice(1).join('/') : model;
}

/** A compact model label for the narrow mobile header pill. Prefers a known
 *  short alias (sol/fable/terra/haiku — the same family as MODEL_ACCENT) when
 *  the model name contains one. A curated display name (has spaces, e.g.
 *  "Claude Opus 4.8") is trusted as-is. A raw technical id drops a noisy vendor
 *  prefix ("claude-"/"gpt-") and truncates ("claude-opus-4-8" → "opus-4-8"). */

// MODEL_CODENAMES — the friendly one-word names moa gives its models across
// providers. Detected as a substring of the model string (case-insensitive) and
// rendered capitalized, so "Claude Opus 4.8" → "Opus", "Claude Fable 5" →
// "Fable", "GPT-5.6 Sol" → "Sol". None is a substring of another, so order
// doesn't matter. Anthropic: opus/sonnet/haiku/fable; OpenAI: sol/terra/luna.
const MODEL_CODENAMES = ['opus', 'sonnet', 'haiku', 'fable', 'sol', 'terra', 'luna'];

/** modelCodename → the friendly one-word model name (capitalized) when the model
 *  string carries a known codename; "" when it doesn't (caller falls back). */
export function modelCodename(model) {
  const lower = (model || '').toLowerCase();
  for (const c of MODEL_CODENAMES) {
    if (lower.includes(c)) return c.charAt(0).toUpperCase() + c.slice(1);
  }
  return '';
}

/** Formats a model's max input context window for the compact model-chip
 *  sublines: 1_000_000 → "1M ctx", 1_050_000 → "1M ctx" (rounds to the
 *  nearest whole M when within 0.1M of it), 400_000 → "400K ctx". Returns ""
 *  when the registry doesn't carry a context size (custom model, or the
 *  endpoint hasn't populated max_input) so callers can drop it from the
 *  "version · context" subline instead of showing a bogus "0 ctx". */
export function contextWindowLabel(maxInput) {
  if (typeof maxInput !== 'number' || !isFinite(maxInput) || maxInput <= 0) return '';
  if (maxInput >= 1_000_000) {
    const millions = maxInput / 1_000_000;
    const roundedInt = Math.round(millions);
    if (Math.abs(millions - roundedInt) < 0.1) return `${roundedInt}M ctx`;
    return `${Math.round(millions * 10) / 10}M ctx`;
  }
  if (maxInput >= 1000) return `${Math.round(maxInput / 1000)}K ctx`;
  return `${maxInput} ctx`;
}

export function mobileModelLabel(model) {
  const code = modelCodename(model);
  if (code) return code;
  const short = shortModel(model || '');
  if (!short) return '';
  if (short.includes(' ')) return short; // curated display name — trust it
  const trimmed = short.replace(/^(claude-|gpt-|models\/)/i, '');
  return trimmed.length > 12 ? trimmed.slice(0, 11) + '…' : trimmed;
}

/** A compact token count for the status line: 41200 → "41k", 8700 → "8.7k",
 *  940 → "940". Keeps one decimal only while the value stays below 10k; at/above
 *  10k it rounds to a whole "k" (and a value like 9990 that would round up to
 *  10.0k is shown as "10k", not "10.0k"). */
export function fmtTokens(n) {
  if (typeof n !== 'number' || !isFinite(n) || n < 0) return '0';
  if (n < 1000) return String(Math.round(n));
  const k = n / 1000;
  const oneDecimal = Math.round(k * 10) / 10;
  if (oneDecimal < 10) return oneDecimal + 'k';
  return Math.round(k) + 'k';
}

/** A stable key identifying the project a session belongs to (its cwd). */
export function projectKey(cwd) {
  if (!cwd) return '';
  return cwd.replace(/\/+$/, '');
}

/** A short, human-friendly project label from a cwd: the last two path
 *  segments (e.g. "/home/me/dev/moa/main" → "moa/main"). */
export function projectLabel(cwd) {
  const p = projectKey(cwd);
  if (!p) return 'No project';
  const segs = p.split('/').filter(Boolean);
  if (segs.length === 0) return '/';
  return segs.slice(-2).join('/');
}

/** A compact display of a full path for a session card: collapses the home
 *  prefix to "~" and keeps the tail readable on narrow screens. */
export function shortPath(cwd, maxLen = 42) {
  let p = projectKey(cwd);
  if (!p) return '';
  p = p.replace(/^\/home\/[^/]+/, '~').replace(/^\/root/, '~');
  if (p.length <= maxLen) return p;
  return '…' + p.slice(-(maxLen - 1));
}

/** Collapse the server's home prefix to "~". Unlike shortPath this needs the
 *  real home directory (from /api/capabilities) and never truncates, so it is
 *  the one to use for a path the user can edit. */
export function tildify(path, home) {
  if (!home) return path;
  if (path === home) return '~';
  if (path.startsWith(home + '/')) return '~' + path.slice(home.length);
  return path;
}

/** The inverse of tildify: turn a typed "~/..." back into an absolute path. */
export function expandHome(path, home) {
  if (!home) return path;
  if (path === '~') return home;
  if (path.startsWith('~/')) return home + path.slice(1);
  return path;
}

/** Last segment of a path ("/a/b/c" → "c"), "/" for the root. */
export function basename(p) {
  const parts = p.split('/').filter(Boolean);
  return parts.pop() || '/';
}

/** Default window (days) for "recent" session lists. Older sessions are hidden
 *  from the lists to avoid overload but remain findable via search. */
export const RECENT_DAYS = 7;

/** Whether a session counts as "recent" (updated within RECENT_DAYS). Sessions
 *  with no timestamp are treated as recent so they never silently vanish. */
export function isRecentSession(sess, days = RECENT_DAYS) {
  if (!sess || !sess.updated) return true;
  return Date.now() - sess.updated <= days * 24 * 60 * 60 * 1000;
}


/** Returns the state used to color a session's status dot. It mirrors the
 *  session's own state, except that an idle main agent which has live
 *  subagents still counts as 'running' — otherwise a session waiting on a
 *  delegated subagent shows green (idle) despite work being in progress.
 *  A non-idle main state (running/permission/error) always wins. */
export function sessionDotState(sess) {
  if (!sess) return 'idle';
  if (sess.state && sess.state !== 'idle') return sess.state;
  if (hasLiveSubagents(sess)) return 'running';
  return sess.state || 'idle';
}

function hasLiveSubagents(sess) {
  if (sess.subagentCount > 0) return true;
  const subs = sess.subagents;
  if (subs) {
    for (const k in subs) {
      const st = subs[k] && subs[k].status;
      if (st === 'running' || st === 'cancelling') return true;
    }
  }
  return false;
}

/** copyToClipboard writes text to the clipboard and resolves true/false so a
 *  caller can only flash "copied ✓" on real success, never unconditionally.
 *  A rejected clipboard promise (permissions/insecure context/user gesture
 *  lost) is swallowed here rather than left as an unhandled rejection. */
export function copyToClipboard(text) {
  if (!text || !navigator.clipboard?.writeText) return Promise.resolve(false);
  return navigator.clipboard.writeText(text).then(() => true).catch(() => false);
}
