import { useState } from 'preact/hooks';
import { Download, FileText, FileImage, FileArchive, File as FileIcon, Loader2 } from 'lucide-preact';

/**
 * Renders a send_file tool result as a download card instead of raw text.
 * Parses the last line of the tool result as JSON (the convention used by
 * the send_file tool: a human-readable line followed by a JSON line).
 */
export function FileCard({ result, status }) {
  const [busy, setBusy] = useState(false);

  if (status !== 'done' || !result) return null;

  const data = parseFileCardData(result);
  if (!data) return null;

  const { name, size, mime, url } = data;
  const Icon = iconFor(mime);

  // Fetch the file as a blob and hand it off via the OS share sheet (mobile)
  // or a same-origin blob: URL (desktop), instead of navigating the WebView
  // to the download URL directly. Installed PWAs run with no browser chrome
  // (display: standalone), so a direct <a href> download opens a full-screen,
  // chrome-less "file downloaded" view with no way to dismiss it short of
  // force-closing the app — see tmp/bugs.md. blob: URLs never trigger that
  // full-page navigation.
  const handleClick = async (e) => {
    e.preventDefault();
    if (busy) return;
    setBusy(true);
    try {
      const resp = await fetch(url);
      if (!resp.ok) throw new Error(`download failed: ${resp.status}`);
      const blob = await resp.blob();
      const file = new File([blob], name, { type: mime || blob.type });

      if (navigator.canShare && navigator.canShare({ files: [file] })) {
        await navigator.share({ files: [file] });
        return;
      }

      const blobUrl = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = blobUrl;
      a.download = name;
      document.body.appendChild(a);
      a.click();
      a.remove();
      setTimeout(() => URL.revokeObjectURL(blobUrl), 30000);
    } catch (err) {
      if (err?.name !== 'AbortError') console.error('FileCard download failed:', err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div class="file-card">
      <Icon class="file-card-icon" />
      <div class="file-card-info">
        <div class="file-card-name">{name}</div>
        <div class="file-card-size">{humanSize(size)}</div>
      </div>
      <button class="file-card-download" onClick={handleClick} disabled={busy} title="Download">
        {busy ? <Loader2 class="spin" /> : <Download />}
      </button>
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
