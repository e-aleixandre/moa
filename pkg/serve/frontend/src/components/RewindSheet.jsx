import { useState, useEffect } from 'preact/hooks';
import { History, GitBranch, X } from 'lucide-preact';
import { fetchBranchPoints, branchTo } from '../session-actions.js';
import { addToast } from '../notifications.js';

// timeAgo renders a compact relative time from a Unix-seconds timestamp
// (BranchPoint.Timestamp is e.Timestamp.Unix()).
function timeAgo(sec) {
  if (!sec) return '';
  const diff = Date.now() - sec * 1000;
  const m = Math.floor(diff / 60000);
  if (m < 1) return 'just now';
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  return `${Math.floor(h / 24)}d ago`;
}

// Subagent turns are injected as user messages whose first line is a fixed
// marker (see pkg/bootstrap/bootstrap.go and the TUI's parser in
// app_viewport.go). Detect them so the long "[subagent completed] Job sa-…"
// labels render as a tidy tag + monospace id instead of raw text.
const SUBAGENT_MARKERS = [
  ['[subagent completed] ', 'done', 'ok'],
  ['[subagent failed] ', 'failed', 'err'],
  ['[subagent cancelled] ', 'cancelled', 'muted'],
];

function parseSubagent(label) {
  for (const [prefix, word, kind] of SUBAGENT_MARKERS) {
    if (label.startsWith(prefix)) {
      return { tag: `subagent · ${word}`, rest: label.slice(prefix.length), kind };
    }
  }
  return null;
}

// RewindSheet lists the conversation's branch points and lets the user start a
// new branch from an earlier turn. Mirrors the TUI branch picker: branching is
// non-destructive — the current path is kept and a new one starts from the
// chosen point.
export function RewindSheet({ sessionId, open, onClose }) {
  const [points, setPoints] = useState(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!open || !sessionId) return;
    setPoints(null);
    fetchBranchPoints(sessionId)
      .then(p => setPoints(p || []))
      .catch(() => setPoints([]));
  }, [open, sessionId]);

  if (!open) return null;

  const handleSelect = async (entryId) => {
    if (busy) return;
    setBusy(true);
    try {
      await branchTo(sessionId, entryId);
      onClose();
    } catch (e) {
      addToast({ title: 'Rewind failed', detail: e.message, type: 'error' });
    } finally {
      setBusy(false);
    }
  };

  // The tip of the current path (last on-path turn) gets the single "here"
  // marker — one anchor, not a check repeated on every current-path row.
  let tipIndex = -1;
  if (points) {
    for (let i = 0; i < points.length; i++) {
      if (points[i].is_current_path) tipIndex = i;
    }
  }
  const forks = points ? points.filter(p => p.branch_count > 1).length : 0;

  return (
    <div class="rewind-overlay" onClick={onClose}>
      <div class="rewind-sheet" onClick={(e) => e.stopPropagation()}>
        <div class="rewind-grabber" />
        <div class="rewind-header">
          <div class="rewind-head-icon"><History /></div>
          <div class="rewind-head-titles">
            <span class="rewind-title">Rewind conversation</span>
            {points && points.length > 0 && (
              <span class="rewind-sub">
                {points.length} point{points.length > 1 ? 's' : ''}
                {forks > 0 && ` · ${forks} branch${forks > 1 ? 'es' : ''}`}
              </span>
            )}
          </div>
          <button class="rewind-close" onClick={onClose} title="Close"><X /></button>
        </div>
        <div class="rewind-hint">
          Tap a point to start a new branch from there. Your current conversation is kept.
        </div>
        {points && points.length > 0 && (
          <div class="rewind-legend">
            <span><i class="rewind-dot user" /> you</span>
            <span><i class="rewind-dot assistant" /> assistant</span>
          </div>
        )}
        <div class="rewind-timeline">
          {!points && <div class="rewind-loading">Loading…</div>}
          {points && points.length === 0 && <div class="rewind-empty">No branch points yet</div>}
          {points && points.map((p, i) => {
            const sys = parseSubagent(p.label || '');
            const cls = [
              'rewind-point',
              i === 0 ? 'first' : '',
              i === points.length - 1 ? 'last' : '',
              p.is_current_path ? 'on' : 'off',
              i === tipIndex ? 'here' : '',
              p.role === 'assistant' ? 'assistant' : 'user',
            ].filter(Boolean).join(' ');
            return (
              <button
                key={p.entry_id}
                class={cls}
                onClick={() => handleSelect(p.entry_id)}
                disabled={busy}
              >
                <span class="rewind-rail"><span class="rewind-node" /></span>
                <span class="rewind-body">
                  {sys ? (
                    <span class="rewind-sys">
                      <span class={`rewind-tag ${sys.kind}`}>{sys.tag}</span>
                      <span class="rewind-sys-id">{sys.rest || '(empty)'}</span>
                    </span>
                  ) : (
                    <span class="rewind-label">{p.label || '(empty)'}</span>
                  )}
                  <span class="rewind-meta">
                    {timeAgo(p.timestamp)}
                    {p.branch_count > 1 && (
                      <span class="rewind-chip branches">
                        <GitBranch />{p.branch_count} branches
                      </span>
                    )}
                    {i === tipIndex && <span class="rewind-chip here">you are here</span>}
                  </span>
                </span>
                <span class="rewind-go">new branch <GitBranch /></span>
              </button>
            );
          })}
        </div>
      </div>
    </div>
  );
}
