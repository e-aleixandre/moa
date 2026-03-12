import { useState, useMemo, useEffect, useRef } from 'preact/hooks';
import { PanelLeftClose, PanelLeft, X, Plus, Check } from 'lucide-preact';
import {
  assignTile, deleteSession, resumeSession,
  toggleDialog, toggleSidebar, sessionsByGroup,
} from '../state.js';
import { formatShortcut } from '../hooks/useHotkeys.js';

export function Sidebar({ state }) {
  const [filter, setFilter] = useState('');
  const [confirmId, setConfirmId] = useState(null);
  const timerRef = useRef(null);
  const groups = useMemo(() => sessionsByGroup(state), [state.sessions]);

  // Auto-cancel confirmation after 3s
  useEffect(() => {
    if (confirmId) {
      timerRef.current = setTimeout(() => setConfirmId(null), 3000);
      return () => clearTimeout(timerRef.current);
    }
  }, [confirmId]);

  const isInTile = (id) => state.tileAssignments.includes(id);

  const handleClick = (e, sess) => {
    // Ignore clicks on delete/confirm buttons (already handled)
    if (e.target.closest('.sidebar-item-delete, .delete-confirm')) return;
    if (confirmId) { setConfirmId(null); return; }
    if (sess.state === 'saved') {
      resumeSession(sess.id).catch(e => console.error('Resume failed:', e));
      return;
    }
    const idx = state.tileAssignments.indexOf(sess.id);
    if (idx >= 0) {
      import('../state.js').then(m => m.focusTile(idx));
      return;
    }
    assignTile(state.focusedTile, sess.id);
  };

  const handleDeleteClick = (e, id) => {
    e.stopPropagation();
    setConfirmId(id);
  };

  const handleConfirm = (e, id) => {
    e.stopPropagation();
    setConfirmId(null);
    deleteSession(id).catch(err => console.error('Delete failed:', err));
  };

  const handleCancel = (e) => {
    e.stopPropagation();
    setConfirmId(null);
  };

  const filterLower = filter.toLowerCase();

  return (
    <div class={`sidebar ${state.sidebarOpen ? '' : 'collapsed'}`}>
      <div class="sidebar-header">
        <span class="sidebar-logo">moa</span>
        <button class="sidebar-toggle" onClick={toggleSidebar} title={`Toggle sidebar (${formatShortcut('B', { ctrl: true })})`}>
          {state.sidebarOpen ? <PanelLeftClose /> : <PanelLeft />}
          {state.sidebarOpen && <kbd class="shortcut-hint">{formatShortcut('B', { ctrl: true })}</kbd>}
        </button>
      </div>

      {state.sidebarOpen && (
        <>
          <input
            class="sidebar-search"
            placeholder="Filter sessions…"
            value={filter}
            onInput={e => setFilter(e.target.value)}
          />

          <div class="sidebar-list">
            {Object.entries(groups).map(([cwd, sessions]) => {
              const filtered = sessions.filter(s => {
                if (!filterLower) return true;
                return (s.title || '').toLowerCase().includes(filterLower) ||
                  (s.model || '').toLowerCase().includes(filterLower) ||
                  cwd.toLowerCase().includes(filterLower);
              });
              if (filtered.length === 0) return null;
              const label = cwd.split('/').pop() || cwd;

              return (
                <div key={cwd}>
                  <div class="sidebar-group-label" title={cwd}>{label}</div>
                  {filtered.map(sess => (
                    <div
                      key={sess.id}
                      class={`sidebar-item ${isInTile(sess.id) ? 'active' : ''} ${sess.state === 'saved' ? 'saved' : ''} ${confirmId === sess.id ? 'confirming' : ''}`}
                      onClick={(e) => handleClick(e, sess)}
                      draggable={sess.state !== 'saved'}
                      onDragStart={(e) => {
                        e.dataTransfer.setData('text/x-session-id', sess.id);
                        e.dataTransfer.effectAllowed = 'move';
                      }}
                    >
                      {confirmId === sess.id ? (
                        <div class="delete-confirm">
                          <span class="delete-confirm-text">Delete?</span>
                          <button class="delete-confirm-yes" onClick={(e) => handleConfirm(e, sess.id)}><Check /></button>
                          <button class="delete-confirm-no" onClick={handleCancel}><X /></button>
                        </div>
                      ) : (
                        <>
                          <span class={`state-dot ${sess.state}`} />
                          <span class="sidebar-item-title">{sess.title || 'Untitled'}</span>
                          {(sess.state === 'permission' || sess.state === 'error') && (
                            <span class="sidebar-attention" />
                          )}
                          {sess.subagentCount > 0 && (
                            <span class="subagent-badge">{sess.subagentCount}</span>
                          )}
                          <button
                            class="sidebar-item-delete"
                            onClick={(e) => handleDeleteClick(e, sess.id)}
                            title="Delete"
                          ><X /></button>
                        </>
                      )}
                    </div>
                  ))}
                </div>
              );
            })}

            {Object.keys(state.sessions).length === 0 && (
              <div class="empty-state">
                <p>No sessions yet.</p>
                <p>Create one to get started.</p>
              </div>
            )}
          </div>

          <div class="sidebar-footer">
            <button class="sidebar-new-btn" onClick={toggleDialog}>
              <Plus /> New Session
              <kbd class="shortcut-hint">{formatShortcut('N', { ctrl: true })}</kbd>
            </button>
          </div>
        </>
      )}
    </div>
  );
}
