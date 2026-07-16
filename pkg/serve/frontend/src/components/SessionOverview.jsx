import { useCallback, useRef, useMemo } from 'preact/hooks';
import { Plus, Archive, Trash2, FolderTree, QrCode } from 'lucide-preact';
import { setActiveSession } from '../tile-actions.js';
import { resumeSession, deleteSession, archiveSession } from '../session-actions.js';
import { setState } from '../store.js';
import { addToast } from '../notifications.js';
import { shortPath, projectKey, projectLabel, sessionDotState, isRecentSession } from '../util/format.js';

export function SessionOverview({ state, onSelect, onNewSession, onOpenPairing }) {
  const touchStart = useRef(null);

  // Close-gesture handlers live ONLY on the header/grabber, not the scrollable
  // list — otherwise a fast flick while scrolling the sessions is mistaken for
  // "close" and dismisses the dashboard. The grabber (and a tap) are the sole
  // ways to close by gesture; the content is free to scroll.
  const onGrabberTouchStart = useCallback((e) => {
    touchStart.current = { y: e.touches[0].clientY, t: Date.now() };
  }, []);

  const onGrabberTouchEnd = useCallback((e) => {
    if (!touchStart.current) return;
    const dy = touchStart.current.y - e.changedTouches[0].clientY;
    const dt = Date.now() - touchStart.current.t;
    touchStart.current = null;
    // Swipe up (or a quick tap) on the grabber closes the dashboard.
    if (dy > 40 || (Math.abs(dy) < 10 && dt < 250)) onSelect();
  }, [onSelect]);

  const activeSessions = useMemo(() =>
    Object.values(state.sessions)
      // Hide stale (>7d) sessions to keep the grid scannable, but never hide the
      // one currently open — it must stay visible in the dashboard.
      .filter(s => s.state !== 'saved' && !s.archived &&
        (isRecentSession(s) || s.id === state.activeSession))
      .sort((a, b) => (b.updated || 0) - (a.updated || 0)),
    [state.sessions, state.activeSession]
  );

  const savedSessions = useMemo(() =>
    Object.values(state.sessions)
      .filter(s => s.state === 'saved' && !s.archived && isRecentSession(s))
      .sort((a, b) => (b.updated || 0) - (a.updated || 0)),
    [state.sessions]
  );

  const handleSelect = useCallback((id) => {
    setActiveSession(id);
    onSelect();
  }, [onSelect]);

  const handleResume = useCallback(async (id) => {
    try {
      await resumeSession(id);
      onSelect();
    } catch (e) {
      addToast(`Could not resume session: ${e.message}`, 'error');
    }
  }, [onSelect]);

  const handleDelete = useCallback((e, sess) => {
    e.stopPropagation();
    const label = sess.title || 'Untitled';
    if (!window.confirm(`Delete session "${label}"? This cannot be undone.`)) return;
    deleteSession(sess.id).catch(err => console.error('Delete failed:', err));
  }, []);

  const handleArchive = useCallback((e, sess) => {
    e.stopPropagation();
    archiveSession(sess.id)
      .then(() => addToast({ title: 'Session closed', message: sess.title || 'Untitled', type: 'info' }))
      .catch(err => addToast({ title: 'Could not close session', message: err.message, type: 'error' }));
  }, []);

  const groupByProject = state.groupByProject;
	const attentionItems = state.attentionItems || [];
  const toggleGroup = useCallback(() => {
    setState(s => ({ groupByProject: !s.groupByProject }));
  }, []);

	const openAttentionSession = useCallback((id) => {
		if (!id) return;
		setActiveSession(id);
		onSelect();
	}, [onSelect]);

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
          <span class={`state-dot ${sessionDotState(sess)}`} />
          <span class="overview-card-title">{sess.title || 'Untitled'}</span>
          {unseen && <span class="overview-card-unseen" title="Unread activity" />}
        </div>
        {path && !groupByProject && (
          <div class="overview-card-path" title={sess.cwd}>{path}</div>
        )}
        {sess.briefAttempting && (
          <div class="overview-card-brief" title={sess.briefProgress || ''}>
            <span class="overview-card-brief-attempting">{sess.briefAttempting}</span>
            {sess.briefProgress && (
              <span class="overview-card-brief-progress">{sess.briefProgress}</span>
            )}
          </div>
        )}
        <div class="overview-card-preview">
          {lastMsg || <span class="overview-card-empty">No messages yet</span>}
        </div>
        <div class="overview-card-footer">
          <button
            class="overview-card-close"
            title="Close session (hides it; reopen later)"
            aria-label="Close session"
            onClick={(e) => handleArchive(e, sess)}
          >
            <Archive />Cerrar
          </button>
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
    <div class="session-overview">
      <div
        class="overview-grabber"
        onTouchStart={onGrabberTouchStart}
        onTouchEnd={onGrabberTouchEnd}
        onClick={onSelect}
        role="button"
        aria-label="Close sessions"
        title="Close"
      >
        <span class="overview-grabber-handle" />
      </div>
      <div class="overview-header">
        <span class="overview-title">Sessions</span>
        <div class="overview-header-actions">
          <button class="overview-group-toggle" title="Pair Pulse" aria-label="Pair Pulse" onClick={onOpenPairing}>
            <QrCode />
          </button>
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

      {attentionItems.length > 0 && (
        <section class="overview-attention" aria-label="Needs attention">
          <div class="overview-attention-title">Needs attention</div>
          {attentionItems.map(item => (
            <button
              key={item.id}
              class={`overview-attention-item ${item.kind || ''}`}
              onClick={() => openAttentionSession(item.session_id)}
            >
              <span>{item.spoken}</span>
              <small>{item.alias || 'Open session'}</small>
            </button>
          ))}
        </section>
      )}

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
