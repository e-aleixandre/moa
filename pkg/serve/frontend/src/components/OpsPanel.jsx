import { useCallback, useEffect, useRef, useState } from 'preact/hooks';
import { AlertTriangle, RefreshCw, ShieldCheck, X } from 'lucide-preact';
import { opsProjectLabel, sessionStatusLabel } from '../ops-data.js';

async function getOps(path, signal) {
  const response = await fetch(path, { signal, headers: { 'X-Moa-Request': '1' } });
  if (!response.ok) throw new Error(`Ops request failed (${response.status})`);
  return response.json();
}

export function OpsPanel({ open, onClose }) {
  const [data, setData] = useState(null);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);
  const requestRef = useRef(null);

  const load = useCallback(() => {
    requestRef.current?.abort();
    const controller = new AbortController();
    requestRef.current = controller;
    setLoading(true);
    setError('');
    Promise.all([
      getOps('/api/ops?view=sitrep', controller.signal),
      getOps('/api/ops?view=blockers', controller.signal),
      getOps('/api/ops/overview', controller.signal),
    ]).then(([sitrep, blockers, overview]) => {
      if (requestRef.current === controller) setData({ sitrep, blockers, overview });
    }).catch((err) => {
      if (err.name !== 'AbortError' && requestRef.current === controller) setError(err.message || 'Unable to load Ops');
    }).finally(() => {
      if (requestRef.current === controller) setLoading(false);
    });
  }, []);

  useEffect(() => {
    if (open) load();
    else requestRef.current?.abort();
    return () => requestRef.current?.abort();
  }, [open, load]);

  if (!open) return null;
  const projects = data?.overview?.projects || [];
  const blockers = data?.blockers?.blockers || [];

  return (
    <div class="ops-overlay" onClick={(e) => { if (e.target === e.currentTarget) onClose(); }}>
      <section class="ops-panel" role="dialog" aria-modal="true" aria-label="Ops overview">
        <header class="ops-header">
          <div><ShieldCheck /><span>Ops</span></div>
          <div class="ops-header-actions">
            <button class="ops-icon-button" onClick={load} disabled={loading} title="Refresh Ops"><RefreshCw class={loading ? 'spinning' : ''} /></button>
            <button class="ops-icon-button" onClick={onClose} title="Close Ops"><X /></button>
          </div>
        </header>
        {loading && !data && <div class="ops-state">Loading verified status…</div>}
        {error && <div class="ops-state ops-error">{error}<button onClick={load}>Try again</button></div>}
        {data && !error && <div class="ops-content">
          <p class="ops-sitrep">{data.sitrep?.spoken || 'Ops status is available.'}</p>
          <section class="ops-blockers" aria-label="Blockers">
            <div class="ops-section-title"><AlertTriangle /> Blockers</div>
            {blockers.length ? blockers.map(blocker => (
              <div class="ops-blocker" key={`${blocker.kind}-${blocker.session_id}`}>
                <strong>{blocker.title || 'Untitled'}</strong><span>{blocker.kind.replaceAll('_', ' ')}</span>
              </div>
            )) : <div class="ops-empty">No verified blockers.</div>}
          </section>
          <section aria-label="Project status">
            <div class="ops-section-title">Projects</div>
            {projects.length ? projects.map(project => (
              <div class="ops-project" key={project.canonical_cwd}>
                <div class="ops-project-title" title={project.canonical_cwd}>{opsProjectLabel(project.canonical_cwd)}</div>
                {(project.sessions || []).map(session => (
                  <div class="ops-session" key={session.id}>
                    <strong>{session.title || 'Untitled'}</strong>
                    <span>{sessionStatusLabel(session)}</span>
                  </div>
                ))}
              </div>
            )) : <div class="ops-empty">No active projects.</div>}
          </section>
        </div>}
      </section>
    </div>
  );
}
