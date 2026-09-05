import { useEffect, useState } from 'preact/hooks';
import { Loader2 } from 'lucide-preact';
import { renderMarkdown } from '../../data/util/markdown.js';
import { readCapped, MAX_PREVIEW_SIZE, MAX_HIGHLIGHT_SIZE } from '../../data/util/file-preview.js';
import { buildHTMLSrcdoc, HTML_PREVIEW_SANDBOX } from '../../data/util/html-preview.js';
import { looksBinary } from '../../data/util/file-card.js';
import { usePinchZoom } from '../../hooks/usePinchZoom.js';
import { artifactFailure, artifactKind, artifactRevision } from '../../data/artifacts-model.js';
import { ShareButton } from './ArtifactRow.jsx';

// ARTIFACT_ESCAPE — the message an HTML artifact may post to ask the reader to
// close, mirroring the LivePreview inspector's bridge. Only a message coming
// from THIS iframe's contentWindow is honoured (see ArtifactsDrawer).
export const ARTIFACT_ESCAPE = 'moa-artifact-escape';

// artifactRevision moved to data/artifacts-model.js (pure, shared with the
// drawer and testable without pulling a component's hook runtime in).

// ArtifactContent — reads the CURRENT bytes of the reference. The stored size
// is the last observed one and the file may have grown or shrunk since, so the
// cap is enforced against the actual response (readCapped), never against the
// metadata the card was created with.
export function ArtifactContent({ artifact, onEscapeFrame }) {
  const [state, setState] = useState({ kind: 'loading' });
  const [attempt, setAttempt] = useState(0);
  const revision = artifactRevision(artifact);
  // Same in-element pinch/pan/double-tap as the file viewer: an image opened
  // here must stay readable without zooming the whole app.
  const { containerRef, contentRef, zoomed } = usePinchZoom();

  useEffect(() => {
    let cancelled = false;
    let imageURL;
    setState({ kind: 'loading' });
    fetch(artifact.url, { cache: 'no-store' }).then(async (response) => {
      if (!response.ok) {
        throw Object.assign(new Error(`artifact failed: ${response.status}`), { status: response.status });
      }
      const blob = await readCapped(response, MAX_PREVIEW_SIZE);
      const kind = artifactKind(artifact);
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
      if (kind === 'html') {
        // Authored HTML keeps its own look inside the existing preview
        // boundary: same sandbox and same CSP as every other HTML preview.
        setState({ kind: 'html', srcdoc: buildHTMLSrcdoc(text + ESCAPE_BRIDGE, '') });
        return;
      }
      if (kind === 'markdown' && text.length <= MAX_HIGHLIGHT_SIZE) {
        setState({ kind: 'markdown', html: renderMarkdown(text) });
        return;
      }
      setState({ kind: 'text', text });
    }).catch((error) => {
      if (cancelled) return;
      if (error?.tooLarge) setState({ kind: 'too-large' });
      else if (error?.status === 410) setState({ kind: 'unavailable' });
      else if (error?.status === 404) setState({ kind: 'missing' });
      else setState({ kind: 'error' });
    });
    return () => {
      cancelled = true;
      if (imageURL) URL.revokeObjectURL(imageURL);
    };
  }, [revision, attempt]);

  if (state.kind === 'loading') {
    return <div class="af-loading" role="status"><Loader2 class="spin" size={16} /> Opening…</div>;
  }
  if (state.kind === 'image') {
    return (
      <div class={`af-image-wrap${zoomed ? ' is-zoomed' : ''}`} ref={containerRef}>
        <img src={state.url} alt={artifact.name} ref={contentRef} draggable={false} />
      </div>
    );
  }
  if (state.kind === 'html') {
    return (
      <iframe
        class="af-document-frame"
        title={artifact.title}
        sandbox={HTML_PREVIEW_SANDBOX}
        referrerpolicy="no-referrer"
        srcdoc={state.srcdoc}
        onLoad={onEscapeFrame}
      />
    );
  }
  if (state.kind === 'markdown') {
    // Markdown reads like an assistant answer: the product's own .doc styles
    // on Mocha, not an embedded light theme.
    return (
      <div class="af-markdown-scroll">
        <article class="doc af-markdown" dangerouslySetInnerHTML={{ __html: state.html }} />
      </div>
    );
  }
  if (state.kind === 'text') {
    return <div class="af-markdown-scroll"><pre class="af-plain">{state.text}</pre></div>;
  }

  // Each failure names the next action; artifactFailure owns that contract.
  const failure = artifactFailure(state.kind);
  return (
    <div class="af-empty" role="alert">
      <h2>Not shown</h2>
      <p>{failure.message}</p>
      <div class="af-empty-actions">
        {failure.retryable && <button type="button" class="af-text-button" onClick={() => setAttempt((n) => n + 1)}>Retry</button>}
        {failure.shareable && <ShareButton artifact={artifact} labelled />}
      </div>
    </div>
  );
}

// The bridge is the same idea as the LivePreview inspector: the sandboxed
// document tells the host that Escape was pressed inside it, and the host only
// acts on a message whose source is that very iframe. It rides in the body,
// under the preview's existing CSP (which already allows inline scripts), and
// leaves the document's own appearance untouched.
export const ESCAPE_BRIDGE = `<script>document.addEventListener('keydown',function(e){if(e.key==='Escape')parent.postMessage({type:'${ARTIFACT_ESCAPE}'},'*');});<\/script>`;
