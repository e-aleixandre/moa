import { useState, useMemo } from 'preact/hooks';
import { PanelLeftClose, PanelLeft, X, Plus } from 'lucide-preact';
import {
  assignTile, deleteSession, resumeSession,
  toggleDialog, toggleSidebar, sessionsByGroup,
} from '../state.js';

export function Sidebar({ state }) {
  const [filter, setFilter] = useState('');
  const groups = useMemo(() => sessionsByGroup(state), [state.sessions]);

  const isInTile = (id) => state.tileAssignments.includes(id);

  const handleClick = (sess) => {
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

  const handleDelete = (e, id) => {
    e.stopPropagation();
    deleteSession(id).catch(err => console.error('Delete failed:', err));
  };

  const filterLower = filter.toLowerCase();

  return (
    <div class={`sidebar ${state.sidebarOpen ? '' : 'collapsed'}`}>
      <div class="sidebar-header">
        <span class="sidebar-logo">moa</span>
        <button class="sidebar-toggle" onClick={toggleSidebar} title="Toggle sidebar">
          {state.sidebarOpen ? <PanelLeftClose /> : <PanelLeft />}
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
                      class={`sidebar-item ${isInTile(sess.id) ? 'active' : ''} ${sess.state === 'saved' ? 'saved' : ''}`}
                      onClick={() => handleClick(sess)}
                    >
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
                        onClick={(e) => handleDelete(e, sess.id)}
                        title="Delete"
                      ><X /></button>
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
            </button>
          </div>
        </>
      )}
    </div>
  );
}
