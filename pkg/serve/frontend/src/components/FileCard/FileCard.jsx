import { useState } from 'preact/hooks';
import { Info } from 'lucide-preact';
import { FileViewer } from '../FileViewer/FileViewer.jsx';
import { HtmlResourceInfo } from '../HtmlResourceInfo/HtmlResourceInfo.jsx';
import { Artifact, artifactKind } from '../Artifacts/Artifact.jsx';
import { downloadFile } from '../../data/util/file-download.js';
import { isPreviewable, isHTMLPreviewable, humanSize } from '../../data/util/file-card.js';

// FileCard — a send_file result that is not an artifact URL. Markup is the
// catalogue's Artifact card; download and (for HTML) resource inspection are
// grafted as extra targets the prototype never had.
export function FileCard({ file }) {
  const [busy, setBusy] = useState(false);
  const [previewOpen, setPreviewOpen] = useState(false);
  const [resourceInfoOpen, setResourceInfoOpen] = useState(false);

  if (!file) return null;
  const { name, size, mime, url } = file;
  const previewable = isPreviewable(name, mime);
  const htmlPreviewable = isHTMLPreviewable(name, mime);

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

  const extra = htmlPreviewable ? (
    <button
      type="button"
      class="zl-art-act is-btn"
      onClick={(e) => { e.preventDefault(); e.stopPropagation(); setResourceInfoOpen(true); }}
      title="Inspect external resources"
      aria-label="Inspect external resources"
    >
      <Info size={16} />
    </button>
  ) : null;

  return (
    <>
      <Artifact
        name={name}
        kind={artifactKind(file)}
        size={humanSize(size)}
        onOpen={previewable ? () => setPreviewOpen(true) : undefined}
        onAction={handleDownload}
        actionLabel="Download or share"
        busy={busy}
        extra={extra}
      />
      {/* Both rendered unconditionally with `open`: a conditional mount pulls
          the sheet before it can animate out. */}
      <FileViewer open={previewOpen} name={name} mime={mime} url={url} size={size} onClose={() => setPreviewOpen(false)} />
      <HtmlResourceInfo open={resourceInfoOpen} name={name} url={url} onClose={() => setResourceInfoOpen(false)} />
    </>
  );
}
