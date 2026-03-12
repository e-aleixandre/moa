import { toggleDrawer } from '../state.js';
import { MessageList } from './MessageList.jsx';
import { InputBar } from './InputBar.jsx';
import { McpBanner } from './McpBanner.jsx';

export function ChatView({ state }) {
  const session = state.activeSession ? state.sessions[state.activeSession] : null;

  if (!session) {
    return (
      <div class="chat-view">
        <div class="chat-header">
          <button class="chat-hamburger" onClick={toggleDrawer}>☰</button>
          <span class="chat-header-title">moa</span>
        </div>
        <div class="empty-state">
          <p>No active session.</p>
          <p>Create one or select from the drawer.</p>
        </div>
      </div>
    );
  }

  return (
    <div class="chat-view">
      <div class="chat-header">
        <button class="chat-hamburger" onClick={toggleDrawer}>☰</button>
        <span class={`state-dot ${session.state}`} />
        <span class="chat-header-title">{session.title || 'Untitled'}</span>
        {session.subagentCount > 0 && (
          <span class="subagent-badge">🔄{session.subagentCount}</span>
        )}
        <span class="chat-header-model">{session.model}</span>
      </div>

      {session.untrustedMcp && <McpBanner sessionId={state.activeSession} />}

      <MessageList session={session} />

      <InputBar sessionId={state.activeSession} sessionState={session.state} />
    </div>
  );
}
