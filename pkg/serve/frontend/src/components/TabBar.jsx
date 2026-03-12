import { setActiveSession, toggleDialog } from '../state.js';

export function TabBar({ state }) {
  const sessions = Object.values(state.sessions)
    .filter(s => s.state !== 'saved')
    .sort((a, b) => (b.updated || 0) - (a.updated || 0));

  return (
    <div class="tab-bar">
      {sessions.map(sess => {
        const isActive = state.activeSession === sess.id;
        const needsAttention = sess.state === 'permission' || sess.state === 'error';
        const classes = ['tab-pill'];
        if (isActive) classes.push('active');
        if (needsAttention && !isActive) classes.push('attention');

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
      <button class="tab-add" onClick={toggleDialog} title="New session">+</button>
    </div>
  );
}
