import { useRef, useCallback, useState } from 'preact/hooks';
import { GitFork, ChevronDown, Plus, History } from 'lucide-preact';
import { MessageList } from './MessageList.jsx';
import { InputBar } from './InputBar.jsx';
import { AgentTray } from './AgentTray.jsx';
import { SubagentView } from './SubagentView.jsx';
import { McpBanner } from './McpBanner.jsx';
import { SettingsDropdown } from './SettingsDropdown.jsx';
import { NotificationSettings } from './NotificationSettings.jsx';
import { ModelPill } from './ModelPill.jsx';
import { TaskBar } from './TaskBar.jsx';
import { TabBar } from './TabBar.jsx';
import { RewindSheet } from './RewindSheet.jsx';
import { VersionIndicator } from './LayoutBar.jsx';
import { sessionDotState } from '../util/format.js';

export function ChatView({ state, onToggleOverview, onOpenPalette, version }) {
  const session = state.activeSession ? state.sessions[state.activeSession] : null;
  const touchStart = useRef(null);
  const [rewindOpen, setRewindOpen] = useState(false);
  const busy = !!session && (session.state === 'running' || session.state === 'permission');

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
          <VersionIndicator version={version} />
          <NotificationSettings state={state} />
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
        <span class={`state-dot ${sessionDotState(session)}`} />
        <button class="chat-header-title-btn" onClick={onToggleOverview}>
          <span class="chat-header-title">{session.title || 'Untitled'}</span>
          <ChevronDown class="chat-header-chevron" />
        </button>
        <VersionIndicator version={version} />
        {session.subagentCount > 0 && (
          <span class="subagent-badge"><GitFork />{session.subagentCount}</span>
        )}
        <ModelPill model={session.model} thinking={session.thinking} />
        <button
          class="chat-header-rewind"
          onClick={() => setRewindOpen(true)}
          disabled={busy}
          title="Rewind conversation"
        >
          <History />
        </button>
        <NotificationSettings state={state} />
        <SettingsDropdown sessionId={state.activeSession} session={session} />
      </div>

      {session.untrustedMcp && <McpBanner sessionId={state.activeSession} />}

      {session.viewingSubagent
        ? <SubagentView sessionId={state.activeSession} session={session} />
        : <MessageList session={session} />}
      <AgentTray sessionId={state.activeSession} session={session} />
      <TabBar state={state} onOpenPalette={onOpenPalette} />
      <InputBar sessionId={state.activeSession} session={session} />
      <TaskBar session={session} usage={state.usage} />

      <RewindSheet
        sessionId={state.activeSession}
        open={rewindOpen}
        onClose={() => setRewindOpen(false)}
      />
    </div>
  );
}
