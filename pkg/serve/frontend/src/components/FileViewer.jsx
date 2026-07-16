import { useEffect, useRef, useState } from 'preact/hooks';
import { Download, Loader2, X } from 'lucide-preact';
import { renderMarkdown } from '../util/markdown.js';
import { downloadFile } from '../util/file-download.js';

const MAX_PREVIEW_SIZE = 2 * 1024 * 1024;
const MAX_HIGHLIGHT_SIZE = 150 * 1024;

export function FileViewer({ name, mime, url, size, onClose }) {
  const [state, setState] = useState({ kind: 'loading' });
  const [downloading, setDownloading] = useState(false);
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;

  // Bind the overlay to a history entry so the mobile back gesture / Android
  // back button closes the viewer instead of navigating the SPA (or exiting
  // the PWA). A unique marker lets the cleanup tell "closed via back/popstate"
  // (entry already consumed) from "unmounted for another reason" (e.g. the
  // session switched) — in the latter case we consume the dangling entry so
  // history stays in sync.
  useEffect(() => {
    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    const marker = `fv-${Date.now()}-${Math.random().toString(36).slice(2)}`;
    history.pushState({ fileViewer: marker }, '');
    let closedByPop = false;
    const closeOnBack = () => { closedByPop = true; onCloseRef.current(); };
    const closeOnEsc = (e) => { if (e.key === 'Escape') history.back(); };
    addEventListener('popstate', closeOnBack);
    addEventListener('keydown', closeOnEsc);
    return () => {
      document.body.style.overflow = previousOverflow;
      removeEventListener('popstate', closeOnBack);
      removeEventListener('keydown', closeOnEsc);
      if (!closedByPop && history.state?.fileViewer === marker) history.back();
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
      const type = previewKind(name, mime);
      if (type === 'image') {
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
      setState({ kind: 'document', srcdoc: buildSrcdoc(type, text) });
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

  // The pushed history entry is consumed by popstate, which performs the
  // visual close. Calling onClose directly here would leave that entry behind.
  const handleClose = () => history.back();

  return (
    <div class="file-viewer-overlay" role="dialog" aria-modal="true" aria-label={`Preview ${name}`}>
      <header class="file-viewer-header">
        <div class="file-viewer-name" title={name}>{name}</div>
        <div class="file-viewer-actions">
          <button class="file-viewer-action" onClick={handleDownload} disabled={downloading} title="Download or share">
            {downloading ? <Loader2 class="spin" /> : <Download />}
          </button>
          <button class="file-viewer-close" onClick={handleClose} title="Close" aria-label="Close preview"><X /></button>
        </div>
      </header>
      <main class="file-viewer-body">
        {state.kind === 'loading' && <div class="file-viewer-status"><Loader2 class="spin" /> Loading preview…</div>}
        {state.kind === 'image' && <div class="file-viewer-image-wrap"><img src={state.url} alt={name} /></div>}
        {state.kind === 'document' && (
          <iframe class="file-viewer-frame" sandbox="" srcdoc={state.srcdoc} referrerpolicy="no-referrer" title={name} />
        )}
        {state.kind === 'expired' && <StatusMessage>El enlace ha caducado (el servidor se reinició) — pídele al agente que lo reenvíe</StatusMessage>}
        {state.kind === 'too-large' && <StatusMessage>Demasiado grande para previsualizar</StatusMessage>}
        {state.kind === 'binary' && <StatusMessage>No se puede previsualizar (parece binario)</StatusMessage>}
        {state.kind === 'error' && <StatusMessage>No se pudo cargar la previsualización</StatusMessage>}
        {['expired', 'too-large', 'binary', 'error'].includes(state.kind) && (
          <button class="file-viewer-download-button" onClick={handleDownload} disabled={downloading}>
            {downloading ? <Loader2 class="spin" /> : <Download />} Descargar
          </button>
        )}
      </main>
    </div>
  );
}

function StatusMessage({ children }) {
  return <div class="file-viewer-status file-viewer-error">{children}</div>;
}

function previewKind(name, mime) {
  const mediaType = (mime || '').split(';', 1)[0].trim().toLowerCase();
  const extension = name.toLowerCase().match(/\.[^.]+$/)?.[0];
  if (mediaType.startsWith('image/')) return 'image';
  if (mediaType === 'text/html' || extension === '.html' || extension === '.htm') return 'html';
  if (mediaType.includes('markdown') || extension === '.md' || extension === '.markdown') return 'markdown';
  return 'text';
}

// readCapped materializes a response body into a Blob, aborting as soon as it
// exceeds max bytes so an oversized (or lying-about-its-size) file can't be
// buffered whole in memory. Rejects with { tooLarge: true } past the cap.
async function readCapped(resp, max) {
  const declared = Number(resp.headers.get('content-length'));
  if (declared && declared > max) throw Object.assign(new Error('too large'), { tooLarge: true });
  const type = resp.headers.get('content-type') || '';
  const reader = resp.body?.getReader?.();
  if (!reader) {
    const blob = await resp.blob();
    if (blob.size > max) throw Object.assign(new Error('too large'), { tooLarge: true });
    return blob;
  }
  const chunks = [];
  let total = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    total += value.length;
    if (total > max) {
      reader.cancel();
      throw Object.assign(new Error('too large'), { tooLarge: true });
    }
    chunks.push(value);
  }
  return new Blob(chunks, { type });
}

function looksBinary(text) {
  if (text.includes('\0')) return true;
  const replacements = (text.match(/\uFFFD/g) || []).length;
  return replacements > 16 && replacements / Math.max(text.length, 1) > 0.01;
}

function escapeHtml(text) {
  return text.replace(/[&<>"']/g, (char) => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
  }[char]));
}

export function buildSrcdoc(kind, content) {
  let body;
  if (kind === 'html') {
    body = content;
  } else if (kind === 'markdown' && content.length <= MAX_HIGHLIGHT_SIZE) {
    body = `<article class="msg-assistant">${renderMarkdown(content)}</article>`;
  } else {
    // Avoid highlightAuto's all-language search on large mobile previews.
    body = `<pre class="plain-text">${escapeHtml(content)}</pre>`;
  }
  return `<!doctype html><html><head><meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; img-src data:"><style>${iframeStyles}</style></head><body>${body}</body></html>`;
}

const iframeStyles = `
:root{color-scheme:light}*{box-sizing:border-box}body{background:#fff;color:#111;margin:16px;font-family:system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;line-height:1.6;word-break:break-word}.msg-assistant{font-size:14px;max-width:100%}.msg-assistant p{margin:0 0 12px}.msg-assistant ul,.msg-assistant ol{padding-left:24px;margin:0 0 12px}.msg-assistant li{margin-bottom:4px}.msg-assistant blockquote{border-left:3px solid #8839ef;padding-left:14px;color:#555;margin:0 0 12px}.msg-assistant strong,.msg-assistant h1,.msg-assistant h2,.msg-assistant h3{color:#111}.msg-assistant h1,.msg-assistant h2,.msg-assistant h3{margin:18px 0 8px}.msg-assistant h1{font-size:20px}.msg-assistant h2{font-size:17px}.msg-assistant h3{font-size:15px}.msg-assistant h1:first-child,.msg-assistant h2:first-child,.msg-assistant h3:first-child{margin-top:0}.msg-assistant a{color:#c75d00}.msg-assistant code{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:12.5px;background:#f1f1f1;padding:2px 5px;border-radius:4px}.msg-assistant pre,.plain-text{margin:10px 0;padding:12px;overflow:auto;background:#f5f5f5;border-radius:6px;font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:12.5px;line-height:1.55;white-space:pre-wrap}.msg-assistant pre code{padding:0;background:none}.msg-assistant table{border-collapse:collapse;font-size:13px}.msg-assistant th,.msg-assistant td{border:1px solid #ccc;padding:6px 10px;text-align:left}.msg-assistant th{background:#eee}.md-table-wrap{overflow-x:auto;margin:10px 0}.code-block{margin:10px 0}.code-block-header{display:none}`;
