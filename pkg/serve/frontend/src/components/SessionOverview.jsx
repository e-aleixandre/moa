import { useCallback } from 'preact/hooks';
import { Plus, Sparkles } from 'lucide-preact';
import { setActiveSession, toggleDialog } from '../state.js';
import { shortModel } from '../util/format.js';

/**
 * Mobile app-switcher view: grid of session cards shown after pinch-in.
 * Tapping a card zooms back into that session.
 */
export function SessionOverview({ state, onSelect }) {
  const sessions = Object.values(state.sessions)
    .filter(s => s.state !== 'saved')
    .sort((a, b) => (b.updated || 0) - (a.updated || 0));

  const handleSelect = useCallback((id) => {
    setActiveSession(id);
    onSelect();
  }, [onSelect]);

  const handleNew = useCallback(() => {
    toggleDialog();
    onSelect();
  }, [onSelect]);

  return (
    <div class="session-overview">
      <div class="overview-header">
        <span class="overview-title">Sessions</span>
        <span class="overview-hint">Tap to open · Pinch out to go back</span>
      </div>
      <div class="overview-grid">
        {sessions.map(sess => {
          const isActive = state.activeSession === sess.id;
          const needsAttention = sess.state === 'permission' || sess.state === 'error';
          const lastMsg = getLastMessage(sess);

          return (
            <div
              key={sess.id}
              class={`overview-card ${isActive ? 'active' : ''} ${needsAttention ? 'attention' : ''}`}
              onClick={() => handleSelect(sess.id)}
            >
              <div class="overview-card-header">
                <span class={`state-dot ${sess.state}`} />
                <span class="overview-card-title">{sess.title || 'Untitled'}</span>
              </div>
              <div class="overview-card-preview">
                {lastMsg || <span class="overview-card-empty">No messages yet</span>}
              </div>
              <div class="overview-card-footer">
                <span class="overview-card-model">
                  <Sparkles />{shortModel(sess.model)}
                </span>
              </div>
            </div>
          );
        })}

        <div class="overview-card new-card" onClick={handleNew}>
          <Plus />
          <span>New Session</span>
        </div>
      </div>
    </div>
  );
}

/** Extract last visible message text for preview. */
function getLastMessage(session) {
  if (!session.messages || session.messages.length === 0) return null;
  // Walk backwards to find last text
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
