// WebSocket session-level event handling.

import { triggerAttention, triggerDone, addToast } from '../notifications.js';
import { store, setState, updateSession, visibleSessionIds } from '../store.js';
import { resetOlderHistory } from '../history-paging.js';
import { normalizeHistory } from './history.js';
import { nextRunEpoch } from './init.js';
import { markUnseen, acknowledgeVisibleLiveAttention, flashSession } from './attention.js';

export function handleWsStateChange(id, data, seq = 0) {
  const state = store.get();
  const prev = state.sessions[id];
  const wasRunning = prev && (prev.state === 'running' || prev.state === 'permission');
  // A compaction settles through the state machine (the bus transitions to
  // error before publishing its terminal event), so a failure arrives here as a
  // plain error state. Read the flag BEFORE the patch clears it, so the toast
  // can name what actually failed instead of blaming "the run".
  const wasCompacting = prev?.compacting === true;
  const patch = { state: data.state, error: data.error || null, runEpoch: nextRunEpoch(id) };
  // Anchor the activity-indicator elapsed counter when a run begins. Only on
  // the transition into a running state, and only if not already set (a reconnect
  // snapshot may have seeded the authoritative server timestamp).
  const nowRunning = data.state === 'running' || data.state === 'permission';
  if (nowRunning && !wasRunning && !prev?.runStartedAtMs) {
    patch.runStartedAtMs = Date.now();
    // A fresh run starts: reset the live per-run token tally so it counts up
    // from zero again. Counts from the previous run persist until this point
    // (not cleared at idle), so the last run's totals stay visible in between.
    patch.runTokensUp = 0;
    patch.runTokensDown = 0;
  }
  updateSession(id, patch);
  if (data.state === 'idle' || data.state === 'error') {
    const sess = store.get().sessions[id];
    // Keep pendingSteers: a steer queued during the last turn stays genuinely
    // queued (mostrar la verdad). It's cleared only by Steered or a snapshot.
    if (sess) updateSession(id, { streamingText: null, thinkingText: null, compacting: false, runStartedAtMs: null });
    if (data.state === 'error' && seq > 0) {
      markUnseen(id, seq, true);
      acknowledgeVisibleLiveAttention(id, seq);
    }
    if (wasRunning) {
      flashSession(id, data.state === 'error' ? 'error' : 'done');
      // A successful/cancelled terminal state is followed by run_end, which
      // owns its authoritative occurrence. Only error state_change is itself
      // the terminal attention event (its run_end reuses that same ID).
      // Surface the reason for an error end so it's visible even when the tile
      // isn't focused — parity with the TUI's run-end error block. A usage/quota
      // limit reads as an actionable "resets in X" line rather than a fault.
      if (data.state === 'error' && data.error) {
        const isQuota = /quota exceeded|usage limit/i.test(data.error);
        let title = 'Run failed';
        if (isQuota) title = 'Usage limit reached';
        else if (wasCompacting) title = 'Compaction failed';
        addToast({
          sessionId: id,
          title,
          detail: data.error,
          type: 'attention',
        });
      }
      const visible = visibleSessionIds(store.get());
      if (!visible.includes(id) && sess) {
        if (data.state === 'error') {
          triggerAttention(sess, null, store.get().soundEnabled);
        } else {
          triggerDone(sess, store.get().soundEnabled);
        }
      }
    }
  }
}


export function handleWsConfigChange(id, data) {
  const sess = store.get().sessions[id];
  const patch = {
    model: data.model || sess?.model,
    provider: data.provider || sess?.provider,
    thinking: data.thinking || sess?.thinking,
  };
  // Fast mode travels with the model: whether it is on, and whether the model
  // it is now on can serve it at all — a model switch can take the option away.
  if (data.fast !== undefined) patch.fast = data.fast;
  if (data.fast_supported !== undefined) patch.fastSupported = data.fast_supported;
  if (data.fast_note !== undefined) patch.fastNote = data.fast_note;
  if (data.permission_mode) {
    patch.permissionMode = data.permission_mode;
  }
  // compact_at is sent only on a threshold change, and 0 is a real value
  // ("compact at the model window") — so presence, not truthiness, is the test.
  if (data.compact_at != null) {
    patch.compactAt = data.compact_at;
  }
  // A model switch carries the new window, since it is the denominator every
  // context reading uses. Without it the ring and the compaction limit would
  // keep measuring against the window of the model we just left.
  if (data.context_window) {
    patch.contextWindow = data.context_window;
  }
  updateSession(id, patch);
}

export function handleWsContextUpdate(id, data) {
  if (data.context_percent != null) {
    updateSession(id, { contextPercent: data.context_percent });
  }
}

// handleWsMcpChange reflects a live MCP server transition: it refreshes the
// glanceable status-line summary and bumps mcpTick, a monotonic counter an open
// MCP panel watches to re-fetch the full per-server detail (the summary alone
// can't carry per-server state/scope changes).
export function handleWsMcpChange(id, data) {
  const mcp = {
    total: data.total || 0,
    ready: data.ready || 0,
    disabled: data.disabled || 0,
    unhealthy: data.unhealthy || 0,
    pending: data.pending || 0,
  };
  const prev = store.get().sessions[id];
  updateSession(id, { mcp, mcpTick: ((prev && prev.mcpTick) || 0) + 1 });
}

export function handleWsSessionCost(id, data) {
  if (data.cost_usd != null) {
    updateSession(id, { costUSD: data.cost_usd });
  }
}

// handleWsRateLimit reflects a request's live rate-limit headers. OpenAI/Codex
// has no usage endpoint, so its last observed account-wide windows are kept in
// the global snapshot. Anthropic also patches its global plan snapshot here so
// the widget does not lag the 60s poll; extra-usage spend (€) stays poller-owned.
export function handleWsRateLimit(id, data) {
  // Per-session utilizations: always record when the header was present
  // (pct >= 0). This is what the OpenAI widget reads (no global poller), and it
  // keeps each session's meter independent in a mixed-provider layout.
  const patch = { onOverage: !!data.on_overage };
  if (data.five_hour_pct >= 0) patch.rlFiveHourPct = data.five_hour_pct;
  if (data.seven_day_pct >= 0) patch.rlSevenDayPct = data.seven_day_pct;
  updateSession(id, patch);

  // Patch the global (poller-owned) snapshot only for Anthropic sessions: those
  // windows are account-wide and share the /api/usage shape. An OpenAI session
  // must NOT overwrite the Anthropic snapshot in a mixed layout.
  const sess = store.get().sessions[id];
  const isAnthropic = !sess?.provider || sess.provider === 'anthropic';
  if (sess?.provider === 'openai') {
    const current = store.get().usage || { available: false, version: 2, providers: {}, provider_status: {} };
    const prior = current.providers?.openai || {};
    const openai = {
      ...prior,
      provider: 'openai',
      auth_kind: 'oauth',
      stability: 'response_headers',
    };
    if (data.five_hour_pct >= 0) openai.five_hour = { utilization: data.five_hour_pct };
    if (data.seven_day_pct >= 0) openai.seven_day = { utilization: data.seven_day_pct };
    setState({ usage: {
      ...current,
      available: true,
      providers: { ...(current.providers || {}), openai },
      provider_status: { ...(current.provider_status || {}), openai: { available: true, auth_kind: 'oauth' } },
    } });
    return;
  }
  if (!isAnthropic) return;

  const u = store.get().usage;
  if (u && u.available) {
    let changed = false;
    const usage = { ...u };
    // Only apply a window when the header was present (pct >= 0); never overwrite
    // a known value with an unknown one.
    if (u.five_hour && data.five_hour_pct >= 0) {
      usage.five_hour = { ...u.five_hour, utilization: data.five_hour_pct };
      changed = true;
    }
    if (u.seven_day && data.seven_day_pct >= 0) {
      usage.seven_day = { ...u.seven_day, utilization: data.seven_day_pct };
      changed = true;
    }
    if (changed) setState({ usage });
  }
}


export function handleWsCommandQueued(id, data) {
  const sess = store.get().sessions[id];
  if (!sess || !data || !data.id) return;
  const steers = [...(sess.pendingSteers || [])];
  if (steers.some(s => s.id === data.id)) {
    // Already present as an optimistic chip — confirm it (see mergeSteers).
    updateSession(id, {
      pendingSteers: steers.map(s => (s.id === data.id ? { ...s, confirmed: true } : s)),
    });
    return;
  }
  steers.push({ id: data.id, text: data.raw, command: true, confirmed: true });
  updateSession(id, { pendingSteers: steers });
}

// handleWsCommandDequeued removes a queued command barrier when it leaves the
// queue — either executed at idle (executed=true) or dropped because it failed
// permanently (executed=false, err set). The command chip disappears; a failure
// surfaces as a toast so a queued command that never ran isn't lost silently.
export function handleWsCommandDequeued(id, data) {
  const sess = store.get().sessions[id];
  if (!sess || !data || !data.id) return;
  const steers = (sess.pendingSteers || []).filter(s => s.id !== data.id);
  updateSession(id, { pendingSteers: steers.length > 0 ? steers : null });
  if (!data.executed && data.err) {
    addToast({ sessionId: id, title: 'Queued command failed', detail: `${data.raw}: ${data.err}`, type: 'error' });
  }
}

export function handleWsTasksUpdate(id, data) {
  updateSession(id, { tasks: data.tasks || [] });
}

export function handleWsCommand(id, data) {
  if (data.command === 'clear') {
    resetOlderHistory(id);
    updateSession(id, { messages: [], streamingText: null, thinkingText: null });
  } else if (data.command === 'compact') {
    // Don't replace the transcript with the compacted LLM context. When the
    // command event includes the durable tree marker, append that exact row;
    // otherwise wait for init/history hydration rather than fabricating a
    // second, non-durable representation.
    const sess = store.get().sessions[id];
    const markers = normalizeHistory(data.messages || []).filter(row => row._type === 'compaction_marker');
    if (sess && markers.length > 0) {
      const known = new Set(sess.messages.map(message => message?._msg_id).filter(Boolean));
      const fresh = markers.filter(marker => marker._msg_id && !known.has(marker._msg_id));
      if (fresh.length > 0) updateSession(id, { messages: [...sess.messages, ...fresh] });
    }
  } else if (data.command === 'skill') {
    // A skill loaded by the user is an ordinary message appended to the
    // conversation. Without this the row only shows up on the next reload, so
    // invoking a skill looks like nothing happened.
    const sess = store.get().sessions[id];
    if (sess && data.messages) {
      const known = new Set(sess.messages.map(message => message?._msg_id).filter(Boolean));
      const fresh = normalizeHistory(data.messages).filter(row => row._msg_id && !known.has(row._msg_id));
      if (fresh.length > 0) updateSession(id, { messages: [...sess.messages, ...fresh] });
    }
  } else if (data.command === 'branch') {
    // Branch switched — reload messages from new branch path.
    if (data.messages) {
      resetOlderHistory(id);
      updateSession(id, { messages: normalizeHistory(data.messages), historyTruncated: !!data.history_truncated });
    }
  }
}

function appendCompactionMarker(id, rawMarker) {
  const sess = store.get().sessions[id];
  if (!sess || !rawMarker) return;
  const [marker] = normalizeHistory([rawMarker]);
  if (!marker?._msg_id || sess.messages.some(message => message?._msg_id === marker._msg_id)) return;
  updateSession(id, { messages: [...sess.messages, marker] });
}


export function handleWsGoalChange(id, data) {
  const sess = store.get().sessions[id];
  const patch = {
    goalActive: !!data.active,
    goalObjective: data.active ? (data.objective || '') : null,
    goalWorkDir: data.active ? (data.work_dir || '') : null,
    goalIteration: data.iteration || 0,
    goalStalled: data.stalled || 0,
  };
  if (!data.active) patch.goalVerifying = false;
  // Live start line, matching the persisted "start" marker shown on reopen.
  // Only on a fresh activation (iteration 0) so a reconnect's goal_change echo
  // doesn't re-announce an already-running goal.
  if (sess && data.active && !sess.goalActive && (data.iteration || 0) === 0) {
    patch.messages = [...sess.messages, { _type: 'system', text: `🎯 Goal started: ${data.objective || ''}` }];
  }
  updateSession(id, patch);
}

export function handleWsGoalIteration(id, data) {
  const sess = store.get().sessions[id];
  if (!sess) return;
  const verdict = data.satisfied ? 'satisfied' : 'not done yet';
  let text = `🎯 Goal iteration ${data.iteration} — ${verdict}`;
  if (data.feedback && data.feedback.trim()) text += `\n${data.feedback}`;
  updateSession(id, {
    messages: [...sess.messages, { _type: 'system', text }],
    goalIteration: data.iteration || sess.goalIteration || 0,
  });
}

export function handleWsGoalVerify(id, data) {
  const sess = store.get().sessions[id];
  if (!sess) return;
  updateSession(id, { goalVerifying: !!data.active });
}

export function handleWsGoalEnd(id, data) {
  const sess = store.get().sessions[id];
  if (!sess) return;
  updateSession(id, {
    goalActive: false,
    goalObjective: null,
    goalWorkDir: null,
    goalVerifying: false,
    messages: [...sess.messages, { _type: 'system', text: `🎯 Goal ended: ${data.reason || ''}` }],
  });
  markUnseen(id);
}

export function handleWsAutoVerifyStart(id, data) {
  // The directory only travels with a manual /verify aimed at another
  // repository; auto-verify always runs in the session's own.
  updateSession(id, {
    autoVerifying: true,
    verifyDir: data?.dir || null,
    verifyManual: Boolean(data?.manual),
  });
}

export function handleWsAutoVerifyEnd(id, data) {
  updateSession(id, { autoVerifying: false, verifyDir: null, verifyManual: false });
}

export function handleWsCompactionStart(id) {
  updateSession(id, { compacting: true });
}

export function handleWsCompactionEnd(id, data) {
  updateSession(id, { compacting: false });
  appendCompactionMarker(id, data?.marker);
}
