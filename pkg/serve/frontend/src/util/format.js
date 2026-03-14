export function truncateText(text, max = 2000) {
  if (!text) return '';
  if (text.length <= max) return text;
  return text.substring(0, max) + '\n… (truncated)';
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
  return { verb: name, cls: '' };
}

/** Extract a short path/command string from tool args for the header. */
export function toolPath(name, args) {
  if (!args) return '';
  const a = typeof args === 'string' ? tryParse(args) : args;
  if (!a) return '';
  const n = (name || '').toLowerCase();

  if (n === 'read' || n === 'write' || n === 'edit' || n === 'ls')
    return a.path || '';
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

  // Fallback: first short string value
  for (const v of Object.values(a)) {
    if (typeof v === 'string' && v.length > 0) {
      return v.length > 80 ? v.substring(0, 80) + '…' : v;
    }
  }
  return '';
}

/** Extract the most relevant content for the tool preview. */
export function toolPreview(name, args, result) {
  const n = (name || '').toLowerCase();
  const a = typeof args === 'string' ? tryParse(args) : (args || {});

  // For write/edit, show the content being written
  if (n === 'write' && a.content)
    return { text: a.content, kind: 'input' };
  if (n === 'edit') {
    const oldText = a.oldText || a.old_text || '';
    const newText = a.newText || a.new_text || '';
    if (oldText || newText) return { text: formatDiff(oldText, newText), kind: 'diff' };
  }

  // ask_user is rendered by AskUserPreview component — skip here.
  if (n === 'ask_user') return null;

  // For everything else, show the result
  if (result) return { text: result, kind: 'output' };

  return null;
}

const PREVIEW_LINES = 12;

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

/** Format a simple unified-style diff between old and new text. */
function formatDiff(oldText, newText) {
  const oldLines = oldText ? oldText.split('\n') : [];
  const newLines = newText ? newText.split('\n') : [];
  const lines = [];

  // Simple line-by-line diff: show removed then added.
  // For short edits this is clearer than a full LCS algorithm.
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
  const contextEnd = suffixLen > 0 ? Math.min(oldLines.length, oldLines.length - suffixLen + maxContext) : oldLines.length;

  // Context before
  for (let i = contextStart; i < prefixLen; i++) {
    lines.push('  ' + oldLines[i]);
  }

  // Removed lines
  for (let i = prefixLen; i < oldLines.length - suffixLen; i++) {
    lines.push('- ' + oldLines[i]);
  }

  // Added lines
  for (let i = prefixLen; i < newLines.length - suffixLen; i++) {
    lines.push('+ ' + newLines[i]);
  }

  // Context after
  const afterStart = oldLines.length - suffixLen;
  const afterEnd = Math.min(oldLines.length, afterStart + maxContext);
  for (let i = afterStart; i < afterEnd; i++) {
    lines.push('  ' + oldLines[i]);
  }

  return lines.join('\n');
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
