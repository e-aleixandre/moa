import { useCallback, useRef, useMemo } from 'preact/hooks';
import { Plus, Sparkles, Archive, Trash2, FolderTree } from 'lucide-preact';
import { setActiveSession } from '../tile-actions.js';
import { resumeSession, deleteSession } from '../session-actions.js';
import { setState } from '../store.js';
import { shortModel, shortPath, projectKey, projectLabel } from '../util/format.js';

export function SessionOverview({ state, onSelect, onNewSession }) {
  const touchStart = useRef(null);

  const onTouchStart = useCallback((e) => {
    touchStart.current = { y: e.touches[0].clientY, t: Date.now() };
  }, []);

  const onTouchEnd = useCallback((e) => {
    if (!touchStart.current) return;
    const dy = touchStart.current.y - e.changedTouches[0].clientY;
    const dt = Date.now() - touchStart.current.t;
    touchStart.current = null;
    if (dy > 50 && dt < 400) onSelect();
  }, [onSelect]);

  const activeSessions = useMemo(() =>
    Object.values(state.sessions)
      .filter(s => s.state !== 'saved')
      .sort((a, b) => (b.updated || 0) - (a.updated || 0)),
    [state.sessions]
  );

  const savedSessions = useMemo(() =>
    Object.values(state.sessions)
      .filter(s => s.state === 'saved')
      .sort((a, b) => (b.updated || 0) - (a.updated || 0)),
    [state.sessions]
  );

  const handleSelect = useCallback((id) => {
    setActiveSession(id);
    onSelect();
  }, [onSelect]);

  const handleResume = useCallback((id) => {
    resumeSession(id).catch(e => console.error('Resume failed:', e));
    onSelect();
  }, [onSelect]);

  const handleDelete = useCallback((e, sess) => {
    e.stopPropagation();
    const label = sess.title || 'Untitled';
    if (!window.confirm(`Delete session "${label}"? This cannot be undone.`)) return;
    deleteSession(sess.id).catch(err => console.error('Delete failed:', err));
  }, []);

  const groupByProject = state.groupByProject;
  const toggleGroup = useCallback(() => {
    setState(s => ({ groupByProject: !s.groupByProject }));
  }, []);

  const renderCard = (sess) => {
    const isActive = state.activeSession === sess.id;
    const needsAttention = sess.state === 'permission' || sess.state === 'error';
    const unseen = sess.unseen && !isActive;
    const lastMsg = getLastMessage(sess);
    const path = shortPath(sess.cwd);

    return (
      <div
        key={sess.id}
        class={`overview-card ${isActive ? 'active' : ''} ${needsAttention ? 'attention' : ''} ${unseen ? 'unseen' : ''}`}
        onClick={() => handleSelect(sess.id)}
      >
        <div class="overview-card-header">
          <span class={`state-dot ${sess.state}`} />
          <span class="overview-card-title">{sess.title || 'Untitled'}</span>
          {unseen && <span class="overview-card-unseen" title="Unread activity" />}
        </div>
        {path && !groupByProject && (
          <div class="overview-card-path" title={sess.cwd}>{path}</div>
        )}
        <div class="overview-card-preview">
          {lastMsg || <span class="overview-card-empty">No messages yet</span>}
        </div>
        <div class="overview-card-footer">
          <span class="overview-card-model">
            <Sparkles />{shortModel(sess.model)}
          </span>
          <button
            class="overview-card-delete"
            title="Delete session"
            aria-label="Delete session"
            onClick={(e) => handleDelete(e, sess)}
          >
            <Trash2 />
          </button>
        </div>
      </div>
    );
  };

  const renderSaved = (sess) => {
    const path = shortPath(sess.cwd);
    return (
      <div
        key={sess.id}
        class="overview-saved-item"
        onClick={() => handleResume(sess.id)}
      >
        <div class="overview-saved-main">
          <span class="overview-saved-title">{sess.title || 'Untitled'}</span>
          {path && !groupByProject && (
            <span class="overview-saved-path" title={sess.cwd}>{path}</span>
          )}
        </div>
        <span class="overview-saved-model">{shortModel(sess.model)}</span>
        <button
          class="overview-saved-delete"
          title="Delete session"
          aria-label="Delete session"
          onClick={(e) => handleDelete(e, sess)}
        >
          <Trash2 />
        </button>
      </div>
    );
  };

  // Build ordered project groups when grouping is on. Each group carries its
  // active + saved sessions; projects are ordered by most recent activity.
  const projectGroups = useMemo(() => {
    if (!groupByProject) return null;
    const map = new Map();
    for (const sess of [...activeSessions, ...savedSessions]) {
      const key = projectKey(sess.cwd);
      if (!map.has(key)) {
        map.set(key, { key, cwd: sess.cwd || '', active: [], saved: [], latest: 0 });
      }
      const g = map.get(key);
      (sess.state === 'saved' ? g.saved : g.active).push(sess);
      g.latest = Math.max(g.latest, sess.updated || 0);
    }
    return [...map.values()].sort((a, b) => b.latest - a.latest);
  }, [groupByProject, activeSessions, savedSessions]);

  return (
    <div class="session-overview" onTouchStart={onTouchStart} onTouchEnd={onTouchEnd}>
      <div class="overview-header">
        <span class="overview-title">Sessions</span>
        <div class="overview-header-actions">
          <button
            class={`overview-group-toggle ${groupByProject ? 'on' : ''}`}
            title={groupByProject ? 'Grouping by project' : 'Group by project'}
            aria-pressed={groupByProject}
            onClick={toggleGroup}
          >
            <FolderTree />
          </button>
        </div>
      </div>

      {groupByProject ? (
        <div class="overview-groups">
          {projectGroups.map(g => (
            <div class="overview-project" key={g.key || '∅'}>
              <div class="overview-project-header" title={g.cwd}>
                <FolderTree class="overview-project-icon" />
                <span class="overview-project-label">{projectLabel(g.cwd)}</span>
                <span class="overview-project-count">{g.active.length + g.saved.length}</span>
              </div>
              <div class="overview-grid">
                {g.active.map(renderCard)}
              </div>
              {g.saved.length > 0 && (
                <div class="overview-saved">
                  {g.saved.map(renderSaved)}
                </div>
              )}
            </div>
          ))}
          <div class="overview-grid">
            <div class="overview-card new-card" onClick={onNewSession}>
              <Plus />
              <span>New Session</span>
            </div>
          </div>
        </div>
      ) : (
        <>
          {/* Active sessions */}
          <div class="overview-grid">
            {activeSessions.map(renderCard)}
            <div class="overview-card new-card" onClick={onNewSession}>
              <Plus />
              <span>New Session</span>
            </div>
          </div>

          {/* Saved sessions */}
          {savedSessions.length > 0 && (
            <div class="overview-saved">
              <div class="overview-saved-header">
                <Archive class="overview-saved-icon" />
                <span>Saved</span>
              </div>
              {savedSessions.map(renderSaved)}
            </div>
          )}
        </>
      )}
    </div>
  );
}

function getLastMessage(session) {
  if (!session.messages || session.messages.length === 0) return null;
  for (let i = session.messages.length - 1; i >= 0; i--) {
    const msg = session.messages[i];
    if (msg.role === 'assistant' || msg.role === 'user') {
      const text = (msg.content || [])
        .filter(c => c.type === 'text')
        .map(c => c.text)
        .join('');
      if (text) {
        return text.length > 120 ? text.substring(0, 120) + '…' : text;
      }
    }
  }
  return null;
}
