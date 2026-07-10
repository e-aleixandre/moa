import { ArrowLeft, GitFork, Rocket } from 'lucide-preact';
import { MessageList } from './MessageList.jsx';
import { updateSession } from '../store.js';
import { cancelBashJob, promoteSubagent } from '../session-actions.js';
import { addToast } from '../notifications.js';

// SubagentView shows one subagent's live sub-conversation, rendered with the
// SAME MessageList as the main chat. A back button returns to the parent
// conversation. The InputBar underneath detects this view and steers messages
// to this subagent (see InputBar's subagentMode) instead of the main agent.
export function SubagentView({ sessionId, session }) {
  const jobId = session?.viewingSubagent;
  const sa = jobId ? (session.subagents || {})[jobId] : null;

  const back = () => updateSession(sessionId, { viewingSubagent: null });

  const promote = async () => {
    try {
      await promoteSubagent(sessionId, jobId);
    } catch (e) {
      addToast({ title: 'Promote failed', detail: e.message, type: 'error' });
    }
  };
  const cancel = async () => {
    try {
      await cancelBashJob(sessionId, jobId);
    } catch (e) {
      addToast({ title: 'Cancel failed', detail: e.message, type: 'error' });
    }
  };

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

  const u = sa.usage;
  const costLabel = u && (u.costUSD || u.inputTokens || u.outputTokens)
    ? `$${(u.costUSD || 0).toFixed(4)} · ${((u.inputTokens || 0) + (u.outputTokens || 0)).toLocaleString()} tok`
    : null;

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
          <div class="subagent-view-task">{sa.kind === 'bash' ? `Bash: ${sa.task || jobId}` : (sa.task || jobId)}</div>
          <div class="subagent-view-meta">
            {sa.kind === 'bash' ? 'background bash' : (sa.model || 'model')} · {statusLabel}
            {costLabel && <span class="subagent-view-cost"> · {costLabel}</span>}
          </div>
        </div>
        {!sa.async && sa.status === 'running' && (
          <button
            class="subagent-promote"
            onClick={promote}
            title="Promote to background (unblocks parent)"
          >
            <Rocket />
          </button>
        )}
        {sa.kind === 'bash' && sa.status === 'running' && (
          <button class="subagent-promote" onClick={cancel} title="Cancel background bash job">×</button>
        )}
      </div>
      <MessageList session={subSession} />
    </div>
  );
}
