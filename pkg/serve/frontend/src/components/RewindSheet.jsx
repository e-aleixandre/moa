import { useState, useEffect } from 'preact/hooks';
import { History, GitBranch, Check, X } from 'lucide-preact';
import { fetchBranchPoints, branchTo } from '../session-actions.js';
import { addToast } from '../notifications.js';

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

  return (
    <div class="rewind-overlay" onClick={onClose}>
      <div class="rewind-sheet" onClick={(e) => e.stopPropagation()}>
        <div class="rewind-header">
          <span class="rewind-title"><History /> Rewind conversation</span>
          <button class="rewind-close" onClick={onClose} title="Close"><X /></button>
        </div>
        <div class="rewind-hint">
          Start a new branch from an earlier turn. Your current history is kept.
        </div>
        <div class="rewind-list">
          {!points && <div class="rewind-loading">Loading…</div>}
          {points && points.length === 0 && <div class="rewind-empty">No branch points yet</div>}
          {points && points.map((p) => (
            <button
              key={p.entry_id}
              class={`rewind-item ${p.is_current_path ? 'current' : ''}`}
              onClick={() => handleSelect(p.entry_id)}
              disabled={busy}
            >
              <span class="rewind-role">{p.role === 'assistant' ? '🤖' : '💬'}</span>
              <span class="rewind-label">{p.label || '(empty)'}</span>
              {p.branch_count > 0 && (
                <span class="rewind-branches" title={`${p.branch_count} branch${p.branch_count > 1 ? 'es' : ''}`}>
                  <GitBranch />{p.branch_count}
                </span>
              )}
              {p.is_current_path && <Check class="rewind-current-icon" />}
            </button>
          ))}
        </div>
      </div>
    </div>
  );
}
