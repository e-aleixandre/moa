// session-actions.js — API-backed session operations

import { api, retryHistoryHydration } from './api.js';
import { normalizeConversationProjection, normalizeHistory } from './ws-handlers.js';
import { triggerAttention, addToast } from './notifications.js';
import { store, setState, updateSession, visibleSessionIds } from './store.js';
import {
  assignToTile, setActiveSession, afterVisibilityChange, autoFillTiles,
  autoSelectMobile,
} from './tile-actions.js';
import { allSessionIds, clearSession } from './tileTree.js';
import { attentionArrival, forgetAttentionArrival, retainAttentionArrivals } from './attention-arrivals.js';

let pollTimer = null;

// newSteerId mints a client-side stable ID for an optimistic steer chip. The
// same ID is sent to the server and echoed back on the Steered event, so the
// chip has an authoritative identity from the moment it appears — no window
// where it must be reconciled by text. crypto.randomUUID is available in the
// secure contexts this app runs in (localhost / Tailscale HTTPS); the fallback
// keeps it working if that ever changes. The same mechanism mints the message
// ID of an optimistic user message (see sendMessage), which the server reuses
// for the message it appends, so the user_message broadcast dedups against it.
export function newSteerId() {
  try {
    if (typeof crypto !== 'undefined' && crypto.randomUUID) return 'c-' + crypto.randomUUID();
  } catch { /* fall through */ }
  return 'c-' + Date.now().toString(36) + '-' + Math.random().toString(36).slice(2, 10);
}

// cacheExpiresAtMs parses the server's cache_expires_at into an epoch-ms number,
// returning 0 for absent/unparseable/non-positive values. The backend omits the
// field when not applicable (omitzero), but a defensive guard keeps a stray Go
// zero-time ("0001-01-01…", which parses to a negative number) from being
// treated as a real deadline and spinning up a pointless UI timer.
function cacheExpiresAtMs(iso) {
  if (!iso) return 0;
  const ms = Date.parse(iso);
  return Number.isFinite(ms) && ms > 0 ? ms : 0;
}

function samePolledSession(existing, next) {
  return !!existing && Object.keys(next).every(key => Object.is(existing[key], next[key]));
}

export async function loadSessions() {
  try {
    const list = await api('GET', '/api/sessions');
    // Read the store AFTER the round-trip: WS handlers may have updated
    // sessions while the request was in flight, and rebuilding from a
    // pre-await snapshot would silently revert those (lost messages, perms).
    const state = store.get();
    const prev = state.sessions;
    const visible = new Set(visibleSessionIds(state));
    const sessions = {};
	const restartedVisibleSessions = [];
	let sessionsChanged = Object.keys(prev).length !== list.length;
    for (const info of list) {
      const existing = prev[info.id];
		const serverRestarted = !!existing?.serverInstance && !!info.server_instance
			&& existing.serverInstance !== info.server_instance;
		// /api/sessions is a snapshot taken before its response reaches us. Once
		// this client has a fenced /read confirmation, that old snapshot must not
		// relight the exact same occurrence locally. A larger generation (or a
		// different server namespace) remains a genuinely new occurrence.
		const pollGeneration = info.unseen_gen || 0;
		const staleAcknowledgedOccurrence = !!info.unseen && !serverRestarted && !!existing &&
			existing.lastAckedUnseenInstance === (info.server_instance || '') &&
			(existing.lastAckedUnseenGen || 0) >= pollGeneration;
		const polledUnseen = !!info.unseen && !staleAcknowledgedOccurrence;
      // A visible session has a live WS connection that owns its live-tracked
      // fields (state, config, context, plan). This poll response may already
      // be stale relative to WS events that arrived while the request was in
      // flight, so keep the WS-tracked values rather than reverting them.
      // Hidden sessions have no WS connection, so the poll is their only source
      // of truth and must refresh those fields. A *saved* session is visible
      // (e.g. just tapped to resume) but has NO socket either — syncConnections
      // never opens one for saved sessions — so the poll must own it too, or a
      // just-resumed session stays stuck 'saved' (grey dot, empty stream) until
      // the app is reopened.
      const wsOwns = existing && visible.has(info.id) && existing.state !== 'saved';
      const next = {
        id: info.id,
        title: info.title,
        state: wsOwns ? existing.state : info.state,
        model: wsOwns ? existing.model : info.model,
        provider: wsOwns ? existing.provider : info.provider,
        thinking: wsOwns ? existing.thinking : (info.thinking || ''),
        cwd: info.cwd,
        updated: info.updated ? Date.parse(info.updated) : (existing ? existing.updated : 0),
        cacheExpiresAt: cacheExpiresAtMs(info.cache_expires_at),
        error: wsOwns ? existing.error : (info.error || null),
        untrustedMcp: info.untrusted_mcp || false,
        // Who created the session: server-owned, omitted for ordinary user
        // sessions (see the Automation API's origin metadata). No WS event
        // tracks it, so the poll is the only source.
        origin: info.origin || '',
        // MCP health summary (poll-driven server truth): {total, ready,
        // unhealthy} or null when the session has no MCP servers. Not WS-owned —
        // it reflects the manager's live state, refreshed on each poll.
        mcp: info.mcp || null,
        messages: existing ? existing.messages : [],
        // A socket's init snapshot is the authority for retained cached
        // messages. Polling has no history, so it must never clear this visual
        // boundary while that snapshot is still in flight.
        historyPending: serverRestarted ? false : (existing ? !!existing.historyPending : false),
        // The roster has no transcript authority. Preserve both a failed
        // hydration boundary and proof that a later init did render one. A
        // process change invalidates that proof: for a displayed transcript we
        // make the boundary visible, then replace its socket below. A hidden
        // transcript is simply no longer authoritative; it need not raise an
        // alarm until the user opens it.
        historyStale: serverRestarted ? visible.has(info.id) : (existing ? !!existing.historyStale : false),
        historyHydrated: serverRestarted ? false : (existing ? !!existing.historyHydrated : false),
        historyAckProven: serverRestarted ? false : (existing ? !!existing.historyAckProven : false),
        historyShownGen: serverRestarted ? 0 : (existing ? existing.historyShownGen || 0 : 0),
        historyShownInstance: serverRestarted ? '' : (existing ? existing.historyShownInstance || '' : ''),
        // Display-only provenance of retained rows. Unlike historyShown*, this
        // survives opening a socket so a brief reconnect does not advertise an
        // up-to-date transcript as behind.
        historyCacheGen: existing ? existing.historyCacheGen || 0 : 0,
        historyCacheInstance: existing ? existing.historyCacheInstance || '' : '',
        historyTailNeeded: existing ? !!existing.historyTailNeeded : false,
        historyTailReady: existing ? !!existing.historyTailReady : false,
        historyTruncated: existing ? !!existing.historyTruncated : false,
        contextPercent: wsOwns ? existing.contextPercent : (info.context_percent ?? (existing ? existing.contextPercent : -1)),
        contextWindow: wsOwns ? existing.contextWindow : (info.context_window || (existing ? existing.contextWindow : 0)),
        compactAt: wsOwns ? existing.compactAt : (info.compact_at || (existing ? existing.compactAt : 0)),
        compactAtMin: wsOwns ? existing.compactAtMin : (info.compact_at_min || (existing ? existing.compactAtMin : 0)),
        permissionMode: wsOwns ? existing.permissionMode : (info.permission_mode || (existing ? existing.permissionMode : 'yolo')),
        pendingPerm: existing ? existing.pendingPerm : null,
        pendingAsk: existing ? existing.pendingAsk : null,
        // A remote resolution leaves a client-only transcript-tail explanation.
        // The roster does not carry this display state, so polling must not
        // erase it before the user can read it.
        resolvedPromptNotice: existing ? existing.resolvedPromptNotice : null,
        pendingSteers: existing ? existing.pendingSteers : null,
        streamingText: existing ? existing.streamingText : null,
        thinkingText: existing ? existing.thinkingText : null,
        runningTool: existing ? existing.runningTool : null,
        flash: existing ? existing.flash : null,
        subagentCount: existing ? existing.subagentCount : 0,
        // Live subagent transcripts are WS-only state (fed by subagent_start/
        // event/end); the poll response knows nothing about them, so always
        // carry them over or the agent tray vanishes on every poll tick.
        subagents: existing ? existing.subagents : {},
        viewingSubagent: existing ? existing.viewingSubagent : null,
        // viewingBashJob is the read-only counterpart (a background command's
        // detail view); client UI-only too, so it survives a poll the same way.
        viewingBashJob: existing ? existing.viewingBashJob : null,
        // dockOpen is the LiveDock's per-session open/closed preference (client
        // UI-only, no server field): preserved across polls exactly like
        // viewingSubagent, so switching sessions and back doesn't reset it.
        dockOpen: existing ? existing.dockOpen : false,
        autoVerifying: existing ? existing.autoVerifying : false,
        verifyDir: existing ? existing.verifyDir : null,
        verifyManual: existing ? existing.verifyManual : false,
        compacting: existing ? existing.compacting : false,
        onOverage: existing ? existing.onOverage : false,
        // Per-request rate-limit percents remain as an old-server fallback;
        // current OpenAI usage comes from the provider-wide /api/usage snapshot.
        rlFiveHourPct: existing ? existing.rlFiveHourPct : undefined,
        rlSevenDayPct: existing ? existing.rlSevenDayPct : undefined,
        // Live per-run state fed by WS (run start timestamp + the running token
        // tally). The poll knows nothing about them, so carry them over or the
        // activity timer and the ↑/↓ token counts vanish on every poll tick.
        runStartedAtMs: existing ? existing.runStartedAtMs : null,
        runTokensUp: existing ? existing.runTokensUp : undefined,
        runTokensDown: existing ? existing.runTokensDown : undefined,
        // Client-only counter of WS writes to the live per-run fields (see
        // nextRunEpoch in ws-handlers): a poll must not rewind it or an
        // in-flight send would misread "nothing happened meanwhile".
        runEpoch: existing ? existing.runEpoch : 0,
        tasks: existing ? existing.tasks : [],
        // Goal lifecycle and the MCP refresh counter are socket-owned. The
        // roster doesn't expose either, so keep them through its replacement.
        goalActive: existing ? existing.goalActive : false,
        goalObjective: existing ? existing.goalObjective : null,
        goalWorkDir: existing ? existing.goalWorkDir : null,
        goalIteration: existing ? existing.goalIteration : 0,
        goalStalled: existing ? existing.goalStalled : 0,
        goalVerifying: existing ? existing.goalVerifying : false,
        mcpTick: existing ? existing.mcpTick : 0,
        lastSeq: existing ? existing.lastSeq : 0,
        planMode: wsOwns ? existing.planMode : (info.plan_mode || (existing ? existing.planMode : 'off')),
        planFile: wsOwns ? existing.planFile : (info.plan_file || (existing ? existing.planFile : null)),
        costUSD: wsOwns ? existing.costUSD : (info.cost_usd ?? (existing ? existing.costUSD : 0)),
        unseen: polledUnseen,
        unseenGen: pollGeneration,
        serverUnseenGen: pollGeneration,
        serverUnseenInstance: info.server_instance || '',
        serverInstance: info.server_instance || (existing ? existing.serverInstance : ''),
		// A generation namespace belongs to one server process. Keep the local
		// read high-water across stale roster replacements, but discard it when
		// the server process changes so its fresh generations can be acknowledged.
		lastAckedUnseenGen: serverRestarted ? 0 : (existing ? existing.lastAckedUnseenGen || 0 : 0),
		lastAckedUnseenInstance: serverRestarted ? '' : (existing ? existing.lastAckedUnseenInstance || '' : ''),
        // One global client arrival sequence covers every session. Repeating
        // the same server-instance occurrence after a poll returns its original value.
        attentionArrival: polledUnseen
          ? attentionArrival(info.id, pollGeneration, info.server_instance || '')
          : (existing ? existing.attentionArrival || 0 : 0),
        // Server-owned session brief (cheap LLM status summary): attempting /
        // progress prose + freshness stamp. No WS event tracks it, so the poll
        // is the source of truth. Preserve the prior value when the poll omits
        // it (omitempty) so a not-yet-generated brief doesn't flicker.
        briefAttempting: info.brief_attempting ?? (existing ? existing.briefAttempting : ''),
        briefProgress: info.brief_progress ?? (existing ? existing.briefProgress : ''),
        briefUpdated: info.brief_updated ? Date.parse(info.brief_updated) : (existing ? existing.briefUpdated : 0),
      };
		if (samePolledSession(existing, next)) {
			sessions[info.id] = existing;
		} else {
			sessions[info.id] = next;
			sessionsChanged = true;
		}
		if (serverRestarted && visible.has(info.id) && existing.state !== 'saved') {
			restartedVisibleSessions.push(info.id);
		}
    }
    // Detect attention transitions (hidden sessions only)
    for (const [id, sess] of Object.entries(sessions)) {
      const prevSess = prev[id];
      if (prevSess && prevSess.state !== sess.state) {
        if (sess.state === 'permission' || sess.state === 'error') {
          if (!visible.has(id)) {
            triggerAttention(sess, null, state.soundEnabled);
          }
        }
      }
    }
		if (sessionsChanged) {
			setState({ sessions });
		}
		// An existing socket belongs to the old process and cannot restore the
		// transcript authority that its roster just revoked. Replace it rather
		// than waiting for the old transport to notice the restart.
		for (const id of restartedVisibleSessions) retryHistoryHydration(id);
    // Clean deleted sessions from tile tree
    const validIds = new Set(Object.keys(sessions));
    retainAttentionArrivals(validIds);
    const currentState = store.get();
    let tree = currentState.tileTree;
    let changed = false;
    for (const sid of allSessionIds(tree)) {
      if (!validIds.has(sid)) {
        tree = clearSession(tree, sid);
        changed = true;
      }
    }
    if (changed) setState({ tileTree: tree });
    afterVisibilityChange();
  } catch (e) {
    console.error('loadSessions failed:', e);
  }
}

export function startPolling() {
  stopPolling();
  // On mobile only one session is visible (and WS-backed); the poll just keeps
  // the list and hidden sessions fresh, and push covers anything urgent — so a
  // slower cadence saves battery/data. The foreground handler refreshes on
  // return, so a stale gap while backgrounded doesn't matter.
  const interval = store.get().isMobile ? 15000 : 3000;
  pollTimer = setInterval(loadSessions, interval);
}

export function stopPolling() {
  if (pollTimer) { clearInterval(pollTimer); pollTimer = null; }
}

let usageTimer = null;

// loadUsage refreshes the global plan usage snapshot. Failures keep the
// previous snapshot rather than clearing the widget.
export async function loadUsage() {
  try {
    const usage = await api('GET', '/api/usage');
    setState({ usage });
  } catch (e) {
    console.error('loadUsage failed:', e);
  }
}

export function startUsagePolling() {
  stopUsagePolling();
  loadUsage();
  usageTimer = setInterval(loadUsage, 60000);
}

export function stopUsagePolling() {
  if (usageTimer) { clearInterval(usageTimer); usageTimer = null; }
}

export async function createSession(opts) {
  const sess = await api('POST', '/api/sessions', opts, { timeoutMs: 0 });
  await loadSessions();
  const state = store.get();
  const id = sess.id;
  if (state.isMobile) {
    setActiveSession(id);
  } else {
    assignToTile(state.focusedTile, id);
  }
  return sess;
}

export async function deleteSession(id) {
  await api('DELETE', `/api/sessions/${id}`);
  // Read the store after the await so concurrent WS updates to other
  // sessions aren't clobbered by a stale pre-request snapshot.
  const state = store.get();
  const sessions = { ...state.sessions };
  delete sessions[id];
  const tileTree = clearSession(state.tileTree, id);
  const activeSession = state.activeSession === id ? null : state.activeSession;
  forgetAttentionArrival(id);
  setState({ sessions, tileTree, activeSession });
  afterVisibilityChange();
}

// closeSession "closes" a session: unlike deleteSession it keeps the
// conversation on disk — the server just unloads it from memory, so it lists as
// `saved` and reopens with resumeSession, losing nothing. It still has to drop
// the session from wherever it is currently visible (tile/activeSession),
// mirroring deleteSession, so the UI doesn't keep showing a closed session as
// if it were live.
//
// The server refuses (409) while the session is still working — a run, a
// pending permission, or background subagents/bash jobs whose output closing
// would kill. Surface that as a toast instead of failing silently: the user
// needs to know the session is still open, and what to do about it.
export async function closeSession(id) {
  try {
    await api('POST', `/api/sessions/${id}/close`);
  } catch (e) {
    const busy = String(e.message || e).startsWith('409');
    addToast({
      title: busy ? 'Session is still working' : 'Could not close session',
      detail: busy
        ? 'Cancel the run (or wait for background work to finish) before closing it.'
        : String(e.message || e),
      type: busy ? 'attention' : 'error',
    });
    throw e;
  }
  // Reflect immediately so the UI updates without waiting for the next poll
  // (which can lag up to ~15s on mobile). The server already committed above.
  updateSession(id, {
    state: 'saved',
    historyAckProven: false,
    lastAckedUnseenGen: 0,
    lastAckedUnseenInstance: '',
  });
  const state = store.get();
  const tileTree = clearSession(state.tileTree, id);
  const activeSession = state.activeSession === id ? null : state.activeSession;
  setState({ tileTree, activeSession });
  // Hand the freed slot straight to another open session. Closing does NOT
  // remove the session from the store (it stays listed as `saved`), so the
  // app-level effect that re-fills when the session COUNT changes never fires
  // on this path: without this, closing the session you were looking at left
  // mobile on the "no open sessions" empty state (and a desktop tile blank)
  // while other sessions were still open — precisely what the drawer kept
  // listing. deleteSession never showed it because there the count does change.
  if (store.get().isMobile) autoSelectMobile();
  else autoFillTiles();
  afterVisibilityChange();
}

// cancelBashJob asks the server to stop a background command. The kill is
// echoed over the WebSocket (bash_job_end with status cancelled), which flips
// the job's status in the store, so there is nothing optimistic to write here.
// A refusal (the job already ended between the tap and the POST → 404) is
// surfaced as a toast rather than silently: the button was live, so the user
// expects an answer.
export async function cancelBashJob(sessionId, jobId) {
  try {
    return await api('POST', `/api/sessions/${sessionId}/bash-jobs/${jobId}/cancel`);
  } catch (e) {
    addToast({
      sessionId,
      title: 'Could not stop the job',
      detail: String(e.message || e).startsWith('404')
        ? 'It already finished.'
        : String(e.message || e),
      type: 'attention',
    });
    throw e;
  }
}

// openBashJob opens a background bash job's read-only detail view (command,
// live output, stop). Unlike openPersistedSubagent there is no disk fallback:
// a bash job has no persisted transcript endpoint, and once it ends its output
// lands inline in the conversation as a card — so this only opens jobs still
// present in the store (i.e. the ones the LiveDock lists).
export function openBashJob(id, jobId) {
  const sess = store.get().sessions[id];
  if (!sess || !sess.subagents || !sess.subagents[jobId]) return;
  updateSession(id, { viewingBashJob: jobId, viewingSubagent: null });
}

// attachmentToContent builds the local content needed for the immediate
// optimistic echo. Files remain file descriptors even before the send resolves.
function attachmentToContent(a) {
  if (a.isImage) {
    return { type: 'image', data: a.data, mime_type: a.mime };
  }
  return { type: 'document', mime_type: a.mime, filename: a.name };
}

// isOptimisticAttachmentBlock recognises the content blocks that carry a local
// (not yet durable) attachment: inline image data or a file chip.
function isOptimisticAttachmentBlock(block) {
  return (block.type === 'image' && block.data) || block.type === 'document';
}

// durableAttachmentContent rewrites `content`, swapping the optimistic
// attachment blocks for the durable descriptors the server stored. It returns
// null (leave the local view alone) unless the server returned exactly one
// descriptor per optimistic attachment: pairing a partial response by position
// could assign a descriptor to the wrong block, so we stay conservative and let
// a reload reconcile instead.
function durableAttachmentContent(content, attachments) {
  if (!Array.isArray(attachments) || attachments.length === 0) return null;
  const optimistic = content.filter(isOptimisticAttachmentBlock);
  if (attachments.length !== optimistic.length) return null;
  let index = 0;
  return content.map((block) => {
    if (!isOptimisticAttachmentBlock(block)) return block;
    const attachment = attachments[index++];
    return {
      type: attachment.kind === 'file' ? 'document' : 'image',
      attachment_id: attachment.id,
      attachment_size: attachment.size,
      mime_type: attachment.mime,
      filename: attachment.name,
    };
  });
}

// adoptMessageRail reconciles the local view with a response that says the
// message started a run (action "send"), whichever rail we optimistically put
// it on.
//   - We predicted a run: adopt the effective msg_id (the server re-mints one
//     that is malformed or already used), or drop our echo when the
//     authoritative broadcast already landed under that ID (it can beat this
//     response).
//   - We predicted a queued chip (the run we saw had already ended): the chip
//     is fiction — drop it and show the message instead.
function adoptMessageRail(id, { optimisticMsg, msgId, effMsgId, optimisticSteer, content }) {
  const cur = store.get().sessions[id];
  if (!cur) return;
  const messages = cur.messages || [];
  const already = messages.some((m) => m._msg_id === effMsgId);
  const patch = {};
  if (optimisticMsg) {
    if (effMsgId === msgId) return;
    patch.messages = already
      ? messages.filter((m) => m._msg_id !== msgId)
      : messages.map((m) => (m._msg_id === msgId ? { ...m, _msg_id: effMsgId } : m));
  } else {
    const kept = (cur.pendingSteers || []).filter((s) => s !== optimisticSteer);
    patch.pendingSteers = kept.length > 0 ? kept : null;
    if (!already) patch.messages = [...messages, { role: 'user', _msg_id: effMsgId, content }];
  }
  updateSession(id, patch);
}

// adoptSteerRail reconciles the local view with a response that says the
// message was queued (action "steer"), whichever rail we optimistically put it
// on. When we predicted a run (our snapshot said idle, but the session was busy
// or had a queued item by the time the server decided), the optimistic message
// is fiction: drop it and show the confirmed chip instead — unless a Steered
// event already delivered the chip, which is the authoritative removal.
function adoptSteerRail(id, { optimisticMsg, effSteerId, images, text, prevRunStartedAtMs, prevRunEpoch }) {
  const cur = store.get().sessions[id];
  if (!cur) return;
  const steers = cur.pendingSteers || [];
  const patch = {};
  if (optimisticMsg) {
    patch.messages = (cur.messages || []).filter((m) => m !== optimisticMsg);
    // No run started for this message, so undo the "fresh run" part of the
    // optimistic patch: restore the start time of the run that is actually in
    // flight. The session itself stays "running" — the server only queues when
    // a run is in flight or one is about to be pumped, and a later state_change
    // corrects it either way.
    //
    // Only if no WS event wrote the per-run fields while the POST was in
    // flight: a real run (this session's or another client's) may have started
    // meanwhile, and restoring the pre-send timestamp on top of it would
    // replace live state with stale state.
    if ((cur.runEpoch || 0) === prevRunEpoch) {
      patch.runStartedAtMs = prevRunStartedAtMs ?? cur.runStartedAtMs ?? null;
    }
    // The token tally is deliberately NOT restored: the server answering
    // "steer" means a run this client didn't know about is in flight, so the
    // pre-send figures belong to an older, finished run. Even the epoch guard
    // can't tell them apart when the POST resolves before the state_change
    // frame for that run arrives. Leaving the optimistic 0/0 is the honest
    // approximation ("run in progress, tally unknown yet") and the next
    // run_tokens event — frequent while a run is active — writes the truth.
  }
  const delivered = (cur.messages || []).some((m) => m._steer_id === effSteerId);
  const existing = steers.find((s) => s.id === effSteerId);
  if (!delivered) {
    const chip = { ...(existing || { id: effSteerId, text }), confirmed: true };
    if (!existing && images > 0) chip.images = images;
    patch.pendingSteers = existing
      ? steers.map((s) => (s === existing ? chip : s))
      : [...steers, chip];
  } else if (existing) {
    const kept = steers.filter((s) => s !== existing);
    patch.pendingSteers = kept.length > 0 ? kept : null;
  }
  updateSession(id, patch);
}

export async function sendMessage(id, text, attachments = []) {
  const state = store.get();
  const sess = state.sessions[id];
  if (!sess) return;

  const isIdle = sess.state === 'idle' || sess.state === 'error';
  let optimisticMsg = null;
  let optimisticSteer = null;
  // Mint BOTH identities up front and send both: the local state we predict
  // from is a stale copy, and only the server knows (atomically) whether this
  // message starts a run or joins the queue. It picks the identity for the rail
  // it actually used and reports it back, so either outcome lands under an ID
  // this client already knows.
  const steerId = newSteerId();
  const msgId = newSteerId();
  // Remember the live per-run token tally so a rejected send can restore it
  // (the optimistic patch below resets it to start the new run at zero).
  const prevTokensUp = sess.runTokensUp;
  const prevTokensDown = sess.runTokensDown;
  const prevRunStartedAtMs = sess.runStartedAtMs;
  // Snapshot taken BEFORE the optimistic patch below (which is local, so it
  // doesn't bump the epoch): any change by the time the response lands means a
  // WS event owns the per-run fields now.
  const prevRunEpoch = sess.runEpoch || 0;
  if (isIdle) {
    // Attachment blocks first, text last — matches the order the server sends
    // to the agent (see Manager.Send).
    const content = attachments.map(attachmentToContent);
    if (text) content.push({ type: 'text', text });
    // The optimistic echo carries the identity the server will append the
    // message under: the authoritative user_message broadcast (which also
    // reaches other tabs and API clients) then dedups against this echo
    // instead of duplicating it.
    optimisticMsg = { role: 'user', _msg_id: msgId, content };
    updateSession(id, {
      messages: [...sess.messages, optimisticMsg],
      state: 'running',
      streamingText: null,
      thinkingText: null,
      runStartedAtMs: Date.now(),
      // A fresh run begins here. Reset the live token tally now, from the same
      // optimistic patch that sets runStartedAtMs — the WS state_change reset
      // won't fire because this patch already made the session "running".
      runTokensUp: 0,
      runTokensDown: 0,
    });
  } else {
    const current = store.get().sessions[id];
    const steers = current?.pendingSteers || [];
    // The optimistic chip carries its authoritative identity immediately —
    // there is no id == null window. The same ID is sent to the server
    // (steer_id) and echoed on the Steered event, so reconnect snapshots and
    // cross-device events reconcile by identity, not by text (closes the
    // double-send and cancel-vs-in-flight races).
    optimisticSteer = { id: steerId, text };
    const imageCount = attachments.filter((a) => a.isImage).length;
    if (imageCount > 0) optimisticSteer.images = imageCount;
    updateSession(id, { pendingSteers: [...steers, optimisticSteer] });
  }

  try {
    const res = await api('POST', `/api/sessions/${id}/send`, {
      text,
      attachments: attachments.map((a) => ({ name: a.name, mime: a.mime, data: a.data })),
      steer_id: steerId,
      msg_id: msgId,
    }, { timeoutMs: 0 });
    // The response is authoritative about WHICH rail took the message: our
    // local prediction can be wrong in both directions (a run started between
    // our snapshot and the request, or the run we thought was live had ended).
    const action = res?.action === 'steer' ? 'steer' : 'send';
    const effMsgId = res?.msg_id || msgId;
    const effSteerId = res?.steer_id || steerId;
    if (action === 'send' && optimisticMsg) {
      const cur = store.get().sessions[id];
      const content = durableAttachmentContent(optimisticMsg.content, res?.attachments);
      if (content && cur?.messages?.includes(optimisticMsg)) {
        updateSession(id, {
          messages: cur.messages.map((message) => (
            message === optimisticMsg ? { ...optimisticMsg, content } : message
          )),
        });
      }
    }
    if (action === 'send') {
      // Content for the case where we had no optimistic message (we predicted a
      // chip): rebuild the blocks the same way the idle path does, then swap in
      // the durable descriptors — the server stored the attachments, so the
      // adopted message must point at them instead of carrying local blocks
      // that render as unavailable on this device.
      const local = attachments.map(attachmentToContent);
      if (text) local.push({ type: 'text', text });
      const content = (!optimisticMsg && durableAttachmentContent(local, res?.attachments)) || local;
      adoptMessageRail(id, { optimisticMsg, msgId, effMsgId, optimisticSteer, content });
    } else {
      adoptSteerRail(id, {
        optimisticMsg,
        effSteerId,
        text,
        images: attachments.filter((a) => a.isImage).length,
        prevRunStartedAtMs,
        prevRunEpoch,
      });
    }
    return action;
  } catch (e) {
    // Roll back the optimistic echo so a rejected send (e.g. 400 on a bad
    // attachment) doesn't leave a phantom message stuck in "running". Remove
    // exactly the message we appended (by reference), leaving any events that
    // arrived meanwhile untouched.
    if (optimisticMsg) {
      const cur = store.get().sessions[id];
      if (cur) {
        updateSession(id, {
          messages: cur.messages.filter((m) => m !== optimisticMsg),
          state: 'idle',
          streamingText: null,
          thinkingText: null,
          // Restore the token tally reset by the optimistic patch: this run
          // never actually started.
          runTokensUp: prevTokensUp,
          runTokensDown: prevTokensDown,
        });
      }
    }
    // Roll back the optimistic steer chip too: a rejected steer (e.g. 503 queue
    // full, or a network error) must not leave a phantom chip. Its client-minted
    // ID was never accepted by the server, so mergeSteers would keep resurrecting
    // it on reconnect until removed here.
    if (optimisticSteer) {
      const cur = store.get().sessions[id];
      if (cur?.pendingSteers) {
        const kept = cur.pendingSteers.filter((s) => s !== optimisticSteer);
        updateSession(id, { pendingSteers: kept.length > 0 ? kept : null });
      }
    }
    throw e;
  }
}

export async function cancelRun(id) {
  return api('POST', `/api/sessions/${id}/cancel-and-recall`);
}

// cancelSteers drops every steer message still queued (not yet delivered) on
// the server. Called when the user pulls queued messages back into the input to
// edit them, so the agent doesn't also deliver the originals (double-delivery).
export async function cancelSteers(id) {
  await api('POST', `/api/sessions/${id}/steers/cancel`);
}

export async function cancelSubagent(id, jobId) {
  await api('POST', `/api/sessions/${id}/subagents/${jobId}/cancel`);
}

// promoteSubagent detaches a synchronous (blocking) subagent so it keeps
// running in the background after the turn that spawned it ends. The server
// echoes the flip over the WebSocket (subagent_start with async:true), which
// flips sa.async in the store and makes the promote button disappear.
export async function promoteSubagent(id, jobId) {
  await api('POST', `/api/sessions/${id}/subagents/${jobId}/promote`);
}

// steerSubagent sends a message to a live subagent's child agent. Returns the
// server response ({ queued: bool }); there's no WS echo for this (parity with
// cancelSubagent), so the caller shows optimistic visual feedback.
export async function steerSubagent(id, jobId, text) {
  return api('POST', `/api/sessions/${id}/subagents/${jobId}/steer`, { text });
}

// openPersistedSubagent loads a finished subagent's transcript from disk and
// opens it in the SubagentView. Used when clicking a subagent card in the chat
// after the live tray entry is gone.
export async function openPersistedSubagent(id, jobId) {
  const sess = store.get().sessions[id];
  if (!sess) return;
  // A running transcript is already authoritative enough for live viewing.
  // Terminal cards always reload their persisted transcript so Conversation
  // opens the complete child history rather than a possibly lossy live cache.
  const existing = sess.subagents && sess.subagents[jobId];
  if (existing && (existing.status === 'running' || existing.status === 'cancelling')) {
    updateSession(id, { viewingSubagent: jobId, viewingBashJob: null });
    return;
  }
  const t = await api('GET', `/api/sessions/${id}/subagents/${jobId}`);
  if (!t) return;
  const transcript = t.order === 'newest_first'
    ? [...(t.messages || [])].reverse()
    : (t.messages || []);
  // The endpoint reports the child's own figures at top level; older payloads
  // nested the tokens under `usage`, so both shapes are read.
  const inputTokens = t.input_tokens != null ? t.input_tokens : (t.usage && t.usage.input) || 0;
  const outputTokens = t.output_tokens != null ? t.output_tokens : (t.usage && t.usage.output) || 0;
  const usage = (t.cost_usd || inputTokens || outputTokens)
    ? { inputTokens, outputTokens, costUSD: t.cost_usd || 0 }
    : null;
  const subs = { ...(store.get().sessions[id].subagents || {}) };
  subs[jobId] = {
    jobId,
    task: t.task || '',
    model: t.model || '',
    thinking: t.thinking || 'off',
    status: t.status || 'completed',
    async: !!t.async,
    messages: normalizeConversationProjection(transcript),
    streamingText: null,
    thinkingText: null,
    usage,
    contextPercent: t.context_percent == null ? -1 : t.context_percent,
  };
  // Clearing viewingBashJob keeps the two detail views mutually exclusive:
  // only one thing is being looked at, whichever was opened last.
  updateSession(id, { subagents: subs, viewingSubagent: jobId, viewingBashJob: null });
}

export async function resolvePermission(sessionId, permId, approved, opts = {}) {
  await api('POST', `/api/sessions/${sessionId}/permission`, {
    id: permId,
    approved,
    feedback: opts.feedback || '',
    allow: opts.allow || '',
  });
  updateSession(sessionId, { pendingPerm: null });
}

export async function addPermissionRule(sessionId, permId, rule) {
  await api('POST', `/api/sessions/${sessionId}/permission`, {
    id: permId,
    action: 'add_rule',
    rule,
  });
}

export async function resolveAskUser(sessionId, askId, answers) {
  await api('POST', `/api/sessions/${sessionId}/ask`, {
    id: askId, answers,
  });
  updateSession(sessionId, { pendingAsk: null });
}

export async function resumeSession(id) {
  const sess = await api('POST', `/api/sessions/${id}/resume`, undefined, { timeoutMs: 0 });
  await loadSessions();
  const state = store.get();
  if (state.isMobile) {
    setActiveSession(sess.id);
  } else {
    assignToTile(state.focusedTile, sess.id);
  }
  return sess;
}

export async function configureSession(id, { model, thinking, permissionMode, compactAt }) {
  const body = {};
  if (model) body.model = model;
  if (thinking) body.thinking = thinking;
  if (permissionMode) body.permission_mode = permissionMode;
  // 0 means "compact at the model window", so this one is sent whenever it is
  // present at all — a falsy check would make "back to auto" unsendable.
  if (compactAt != null) body.compact_at = compactAt;
  const res = await api('PATCH', `/api/sessions/${id}/config`, body);
  if (res) {
    const patch = {};
    if (res.model) patch.model = res.model;
    if (res.thinking) patch.thinking = res.thinking;
    if (res.permission_mode) patch.permissionMode = res.permission_mode;
    if (res.compact_at != null) patch.compactAt = res.compact_at;
    updateSession(id, patch);
  }
  return res;
}

export async function trustMcp(id) {
  await api('POST', `/api/sessions/${id}/trust-mcp`, undefined, { timeoutMs: 0 });
  updateSession(id, { untrustedMcp: false });
}

export async function execCommand(id, command, steerId = '') {
  const res = await api('POST', `/api/sessions/${id}/command`, { command, id: steerId || undefined }, { timeoutMs: 0 });
  if (res && res.newSessionId) {
    await loadSessions();
    const state = store.get();
    if (state.isMobile) {
      setActiveSession(res.newSessionId);
    } else {
      assignToTile(state.focusedTile, res.newSessionId);
    }
  }
  return res;
}

// fetchBranchPoints returns the conversation's branch targets (user/assistant
// turns) for the rewind picker.
export async function fetchBranchPoints(id) {
  return api('GET', `/api/sessions/${id}/branches`);
}

// branchTo rewinds the conversation to entryId, starting a new branch from that
// point. The server publishes a CommandExecuted event that reloads the message
// list over the WebSocket, so callers don't need to apply the result manually.
export async function branchTo(id, entryId) {
  return api('POST', `/api/sessions/${id}/branch`, { entry_id: entryId });
}

// rewindToMessage — branch to one message straight from the transcript, the
// action behind a waypoint's rewind mark. Same call the RewindTimeline makes;
// it exists here so both screens share one failure story instead of writing
// their own. The message list refreshes itself off the WS 'branch' command, so
// there is nothing to do on success.
export function rewindToMessage(id, msgId) {
  return branchTo(id, msgId).catch((e) =>
    addToast({
      title: 'Could not rewind',
      detail: String(e.message || e),
      type: 'error',
    })
  );
}

export async function execShell(id, command, silent) {
  // The server permits user shell commands to run for up to five minutes.
  const result = await api('POST', `/api/sessions/${id}/shell`, { command, silent }, { timeoutMs: 0 });
  const output = (result.output || '').replace(/\n$/, '');
  const isError = result.exit_code !== 0 || result.timed_out;

  const state = store.get();
  const sess = state.sessions[id];
  if (sess) {
    const toolMsg = {
      _type: 'tool_start',
      tool_call_id: 'shell_' + Date.now(),
      tool_name: 'bash',
      args: { command },
      status: isError ? 'error' : 'done',
      result: result.timed_out ? `${output}\n(timed out)` : output,
    };
    updateSession(id, { messages: [...sess.messages, toolMsg] });
  }

  if (result.delivery_error) {
    addToast({ title: 'Shell output not delivered', detail: result.delivery_error, type: 'error' });
  }

  return result;
}
