import { useEffect, useState } from 'preact/hooks';
import { Loader2 } from 'lucide-preact';
import { Sheet } from '../Sheet/Sheet.jsx';
import { extractExternalResources } from '../../data/util/html-resources.js';
import { readCapped } from '../../data/util/file-preview.js';
import './HtmlResourceInfo.css';

const MAX_INSPECT_SIZE = 2 * 1024 * 1024;
const labels = { script: 'Scripts', style: 'Styles', font: 'Fonts', image: 'Images', media: 'Media' };

// HtmlResourceInfo — informational inspector for an HTML preview: lists the
// external HTTPS resources (scripts/styles/fonts/images/media) the file would
// load if previewed, plus any non-HTTPS resources (blocked by the preview's
// sandbox/CSP). Opening the preview loads these automatically — this panel
// only tells the user what to expect, it never fetches the resources itself.
// The back-gesture/history binding lives once in the Sheet this component
// renders into (data/overlay-history.js via components/Sheet/Sheet.jsx).
export function HtmlResourceInfo({ name, url, onClose }) {
  const [state, setState] = useState({ kind: 'loading' });

  useEffect(() => {
    let cancelled = false;
    fetch(url).then(async (resp) => {
      if (!resp.ok) throw new Error(`inspect failed: ${resp.status}`);
      const blob = await readCapped(resp, MAX_INSPECT_SIZE);
      const report = extractExternalResources(await blob.text());
      if (!cancelled) setState({ kind: 'ready', report });
    }).catch((error) => {
      if (!cancelled) setState({ kind: error?.tooLarge ? 'too-large' : 'error' });
    });
    return () => { cancelled = true; };
  }, [url]);

  return (
    <Sheet open onClose={onClose} title="External resources" ariaLabel={`External resources in ${name}`} class="html-resource-sheet">
      <p class="html-resource-name" title={name}>{name}</p>
      <p class="html-resource-notice">
        Informational only. Opening the preview loads detected HTTPS resources automatically. The preview
        remains sandboxed from moa, but its code may make network connections.
      </p>
      {state.kind === 'loading' && <div class="html-resource-status"><Loader2 class="spin" /> Inspecting HTML…</div>}
      {state.kind === 'too-large' && <div class="html-resource-status">This HTML file is too large to inspect.</div>}
      {state.kind === 'error' && <div class="html-resource-status">Could not inspect this HTML file.</div>}
      {state.kind === 'ready' && <ResourceReport report={state.report} />}
    </Sheet>
  );
}

function ResourceReport({ report }) {
  return (
    <>
      <h4>HTTPS domains ({report.domains.length})</h4>
      {report.domains.length
        ? <ul class="html-resource-domains">{report.domains.map((domain) => <li key={domain}>{domain}</li>)}</ul>
        : <p class="html-resource-empty">No external HTTPS resources detected.</p>}
      {Object.keys(labels).map((type) => {
        const resources = report.resources.filter((item) => item.type === type);
        if (!resources.length) return null;
        return (
          <section class="html-resource-group" key={type}>
            <h4>{labels[type]} ({resources.length})</h4>
            <ul>
              {resources.map((item) => (
                <li key={`${item.type}-${item.url}`}><span>{item.domain}</span><code>{item.url}</code></li>
              ))}
            </ul>
          </section>
        );
      })}
      {report.insecure.length > 0 && (
        <section class="html-resource-group html-resource-blocked">
          <h4>Non-HTTPS resources ({report.insecure.length})</h4>
          <p>These are listed for information and are blocked in the preview.</p>
          <ul>
            {report.insecure.map((item) => <li key={`${item.type}-${item.url}`}><code>{item.url}</code></li>)}
          </ul>
        </section>
      )}
    </>
  );
}
