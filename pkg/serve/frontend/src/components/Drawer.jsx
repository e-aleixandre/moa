import { useState, useMemo } from 'preact/hooks';
import {
  toggleDrawer, setActiveSession, deleteSession,
  resumeSession, toggleDialog, sessionsByGroup,
} from '../state.js';

export function Drawer({ state }) {
  const [filter, setFilter] = useState('');
  const groups = useMemo(() => sessionsByGroup(state), [state.sessions]);
  const filterLower = filter.toLowerCase();

  const handleClick = (sess) => {
    if (sess.state === 'saved') {
      resumeSession(sess.id).catch(e => console.error('Resume failed:', e));
    } else {
      setActiveSession(sess.id);
    }
    toggleDrawer();
  };

  const handleDelete = (e, id) => {
    e.stopPropagation();
    deleteSession(id).catch(err => console.error('Delete failed:', err));
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
          <button class="drawer-close" onClick={toggleDrawer}>×</button>
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
                    class={`drawer-item ${state.activeSession === sess.id ? 'active' : ''}`}
                    onClick={() => handleClick(sess)}
                  >
                    <span class={`state-dot ${sess.state}`} />
                    <span class="drawer-item-title">{sess.title || 'Untitled'}</span>
                    {(sess.state === 'permission' || sess.state === 'error') && (
                      <span class="sidebar-attention" />
                    )}
                    <span class="drawer-item-meta">{sess.model}</span>
                    <button
                      class="drawer-item-delete"
                      onClick={(e) => handleDelete(e, sess.id)}
                    >×</button>
                  </div>
                ))}
              </div>
            );
          })}
        </div>

        <div class="drawer-footer">
          <button class="drawer-new-btn" onClick={() => { toggleDialog(); toggleDrawer(); }}>
            + New Session
          </button>
        </div>
      </div>
    </>
  );
}
