import { useRef, useCallback } from 'preact/hooks';
import { Menu, GitFork, ChevronDown } from 'lucide-preact';
import { toggleDrawer } from '../state.js';
import { MessageList } from './MessageList.jsx';
import { InputBar } from './InputBar.jsx';
import { McpBanner } from './McpBanner.jsx';
import { SettingsDropdown } from './SettingsDropdown.jsx';
import { ModelPill } from './ModelPill.jsx';

export function ChatView({ state, onToggleOverview }) {
  const session = state.activeSession ? state.sessions[state.activeSession] : null;
  const headerRef = useRef(null);
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
        <div class="chat-header" ref={headerRef}>
          <button class="chat-hamburger" onClick={toggleDrawer}><Menu /></button>
          <span class="chat-header-title" onClick={onToggleOverview}>moa</span>
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
      <div
        class="chat-header"
        ref={headerRef}
        onTouchStart={onTouchStart}
        onTouchEnd={onTouchEnd}
      >
        <button class="chat-hamburger" onClick={toggleDrawer}><Menu /></button>
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

      <InputBar sessionId={state.activeSession} sessionState={session.state} />
    </div>
  );
}
