import { useRef, useCallback } from 'preact/hooks';
import { GitFork, ChevronDown, Plus } from 'lucide-preact';
import { MessageList } from './MessageList.jsx';
import { InputBar } from './InputBar.jsx';
import { McpBanner } from './McpBanner.jsx';
import { SettingsDropdown } from './SettingsDropdown.jsx';
import { ModelPill } from './ModelPill.jsx';
import { TaskBar } from './TaskBar.jsx';

export function ChatView({ state, onToggleOverview, onOpenPalette }) {
  const session = state.activeSession ? state.sessions[state.activeSession] : null;
  const touchStart = useRef(null);

  // Swipe-down on header → overview
  const onTouchStart = useCallback((e) => {
    touchStart.current = { y: e.touches[0].clientY, t: Date.now() };
  }, []);

  const onTouchEnd = useCallback((e) => {
    if (!touchStart.current || !onToggleOverview) return;
    const dy = e.changedTouches[0].clientY - touchStart.current.y;
    const dt = Date.now() - touchStart.current.t;
    touchStart.current = null;
    if (dy > 50 && dt < 400) onToggleOverview();
  }, [onToggleOverview]);

  if (!session) {
    return (
      <div class="chat-view">
        <div class="chat-header">
          <button class="chat-header-title-btn" onClick={onToggleOverview}>
            <span class="chat-header-title">moa</span>
            <ChevronDown class="chat-header-chevron" />
          </button>
        </div>
        <div class="empty-state">
          <p>No active session</p>
          <button class="empty-new-btn" onClick={onOpenPalette}>
            <Plus /> New Session
          </button>
        </div>
      </div>
    );
  }

  return (
    <div class="chat-view">
      <div
        class="chat-header"
        onTouchStart={onTouchStart}
        onTouchEnd={onTouchEnd}
      >
        <span class={`state-dot ${session.state}`} />
        <button class="chat-header-title-btn" onClick={onToggleOverview}>
          <span class="chat-header-title">{session.title || 'Untitled'}</span>
          <ChevronDown class="chat-header-chevron" />
        </button>
        {session.subagentCount > 0 && (
          <span class="subagent-badge"><GitFork />{session.subagentCount}</span>
        )}
        <ModelPill model={session.model} thinking={session.thinking} />
        <SettingsDropdown sessionId={state.activeSession} session={session} />
      </div>

      {session.untrustedMcp && <McpBanner sessionId={state.activeSession} />}

      <MessageList session={session} />
      <TaskBar session={session} />
      <InputBar sessionId={state.activeSession} sessionState={session.state} />
    </div>
  );
}
