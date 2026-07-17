import { useEffect, useRef, useState } from 'preact/hooks';
import { Loader2, X } from 'lucide-preact';
import { extractExternalResources } from '../util/html-resources.js';
import { readCapped } from '../util/file-preview.js';

const MAX_INSPECT_SIZE = 2 * 1024 * 1024;
const labels = { script: 'Scripts', style: 'Styles', font: 'Fonts', image: 'Images', media: 'Media' };

export function HtmlResourceInfo({ name, url, onClose }) {
  const [state, setState] = useState({ kind: 'loading' });
  const closeButtonRef = useRef(null);
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;

  useEffect(() => {
    let cancelled = false;
    const previousOverflow = document.body.style.overflow;
    const previousFocus = document.activeElement;
    document.body.style.overflow = 'hidden';
    closeButtonRef.current?.focus();
    const marker = `hri-${Date.now()}-${Math.random().toString(36).slice(2)}`;
    history.pushState({ htmlResourceInfo: marker }, '');
    let closedByPop = false;
    const closeOnBack = () => { closedByPop = true; onCloseRef.current(); };
    fetch(url).then(async (resp) => {
      if (!resp.ok) throw new Error(`inspect failed: ${resp.status}`);
      const blob = await readCapped(resp, MAX_INSPECT_SIZE);
      const report = extractExternalResources(await blob.text());
      if (!cancelled) setState({ kind: 'ready', report });
    }).catch((error) => {
      if (!cancelled) setState({ kind: error?.tooLarge ? 'too-large' : 'error' });
    });
    const onKeyDown = (event) => { if (event.key === 'Escape') onCloseRef.current(); };
    addEventListener('keydown', onKeyDown);
    addEventListener('popstate', closeOnBack);
    return () => {
      cancelled = true;
      document.body.style.overflow = previousOverflow;
      removeEventListener('keydown', onKeyDown);
      removeEventListener('popstate', closeOnBack);
      if (!closedByPop && history.state?.htmlResourceInfo === marker) history.back();
      previousFocus?.focus?.();
    };
  }, [url]);

  return (
    <div class="html-resource-overlay" role="dialog" aria-modal="true" aria-label={`External resources in ${name}`} onClick={onClose}>
      <section class="html-resource-sheet" onClick={(event) => event.stopPropagation()}>
        <header class="html-resource-header">
          <div><strong>External resources</strong><span>{name}</span></div>
          <button ref={closeButtonRef} onClick={() => history.back()} aria-label="Close external resources" title="Close"><X /></button>
        </header>
        <div class="html-resource-content">
          <p class="html-resource-notice">Informational only. Opening the preview loads detected HTTPS resources automatically. The preview remains sandboxed from moa, but its code may make network connections.</p>
          {state.kind === 'loading' && <div class="html-resource-status"><Loader2 class="spin" /> Inspecting HTML…</div>}
          {state.kind === 'too-large' && <div class="html-resource-status">This HTML file is too large to inspect.</div>}
          {state.kind === 'error' && <div class="html-resource-status">Could not inspect this HTML file.</div>}
          {state.kind === 'ready' && <ResourceReport report={state.report} />}
        </div>
      </section>
    </div>
  );
}

function ResourceReport({ report }) {
  return <>
    <h2>HTTPS domains ({report.domains.length})</h2>
    {report.domains.length ? <ul class="html-resource-domains">{report.domains.map((domain) => <li key={domain}>{domain}</li>)}</ul> : <p class="html-resource-empty">No external HTTPS resources detected.</p>}
    {Object.keys(labels).map((type) => {
      const resources = report.resources.filter((item) => item.type === type);
      if (!resources.length) return null;
      return <section class="html-resource-group" key={type}><h2>{labels[type]} ({resources.length})</h2><ul>{resources.map((item) => <li key={`${item.type}-${item.url}`}><span>{item.domain}</span><code>{item.url}</code></li>)}</ul></section>;
    })}
    {report.insecure.length > 0 && <section class="html-resource-group html-resource-blocked"><h2>Non-HTTPS resources ({report.insecure.length})</h2><p>These are listed for information and are blocked in the preview.</p><ul>{report.insecure.map((item) => <li key={`${item.type}-${item.url}`}><code>{item.url}</code></li>)}</ul></section>}
  </>;
}
