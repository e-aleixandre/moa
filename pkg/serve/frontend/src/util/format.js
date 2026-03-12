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

export function toolSummary(toolName, args) {
  if (!args) return '';
  try {
    const parsed = typeof args === 'string' ? JSON.parse(args) : args;
    // Show first string value as summary
    for (const v of Object.values(parsed)) {
      if (typeof v === 'string' && v.length > 0) {
        const short = v.length > 60 ? v.substring(0, 60) + '…' : v;
        return short;
      }
    }
  } catch { /* ignore */ }
  return '';
}

export function escapeHtml(str) {
  const div = document.createElement('div');
  div.textContent = str;
  return div.innerHTML;
}

/** Strip provider prefix from model string for display. */
export function shortModel(model) {
  if (!model) return '';
  // "anthropic/claude-sonnet-4-6" → "claude-sonnet-4-6"
  const parts = model.split('/');
  return parts.length > 1 ? parts.slice(1).join('/') : model;
}
