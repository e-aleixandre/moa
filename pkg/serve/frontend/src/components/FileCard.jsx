import { useState } from 'preact/hooks';
import { Download, FileText, FileImage, FileArchive, File as FileIcon, Loader2 } from 'lucide-preact';
import { FileViewer } from './FileViewer.jsx';
import { downloadFile } from '../util/file-download.js';

/**
 * Renders a send_file tool result as a download card instead of raw text.
 * Parses the last line of the tool result as JSON (the convention used by
 * the send_file tool: a human-readable line followed by a JSON line).
 */
export function FileCard({ result, status }) {
  const [busy, setBusy] = useState(false);
  const [previewOpen, setPreviewOpen] = useState(false);

  if (status !== 'done' || !result) return null;

  const data = parseFileCardData(result);
  if (!data) return null;

  const { name, size, mime, url } = data;
  const Icon = iconFor(mime);
  const previewable = isPreviewable(name, mime);

  // Fetch the file as a blob and hand it off via the OS share sheet (mobile)
  // or a same-origin blob: URL (desktop), instead of navigating the WebView
  // to the download URL directly. Installed PWAs run with no browser chrome
  // (display: standalone), so a direct <a href> download opens a full-screen,
  // chrome-less "file downloaded" view with no way to dismiss it short of
  // force-closing the app — see tmp/bugs.md. blob: URLs never trigger that
  // full-page navigation.
  const handleDownload = async (e) => {
    e.preventDefault();
    e.stopPropagation();
    if (busy) return;
    setBusy(true);
    try {
      await downloadFile({ name, mime, url });
    } catch (err) {
      if (err?.name !== 'AbortError') console.error('FileCard download failed:', err);
    } finally {
      setBusy(false);
    }
  };

  const openPreview = () => previewable && setPreviewOpen(true);
  const onKeyDown = (e) => {
    // Only the card itself opens the preview: keydowns bubbling up from the
    // nested download button (Enter/Space) must not trigger it.
    if (e.target !== e.currentTarget) return;
    if (previewable && (e.key === 'Enter' || e.key === ' ')) {
      e.preventDefault();
      openPreview();
    }
  };

  return (
    <>
    <div class={`file-card ${previewable ? 'file-card-previewable' : ''}`} onClick={openPreview} onKeyDown={onKeyDown} role={previewable ? 'button' : undefined} tabIndex={previewable ? 0 : undefined}>
      <Icon class="file-card-icon" />
      <div class="file-card-info">
        <div class="file-card-name">{name}</div>
        <div class="file-card-size">{humanSize(size)}</div>
      </div>
      <button class="file-card-download" onClick={handleDownload} disabled={busy} title="Download or share" aria-label="Download or share">
        {busy ? <Loader2 class="spin" /> : <Download />}
      </button>
    </div>
    {previewOpen && <FileViewer name={name} mime={mime} url={url} size={size} onClose={() => setPreviewOpen(false)} />}
    </>
  );
}

export function isPreviewable(name, mime) {
  const mediaType = (mime || '').split(';', 1)[0].trim().toLowerCase();
  const lowerName = (name || '').toLowerCase();
  return mediaType.startsWith('image/') || mediaType.startsWith('text/') || mediaType.includes('markdown') ||
    mediaType === 'text/html' || lowerName.endsWith('.md') || lowerName.endsWith('.markdown') ||
    lowerName.endsWith('.html') || lowerName.endsWith('.htm');
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
