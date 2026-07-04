import { Download, FileText, FileImage, FileArchive, File as FileIcon } from 'lucide-preact';

/**
 * Renders a send_file tool result as a download card instead of raw text.
 * Parses the last line of the tool result as JSON (the convention used by
 * the send_file tool: a human-readable line followed by a JSON line).
 */
export function FileCard({ result, status }) {
  if (status !== 'done' || !result) return null;

  const data = parseFileCardData(result);
  if (!data) return null;

  const { name, size, mime, url } = data;
  const Icon = iconFor(mime);

  return (
    <div class="file-card">
      <Icon class="file-card-icon" />
      <div class="file-card-info">
        <div class="file-card-name">{name}</div>
        <div class="file-card-size">{humanSize(size)}</div>
      </div>
      <a class="file-card-download" href={url} download={name} title="Download">
        <Download />
      </a>
    </div>
  );
}

function parseFileCardData(result) {
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

function iconFor(mime) {
  if (!mime) return FileIcon;
  if (mime.startsWith('image/')) return FileImage;
  if (mime.startsWith('text/') || mime === 'application/pdf' || mime === 'application/json') return FileText;
  if (mime.includes('zip') || mime.includes('tar') || mime.includes('compressed')) return FileArchive;
  return FileIcon;
}

function humanSize(n) {
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
