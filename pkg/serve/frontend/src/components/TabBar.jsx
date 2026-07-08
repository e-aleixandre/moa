import { Plus } from 'lucide-preact';
import { setActiveSession } from '../tile-actions.js';

export function TabBar({ state, onOpenPalette }) {
  const sessions = Object.values(state.sessions)
    // Archived ("closed") sessions drop off the tab bar, but as a safety net
    // one that needs attention (a pending permission or an error) still
    // surfaces here rather than getting silently stuck out of sight.
    .filter(s => s.state !== 'saved' && (!s.archived || s.state === 'permission' || s.state === 'error'))
    .sort((a, b) => (b.updated || 0) - (a.updated || 0));

  return (
    <div class="tab-bar">
      {sessions.map(sess => {
        const isActive = state.activeSession === sess.id;
        const needsAttention = sess.state === 'permission' || sess.state === 'error';
        const hasFlash = sess.flash && !isActive;
        const unseen = sess.unseen && !isActive;
        const classes = ['tab-pill'];
        if (isActive) classes.push('active');
        if (needsAttention && !isActive) classes.push('attention');
        if (hasFlash) classes.push('flash');

        return (
          <button
            key={sess.id}
            class={classes.join(' ')}
            onClick={() => setActiveSession(sess.id)}
          >
            <span class={`state-dot ${sess.state}`} />
            {sess.title || 'Untitled'}
            {unseen && <span class="tab-unseen" title="Unread activity" />}
          </button>
        );
      })}
      <button class="tab-add" onClick={onOpenPalette} title="New session">
        <Plus />
      </button>
    </div>
  );
}
