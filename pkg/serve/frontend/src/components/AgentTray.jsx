import { useState, useRef, useCallback } from 'preact/hooks';
import { GitFork, ChevronUp, ChevronDown, X, Loader2, Rocket } from 'lucide-preact';
import { updateSession } from '../store.js';
import { cancelSubagent, cancelBashJob, promoteSubagent } from '../session-actions.js';
import { addToast } from '../notifications.js';

// liveSubagents returns the running/cancelling subagents of a session, as an
// array. The tray only shows live agents (finished ones disappear).
function liveSubagents(session) {
  const map = session?.subagents || {};
  return Object.values(map).filter(
    sa => sa.status === 'running' || sa.status === 'cancelling',
  );
}

// subagentTotals aggregates cost/tokens across ALL subagents of the session
// (including finished ones), so the user sees how much subagents have spent,
// kept separate from the main session's own cost.
function subagentTotals(session) {
  const map = session?.subagents || {};
  let costUSD = 0, tokens = 0;
  for (const sa of Object.values(map)) {
    if (sa.usage) {
      costUSD += sa.usage.costUSD || 0;
      tokens += (sa.usage.inputTokens || 0) + (sa.usage.outputTokens || 0);
    }
  }
  return { costUSD, tokens };
}

// AgentTray is a slide-up panel anchored above the InputBar. Collapsed it's a
// thin bar showing how many agents are working; drag up (or tap) to expand the
// list. Tapping an agent opens its sub-conversation (session.viewingSubagent).
export function AgentTray({ sessionId, session }) {
  const [expanded, setExpanded] = useState(false);
  const dragStart = useRef(null);

  const live = liveSubagents(session);
  const totals = subagentTotals(session);

  const onTouchStart = useCallback((e) => {
    dragStart.current = { y: e.touches[0].clientY };
  }, []);

  const onTouchEnd = useCallback((e) => {
    if (!dragStart.current) return;
    const dy = e.changedTouches[0].clientY - dragStart.current.y;
    dragStart.current = null;
    if (dy < -30) setExpanded(true);
    else if (dy > 30) setExpanded(false);
  }, []);

  const openSubagent = useCallback((jobId) => {
    updateSession(sessionId, { viewingSubagent: jobId });
  }, [sessionId]);

  const handleCancel = useCallback(async (e, jobId) => {
    e.stopPropagation();
    try {
      const job = session?.subagents?.[jobId];
      if (job?.kind === 'bash') await cancelBashJob(sessionId, jobId);
      else await cancelSubagent(sessionId, jobId);
    } catch (_) { /* best-effort */ }
  }, [sessionId, session]);

  const handlePromote = useCallback(async (e, jobId) => {
    e.stopPropagation();
    try {
      await promoteSubagent(sessionId, jobId);
    } catch (e) {
      addToast({ title: 'Promote failed', detail: e.message, type: 'error' });
    }
  }, [sessionId]);

  if (live.length === 0) return null;

  return (
    <div class={`agent-tray ${expanded ? 'expanded' : ''}`}>
      <button
        class="agent-tray-handle"
        onClick={() => setExpanded(v => !v)}
        onTouchStart={onTouchStart}
        onTouchEnd={onTouchEnd}
      >
        <span class="agent-tray-grip" />
        <GitFork class="agent-tray-icon" />
        <span class="agent-tray-count">
          {live.length} {live.length === 1 ? 'job' : 'jobs'} working
        </span>
        {totals.costUSD > 0 && (
          <span class="agent-tray-cost" title="Total spent by subagents this session">
            ${totals.costUSD.toFixed(4)}
          </span>
        )}
        {expanded ? <ChevronDown /> : <ChevronUp />}
      </button>

      {expanded && (
        <div class="agent-tray-list">
          {live.map((sa) => (
            <div
              key={sa.jobId}
              class="agent-tray-item"
              onClick={() => openSubagent(sa.jobId)}
            >
              <Loader2 class="agent-tray-spinner" />
              <div class="agent-tray-item-body">
                <div class="agent-tray-task">{sa.task || sa.jobId}</div>
                <div class="agent-tray-meta">
                  {sa.kind === 'bash' ? 'background bash' : (sa.model || 'model')}
                  {sa.status === 'cancelling' && ' · cancelling…'}
                </div>
              </div>
              {(sa.kind === 'bash' || sa.async) && sa.status === 'running' && (
                <button
                  class="agent-tray-cancel"
                  title="Cancel this agent"
                  onClick={(e) => handleCancel(e, sa.jobId)}
                >
                  <X />
                </button>
              )}
              {!sa.async && sa.status === 'running' && (
                <button
                  class="agent-tray-promote"
                  title="Promote to background (unblocks parent)"
                  onClick={(e) => handlePromote(e, sa.jobId)}
                >
                  <Rocket />
                </button>
              )}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
