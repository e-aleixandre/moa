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
    // Show the new text being inserted
    if (a.newText) return { text: a.newText, kind: 'input' };
    if (a.new_text) return { text: a.new_text, kind: 'input' };
  }

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
