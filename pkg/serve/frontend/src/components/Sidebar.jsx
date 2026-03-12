import { useState, useMemo, useEffect, useRef } from 'preact/hooks';
import { PanelLeftClose, PanelLeft, X, Plus, Check } from 'lucide-preact';
import {
  assignToTile, deleteSession, resumeSession,
  toggleDialog, toggleSidebar, sessionsByGroup, isSessionInTile,
  focusTile,
} from '../state.js';
import { allTileIds, findTile } from '../tileTree.js';
import { formatShortcut } from '../hooks/useHotkeys.js';
import { useTouchDrag } from '../hooks/useTouchDrag.js';

function SidebarItem({ sess, isActive, isConfirming, onConfirm, onCancelConfirm, onClick, onDeleteClick }) {
  const touchProps = useTouchDrag({
    data: { 'text/x-session-id': sess.id },
  });

  return (
    <div
      class={`sidebar-item ${isActive ? 'active' : ''} ${sess.state === 'saved' ? 'saved' : ''} ${isConfirming ? 'confirming' : ''}`}
      onClick={onClick}
      draggable={sess.state !== 'saved'}
      onDragStart={(e) => {
        e.dataTransfer.setData('text/x-session-id', sess.id);
        e.dataTransfer.effectAllowed = 'move';
      }}
      {...(sess.state !== 'saved' ? touchProps : {})}
    >
      {isConfirming ? (
        <div class="delete-confirm">
          <span class="delete-confirm-text">Delete?</span>
          <button class="delete-confirm-yes" onClick={onConfirm}><Check /></button>
          <button class="delete-confirm-no" onClick={onCancelConfirm}><X /></button>
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
            onClick={onDeleteClick}
            title="Delete"
          ><X /></button>
        </>
      )}
    </div>
  );
}

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

  const isInTile = (id) => isSessionInTile(state, id);

  const handleClick = (e, sess) => {
    if (e.target.closest('.sidebar-item-delete, .delete-confirm')) return;
    if (confirmId) { setConfirmId(null); return; }
    if (sess.state === 'saved') {
      resumeSession(sess.id).catch(e => console.error('Resume failed:', e));
      return;
    }
    // If already in a tile, focus that tile
    const ids = allTileIds(state.tileTree);
    for (const tid of ids) {
      const t = findTile(state.tileTree, tid);
      if (t && t.sessionId === sess.id) {
        focusTile(tid);
        return;
      }
    }
    assignToTile(state.focusedTile, sess.id);
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
        <button class="sidebar-toggle" onClick={toggleSidebar} title={`Toggle sidebar (${formatShortcut('B', { mod: true })})`}>
          {state.sidebarOpen ? <PanelLeftClose /> : <PanelLeft />}
          {state.sidebarOpen && <kbd class="shortcut-hint">{formatShortcut('B', { mod: true })}</kbd>}
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
                    <SidebarItem
                      key={sess.id}
                      sess={sess}
                      isActive={isInTile(sess.id)}
                      isConfirming={confirmId === sess.id}
                      onClick={(e) => handleClick(e, sess)}
                      onDeleteClick={(e) => handleDeleteClick(e, sess.id)}
                      onConfirm={(e) => handleConfirm(e, sess.id)}
                      onCancelConfirm={handleCancel}
                    />
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
              <kbd class="shortcut-hint">{formatShortcut('N', { mod: true })}</kbd>
            </button>
          </div>
        </>
      )}
    </div>
  );
}
