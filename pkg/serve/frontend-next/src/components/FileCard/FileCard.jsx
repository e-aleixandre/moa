import { useState } from 'preact/hooks';
import { Download, FileText, FileImage, FileArchive, File as FileIcon, Info, Loader2 } from 'lucide-preact';
import { FileViewer } from '../FileViewer/FileViewer.jsx';
import { HtmlResourceInfo } from '../HtmlResourceInfo/HtmlResourceInfo.jsx';
import { downloadFile } from '../../data/util/file-download.js';
import { isPreviewable, isHTMLPreviewable, iconKindFor, humanSize } from '../../data/util/file-card.js';
import './FileCard.css';

const ICONS = { image: FileImage, text: FileText, archive: FileArchive, file: FileIcon };

// FileCard — the download card a send_file tool result renders as (instead of
// raw text). `file` is the {name, size, mime, url} descriptor already parsed
// from the tool result by stream-model.js's toFileBlock (see
// data/util/file-card.js#parseFileCardData for the parsing rule).
export function FileCard({ file }) {
  const [busy, setBusy] = useState(false);
  const [previewOpen, setPreviewOpen] = useState(false);
  const [resourceInfoOpen, setResourceInfoOpen] = useState(false);

  if (!file) return null;
  const { name, size, mime, url } = file;
  const Icon = ICONS[iconKindFor(mime)] || FileIcon;
  const previewable = isPreviewable(name, mime);
  const htmlPreviewable = isHTMLPreviewable(name, mime);

  // Fetch the file as a blob and hand it off via the OS share sheet (mobile)
  // or a same-origin blob: URL (desktop), instead of navigating the WebView
  // to the download URL directly. Installed PWAs run with no browser chrome
  // (display: standalone), so a direct <a href> download opens a full-screen,
  // chrome-less "file downloaded" view with no way to dismiss it short of
  // force-closing the app. blob: URLs never trigger that full-page navigation.
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
  const openResourceInfo = (e) => {
    e.preventDefault();
    e.stopPropagation();
    setResourceInfoOpen(true);
  };

  return (
    <>
      <div class="file-card">
        <button
          type="button"
          class={`file-card-open ${previewable ? 'file-card-previewable' : ''}`}
          onClick={openPreview}
          disabled={!previewable}
        >
          <Icon class="file-card-icon" />
          <div class="file-card-info">
            <div class="file-card-name">{name}</div>
            <div class="file-card-size">{humanSize(size)}</div>
          </div>
        </button>
        {htmlPreviewable && (
          <button
            type="button"
            class="file-card-resource-info"
            onClick={openResourceInfo}
            title="Inspect external resources"
            aria-label="Inspect external resources"
          >
            <Info />
          </button>
        )}
        <button
          type="button"
          class="file-card-download"
          onClick={handleDownload}
          disabled={busy}
          title="Download or share"
          aria-label="Download or share"
        >
          {busy ? <Loader2 class="spin" /> : <Download />}
        </button>
      </div>
      {previewOpen && (
        <FileViewer name={name} mime={mime} url={url} size={size} onClose={() => setPreviewOpen(false)} />
      )}
      {resourceInfoOpen && (
        <HtmlResourceInfo name={name} url={url} onClose={() => setResourceInfoOpen(false)} />
      )}
    </>
  );
}
