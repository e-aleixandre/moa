import { focusTile } from '../state.js';
import { MessageList } from './MessageList.jsx';
import { InputBar } from './InputBar.jsx';
import { McpBanner } from './McpBanner.jsx';

export function Tile({ tileIndex, sessionId, session, isFocused }) {
  const needsAttention = session && (session.state === 'permission' || session.state === 'error');
  const classes = ['tile'];
  if (isFocused) classes.push('focused');
  if (needsAttention) classes.push('attention');

  if (!session) {
    return (
      <div class={classes.join(' ')} onClick={() => focusTile(tileIndex)}>
        <div class="tile-empty">
          Click a session from the sidebar to open it here.
        </div>
      </div>
    );
  }

  return (
    <div class={classes.join(' ')} onClick={() => focusTile(tileIndex)}>
      <div class="tile-header">
        <span class={`state-dot ${session.state}`} />
        <span class="tile-title">{session.title || 'Untitled'}</span>
        {session.subagentCount > 0 && (
          <span class="subagent-badge">🔄{session.subagentCount}</span>
        )}
        <span class="tile-model">{session.model}</span>
        <span class="tile-number">#{tileIndex + 1}</span>
      </div>

      {session.untrustedMcp && <McpBanner sessionId={sessionId} />}

      <MessageList session={session} />

      <InputBar sessionId={sessionId} sessionState={session.state} />
    </div>
  );
}
