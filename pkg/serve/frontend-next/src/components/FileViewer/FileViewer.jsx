import { useEffect, useState } from 'preact/hooks';
import { Download, Loader2 } from 'lucide-preact';
import { Sheet } from '../Sheet/Sheet.jsx';
import { renderMarkdown } from '../../data/util/markdown.js';
import { downloadFile } from '../../data/util/file-download.js';
import { readCapped } from '../../data/util/file-preview.js';
import { buildHTMLSrcdoc, HTML_PREVIEW_SANDBOX } from '../../data/util/html-preview.js';
import { previewKind, looksBinary } from '../../data/util/file-card.js';
import './FileViewer.css';

const MAX_PREVIEW_SIZE = 2 * 1024 * 1024;
const MAX_HIGHLIGHT_SIZE = 150 * 1024;

// FileViewer — in-app preview of a send_file result (image/HTML/markdown/
// plain text), rendered inside the shared Sheet container. Fetches the file
// fresh (never trusts the possibly-stale size from send_file) and caps the
// read at MAX_PREVIEW_SIZE so a large or rewritten-in-place file never blows
// up memory on mobile.
export function FileViewer({ name, mime, url, size, onClose }) {
  const [state, setState] = useState({ kind: 'loading' });
  const [downloading, setDownloading] = useState(false);

  // Scroll-lock only — the back-gesture/history binding lives once in the
  // Sheet this component renders into (data/overlay-history.js via
  // components/Sheet/Sheet.jsx), so it isn't duplicated here.
  useEffect(() => {
    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    return () => {
      document.body.style.overflow = previousOverflow;
    };
  }, []);

  useEffect(() => {
    let cancelled = false;
    let imageURL;
    if (size > MAX_PREVIEW_SIZE) {
      setState({ kind: 'too-large' });
      return undefined;
    }

    setState({ kind: 'loading' });
    fetch(url).then(async (resp) => {
      if (!resp.ok) {
        throw Object.assign(new Error(`preview failed: ${resp.status}`), { status: resp.status });
      }
      // Enforce the cap against the *actual* response, not the (possibly stale)
      // size from send_file: the file on disk may have grown/been rewritten in
      // place since it was shared, so trusting old metadata would let a large
      // body through and blow up memory on mobile.
      const blob = await readCapped(resp, MAX_PREVIEW_SIZE);
      const kind = previewKind(name, mime);
      if (kind === 'image') {
        if (cancelled) return;
        imageURL = URL.createObjectURL(blob);
        setState({ kind: 'image', url: imageURL });
        return;
      }

      const text = await blob.text();
      if (cancelled) return;
      if (looksBinary(text)) {
        setState({ kind: 'binary' });
        return;
      }
      setState({ kind: 'document', srcdoc: buildSrcdoc(kind, text) });
    }).catch((error) => {
      if (cancelled) return;
      if (error?.tooLarge) setState({ kind: 'too-large' });
      else setState({ kind: error.status === 404 ? 'expired' : 'error' });
    });

    return () => {
      cancelled = true;
      if (imageURL) URL.revokeObjectURL(imageURL);
    };
  }, [name, mime, size, url]);

  const handleDownload = async () => {
    if (downloading) return;
    setDownloading(true);
    try {
      await downloadFile({ name, mime, url });
    } catch (error) {
      if (error?.name !== 'AbortError') console.error('FileViewer download failed:', error);
    } finally {
      setDownloading(false);
    }
  };

  return (
    <Sheet open onClose={onClose} title={name} ariaLabel={`Preview ${name}`} class="file-viewer-sheet">
      <div class="file-viewer-toolbar">
        <button class="file-viewer-action" onClick={handleDownload} disabled={downloading} title="Download or share">
          {downloading ? <Loader2 class="spin" /> : <Download />} Download
        </button>
      </div>
      <div class="file-viewer-body">
        {state.kind === 'loading' && <div class="file-viewer-status"><Loader2 class="spin" /> Loading preview…</div>}
        {state.kind === 'image' && <div class="file-viewer-image-wrap"><img src={state.url} alt={name} /></div>}
        {state.kind === 'document' && (
          <iframe class="file-viewer-frame" sandbox={HTML_PREVIEW_SANDBOX} srcdoc={state.srcdoc} referrerpolicy="no-referrer" title={name} />
        )}
        {state.kind === 'expired' && <StatusMessage>Link expired (the server restarted) — ask the agent to resend it</StatusMessage>}
        {state.kind === 'too-large' && <StatusMessage>Too large to preview</StatusMessage>}
        {state.kind === 'binary' && <StatusMessage>Cannot preview (looks binary)</StatusMessage>}
        {state.kind === 'error' && <StatusMessage>Could not load the preview</StatusMessage>}
        {['expired', 'too-large', 'binary', 'error'].includes(state.kind) && (
          <button class="file-viewer-download-button" onClick={handleDownload} disabled={downloading}>
            {downloading ? <Loader2 class="spin" /> : <Download />} Download
          </button>
        )}
      </div>
    </Sheet>
  );
}

function StatusMessage({ children }) {
  return <div class="file-viewer-status file-viewer-error">{children}</div>;
}

function escapeHtml(text) {
  return text.replace(/[&<>"']/g, (char) => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
  }[char]));
}

export function buildSrcdoc(kind, content) {
  let body;
  if (kind === 'html') {
    // HTML previews intentionally support interactive, externally styled
    // mockups. The iframe still has an opaque sandboxed origin and cannot
    // access moa's DOM, cookies, or storage.
    return buildHTMLSrcdoc(content, iframeStyles);
  } else if (kind === 'markdown' && content.length <= MAX_HIGHLIGHT_SIZE) {
    body = `<article class="msg-assistant">${renderMarkdown(content)}</article>`;
  } else {
    // Avoid highlighting large mobile previews.
    body = `<pre class="plain-text">${escapeHtml(content)}</pre>`;
  }
  return `<!doctype html><html><head><meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; img-src data:"><style>${iframeStyles}</style></head><body>${body}</body></html>`;
}

const iframeStyles = `
:root{color-scheme:light}*{box-sizing:border-box}body{background:#fff;color:#111;margin:16px;font-family:system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;line-height:1.6;word-break:break-word}.msg-assistant{font-size:14px;max-width:100%}.msg-assistant p{margin:0 0 12px}.msg-assistant ul,.msg-assistant ol{padding-left:24px;margin:0 0 12px}.msg-assistant li{margin-bottom:4px}.msg-assistant blockquote{border-left:3px solid #8839ef;padding-left:14px;color:#555;margin:0 0 12px}.msg-assistant strong,.msg-assistant h1,.msg-assistant h2,.msg-assistant h3{color:#111}.msg-assistant h1,.msg-assistant h2,.msg-assistant h3{margin:18px 0 8px}.msg-assistant h1{font-size:20px}.msg-assistant h2{font-size:17px}.msg-assistant h3{font-size:15px}.msg-assistant h1:first-child,.msg-assistant h2:first-child,.msg-assistant h3:first-child{margin-top:0}.msg-assistant a{color:#c75d00}.msg-assistant code{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:12.5px;background:#f1f1f1;padding:2px 5px;border-radius:4px}.msg-assistant pre,.plain-text{margin:10px 0;padding:12px;overflow:auto;background:#f5f5f5;border-radius:6px;font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:12.5px;line-height:1.55;white-space:pre-wrap}.msg-assistant pre code{padding:0;background:none}.msg-assistant table{border-collapse:collapse;font-size:13px}.msg-assistant th,.msg-assistant td{border:1px solid #ccc;padding:6px 10px;text-align:left}.msg-assistant th{background:#eee}.md-table-wrap{overflow-x:auto;margin:10px 0}.code-block{margin:10px 0}.code-block-header{display:none}`;
