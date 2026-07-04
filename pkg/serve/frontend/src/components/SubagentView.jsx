import { ArrowLeft, GitFork } from 'lucide-preact';
import { MessageList } from './MessageList.jsx';
import { updateSession } from '../store.js';

// SubagentView shows one subagent's live sub-conversation, rendered with the
// SAME MessageList as the main chat. A back button returns to the parent
// conversation. The parent's InputBar stays active underneath (the
// sub-conversation is read-only).
export function SubagentView({ sessionId, session }) {
  const jobId = session?.viewingSubagent;
  const sa = jobId ? (session.subagents || {})[jobId] : null;

  const back = () => updateSession(sessionId, { viewingSubagent: null });

  if (!sa) {
    // Subagent vanished (e.g. finished + pruned) — bounce back.
    if (jobId) back();
    return null;
  }

  const statusLabel = sa.status === 'running'
    ? 'working…'
    : sa.status === 'cancelling'
      ? 'cancelling…'
      : sa.status;

  // MessageList takes a session-shaped object; the subagent state already has
  // messages/streamingText/thinkingText, so pass it directly.
  const subSession = {
    id: sessionId,
    messages: sa.messages,
    streamingText: sa.streamingText,
    thinkingText: sa.thinkingText,
  };

  return (
    <div class="subagent-view">
      <div class="subagent-view-header">
        <button class="subagent-back" onClick={back} title="Back to conversation">
          <ArrowLeft />
        </button>
        <GitFork class="subagent-view-icon" />
        <div class="subagent-view-titles">
          <div class="subagent-view-task">{sa.task || jobId}</div>
          <div class="subagent-view-meta">
            {sa.model || 'model'} · {statusLabel}
          </div>
        </div>
      </div>
      <MessageList session={subSession} />
    </div>
  );
}
