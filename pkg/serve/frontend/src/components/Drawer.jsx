import { useState, useMemo, useEffect, useRef } from 'preact/hooks';
import { X, Plus, Check } from 'lucide-preact';
import {
  toggleDrawer, setActiveSession, deleteSession,
  resumeSession, toggleDialog, sessionsByGroup,
} from '../state.js';

export function Drawer({ state }) {
  const [filter, setFilter] = useState('');
  const [confirmId, setConfirmId] = useState(null);
  const timerRef = useRef(null);
  const groups = useMemo(() => sessionsByGroup(state), [state.sessions]);
  const filterLower = filter.toLowerCase();

  // Auto-cancel after 3s
  useEffect(() => {
    if (confirmId) {
      timerRef.current = setTimeout(() => setConfirmId(null), 3000);
      return () => clearTimeout(timerRef.current);
    }
  }, [confirmId]);

  const handleClick = (e, sess) => {
    if (e.target.closest('.drawer-item-delete, .delete-confirm')) return;
    if (confirmId) { setConfirmId(null); return; }
    if (sess.state === 'saved') {
      resumeSession(sess.id).catch(e => console.error('Resume failed:', e));
    } else {
      setActiveSession(sess.id);
    }
    toggleDrawer();
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

  return (
    <>
      <div
        class={`drawer-backdrop ${state.drawerOpen ? 'open' : ''}`}
        onClick={toggleDrawer}
      />
      <div class={`drawer ${state.drawerOpen ? 'open' : ''}`}>
        <div class="drawer-header">
          <span class="drawer-title">Sessions</span>
          <button class="drawer-close" onClick={toggleDrawer}><X /></button>
        </div>

        <input
          class="sidebar-search"
          placeholder="Filter…"
          value={filter}
          onInput={e => setFilter(e.target.value)}
          style="margin: 8px 12px; width: calc(100% - 24px);"
        />

        <div class="drawer-list">
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
                    class={`drawer-item ${state.activeSession === sess.id ? 'active' : ''} ${confirmId === sess.id ? 'confirming' : ''}`}
                    onClick={(e) => handleClick(e, sess)}
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
                        <span class="drawer-item-title">{sess.title || 'Untitled'}</span>
                        {(sess.state === 'permission' || sess.state === 'error') && (
                          <span class="sidebar-attention" />
                        )}
                        <span class="drawer-item-meta">{sess.model}</span>
                        <button
                          class="drawer-item-delete"
                          onClick={(e) => handleDeleteClick(e, sess.id)}
                        ><X /></button>
                      </>
                    )}
                  </div>
                ))}
              </div>
            );
          })}
        </div>

        <div class="drawer-footer">
          <button class="drawer-new-btn" onClick={() => { toggleDialog(); toggleDrawer(); }}>
            <Plus /> New Session
          </button>
        </div>
      </div>
    </>
  );
}
