// file-card.js — pure helpers for the send_file download card (FileCard).
// Ported from the old SPA's FileCard.jsx: parsing the tool result, deciding
// what's previewable, and picking an icon. Kept DOM-free so it's usable both
// from the component and from stream-model.js (which needs parseFileCardData
// to detect a send_file tool_start and emit a `file` block).

// parseFileCardData reads the LAST line of a send_file tool result as JSON
// (the convention used by the send_file tool: a human-readable line followed
// by a JSON line: {file_id, mime, name, size, url}). Returns null if the
// result doesn't end in valid JSON, or the JSON isn't a trustworthy file
// descriptor.
export function parseFileCardData(result) {
  if (!result) return null;
  const lines = result.trim().split('\n');
  let data;
  try {
    data = JSON.parse(lines[lines.length - 1]);
  } catch {
    return null;
  }
  // Sanity guard: only trust a URL our own tool generated.
  if (!data || typeof data.url !== 'string' || !data.url.startsWith('/api/')) return null;
  return data;
}

export function isPreviewable(name, mime) {
  const mediaType = (mime || '').split(';', 1)[0].trim().toLowerCase();
  const lowerName = (name || '').toLowerCase();
  return mediaType.startsWith('image/') || mediaType.startsWith('text/') || mediaType.includes('markdown') ||
    mediaType === 'text/html' || lowerName.endsWith('.md') || lowerName.endsWith('.markdown') ||
    lowerName.endsWith('.html') || lowerName.endsWith('.htm');
}

export function isHTMLPreviewable(name, mime) {
  const mediaType = (mime || '').split(';', 1)[0].trim().toLowerCase();
  const lowerName = (name || '').toLowerCase();
  return mediaType === 'text/html' || lowerName.endsWith('.html') || lowerName.endsWith('.htm');
}

// iconKindFor returns a plain string token (not a component) so this stays
// DOM-free; the component maps it to a lucide-preact icon.
export function iconKindFor(mime) {
  if (!mime) return 'file';
  if (mime.startsWith('image/')) return 'image';
  if (mime.startsWith('text/') || mime === 'application/pdf' || mime === 'application/json') return 'text';
  if (mime.includes('zip') || mime.includes('tar') || mime.includes('compressed')) return 'archive';
  return 'file';
}

export function humanSize(n) {
  if (typeof n !== 'number' || n < 1024) return `${n} B`;
  const units = ['KB', 'MB', 'GB', 'TB'];
  let value = n / 1024;
  let i = 0;
  while (value >= 1024 && i < units.length - 1) {
    value /= 1024;
    i++;
  }
  return `${value.toFixed(1)} ${units[i]}`;
}

// previewKind decides which FileViewer renderer applies (image / html /
// markdown / plain text), based on mime first and file extension as fallback.
export function previewKind(name, mime) {
  const mediaType = (mime || '').split(';', 1)[0].trim().toLowerCase();
  const extension = (name || '').toLowerCase().match(/\.[^.]+$/)?.[0];
  if (mediaType.startsWith('image/')) return 'image';
  if (mediaType === 'text/html' || extension === '.html' || extension === '.htm') return 'html';
  if (mediaType.includes('markdown') || extension === '.md' || extension === '.markdown') return 'markdown';
  return 'text';
}

export function looksBinary(text) {
  if (text.includes('\0')) return true;
  const replacements = (text.match(/\uFFFD/g) || []).length;
  return replacements > 16 && replacements / Math.max(text.length, 1) > 0.01;
}
