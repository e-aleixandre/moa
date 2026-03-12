import { Plus } from 'lucide-preact';
import { setActiveSession } from '../state.js';

export function TabBar({ state, onOpenPalette }) {
  const sessions = Object.values(state.sessions)
    .filter(s => s.state !== 'saved')
    .sort((a, b) => (b.updated || 0) - (a.updated || 0));

  return (
    <div class="tab-bar">
      {sessions.map(sess => {
        const isActive = state.activeSession === sess.id;
        const needsAttention = sess.state === 'permission' || sess.state === 'error';
        const hasFlash = sess.flash && !isActive;
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
          </button>
        );
      })}
      <button class="tab-add" onClick={onOpenPalette} title="New session">
        <Plus />
      </button>
    </div>
  );
}
