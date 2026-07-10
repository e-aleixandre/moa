import { useCallback, useEffect, useRef, useState } from 'preact/hooks';
import { AlertTriangle, ArrowRight, RefreshCw, Send, ShieldCheck, X } from 'lucide-preact';
import { opsProjectLabel, sessionStatusLabel } from '../ops-data.js';
import { applyOpsSnapshot, nextOpsReconnectDelay } from '../ops-stream.js';
import { newInstructionRequestID, opsSessions, submitOpsInstruction } from '../ops-instruction.js';

const OPS_WS_INITIAL_BACKOFF = 1000;
const OPS_WS_MAX_BACKOFF = 16000;

async function getOps(path, signal) {
  const response = await fetch(path, { signal, headers: { 'X-Moa-Request': '1' } });
  if (!response.ok) throw new Error(`Ops request failed (${response.status})`);
  return response.json();
}

export function OpsPanel({ open, onClose, onNavigate }) {
  const [data, setData] = useState(null);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);
  const requestRef = useRef(null);
  const streamVersionRef = useRef(0);
  const instructionIDRef = useRef('');
  const [targetID, setTargetID] = useState('');
  const [instruction, setInstruction] = useState('');
  const [sending, setSending] = useState(false);
  const [outcome, setOutcome] = useState(null);

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
      if (requestRef.current === controller) {
        setData(current => ({
          sitrep,
          blockers,
          overview: streamVersionRef.current > 0 && current?.overview ? current.overview : overview,
          streamVersion: current?.streamVersion || 0,
        }));
      }
    }).catch((err) => {
      if (err.name !== 'AbortError' && requestRef.current === controller) setError('Unable to load verified Ops status.');
    }).finally(() => {
      if (requestRef.current === controller) setLoading(false);
    });
  }, []);

  useEffect(() => {
    if (!open) {
      requestRef.current?.abort();
      return undefined;
    }

    streamVersionRef.current = 0;
    setData(current => current ? { ...current, streamVersion: 0 } : current);
    load();

    let stopped = false;
    let connected = false;
    let backoff = OPS_WS_INITIAL_BACKOFF;
    let ws;
    let retryTimer;

    const connect = () => {
      const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
      ws = new WebSocket(`${proto}//${location.host}/api/ops/ws`);

      ws.onopen = () => {
        connected = true;
        backoff = OPS_WS_INITIAL_BACKOFF;
      };
      ws.onmessage = (message) => {
        let event;
        try {
          event = JSON.parse(message.data);
        } catch {
          return;
        }
        if (!Number.isSafeInteger(event?.version) || event.version <= streamVersionRef.current) return;
        setData(current => {
          const next = applyOpsSnapshot(current, event);
          if (next !== current) streamVersionRef.current = event.version;
          return next;
        });
      };
      ws.onerror = () => ws.close();
      ws.onclose = () => {
        if (stopped || !connected) return;
        const delay = backoff;
        backoff = nextOpsReconnectDelay(backoff, OPS_WS_MAX_BACKOFF);
        retryTimer = setTimeout(() => {
          retryTimer = undefined;
          if (!stopped) connect();
        }, delay);
      };
    };

    connect();
    return () => {
      stopped = true;
      requestRef.current?.abort();
      if (retryTimer) clearTimeout(retryTimer);
      ws?.close();
    };
  }, [open, load]);

  const sessions = opsSessions(data?.overview);
  const target = sessions.find(session => session.id === targetID);

  useEffect(() => {
    if (targetID && !sessions.some(session => session.id === targetID)) {
      setTargetID('');
      setOutcome({ kind: 'no-match' });
    }
  }, [targetID, data?.overview]);

  const selectTarget = (id) => {
    instructionIDRef.current = '';
    setTargetID(id);
    setOutcome(null);
  };

  const sendInstruction = async (event) => {
    event.preventDefault();
    const text = instruction.trim();
    if (!target || !text || sending) return;
    setSending(true);
    setOutcome(null);
    if (!instructionIDRef.current) instructionIDRef.current = newInstructionRequestID();
    try {
      const result = await submitOpsInstruction({ target: target.id, text, request_id: instructionIDRef.current });
      setOutcome(result);
      if (result.kind === 'send' || result.kind === 'steer') {
        setInstruction('');
        instructionIDRef.current = '';
      }
    } catch {
      setOutcome({ kind: 'unavailable' });
    } finally {
      setSending(false);
    }
  };

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
          <section class="ops-instruction" aria-label="Directed instruction">
            <div class="ops-section-title">Directed instruction</div>
            <p class="ops-instruction-help">Select one verified session, then send a short instruction. This does not start a chat.</p>
            <form onSubmit={sendInstruction}>
              <label class="ops-instruction-label" for="ops-target">Target</label>
              <select id="ops-target" value={targetID} onChange={(event) => selectTarget(event.currentTarget.value)} disabled={!sessions.length || sending}>
                <option value="">Select a verified session…</option>
                {sessions.map(session => <option value={session.id} key={session.id}>{session.title} — {opsProjectLabel(session.project)}</option>)}
              </select>
              <label class="ops-instruction-label" for="ops-text">Instruction</label>
              <textarea id="ops-text" value={instruction} maxLength="280" rows="2" placeholder="Short, directed instruction" onInput={(event) => { instructionIDRef.current = ''; setInstruction(event.currentTarget.value); setOutcome(null); }} disabled={!target || sending} />
              <div class="ops-instruction-actions">
                {target && <button type="button" class="ops-open-target" onClick={() => onNavigate?.(target.id)}><ArrowRight /> Open target</button>}
                <button class="ops-send" type="submit" disabled={!target || !instruction.trim() || sending}><Send />{sending ? 'Sending…' : 'Send instruction'}</button>
              </div>
            </form>
            {outcome && <InstructionOutcome outcome={outcome} target={target} onNavigate={onNavigate} onRefresh={load} />}
          </section>
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
                  <button class={`ops-session ${targetID === session.id ? 'selected' : ''}`} key={session.id} onClick={() => selectTarget(session.id)}>
                    <strong>{session.title || 'Untitled'}</strong>
                    <span>{sessionStatusLabel(session)}</span>
                  </button>
                ))}
              </div>
            )) : <div class="ops-empty">No active projects.</div>}
          </section>
        </div>}
      </section>
    </div>
  );
}

function InstructionOutcome({ outcome, target, onNavigate, onRefresh }) {
  const messages = {
    send: 'Instruction sent to the selected session.',
    steer: 'Steering instruction sent to the selected session.',
    permission: 'Permission is needed before this instruction can be applied.',
    ambiguous: 'The target needs review. Select one verified session and try again.',
    'no-match': 'That verified session is no longer available. Refresh Ops and select again.',
    invalid: 'Use a short instruction and try again.',
    'rate-limited': 'Please wait a moment before sending another instruction.',
    unavailable: 'Instruction was not sent. Try again when Ops is available.',
  };
  const canOpen = target && (outcome.kind === 'permission' || outcome.kind === 'send' || outcome.kind === 'steer');
  return <div class={`ops-instruction-outcome ${outcome.kind}`} role="status">
    <span>{messages[outcome.kind]}</span>
    {canOpen && <button onClick={() => onNavigate?.(target.id)}>Open target</button>}
    {outcome.kind === 'no-match' && <button onClick={onRefresh}>Refresh</button>}
  </div>;
}
