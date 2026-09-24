// voice-live.js — the voice delegate call: WebRTC transport against our own
// server (GPT-Live), the tool loop that runs in this browser, and the rescue
// contract that guarantees a call never ends with nothing to show for it.
//
// Framework-free and dependency-injected on purpose (fetch, RTCPeerConnection,
// getUserMedia, Audio, timers), exactly like VoiceCaptureController: the
// sequencing rules that matter here — the data channel exists before the
// offer, the session is never started from the client, an ask is acknowledged
// immediately and answered later, a session that exists server-side is always
// closed — are testable without a browser or a render.

export const MAX_QUESTIONS = 5;
export const ASK_POLL_MS = 2000;
export const ASK_TIMEOUT_MS = 90_000;
// ICE gathering must COMPLETE: the offer travels in a single HTTP request, so
// a partial candidate set is an offer that may never connect. The documented
// client waits with a 10s timeout and fails the attempt on expiry.
export const ICE_TIMEOUT_MS = 10_000;
export const SESSION_START_TIMEOUT_MS = 20_000;
export const CHANNEL_OPEN_TIMEOUT_MS = 10_000;
// session.closed is the normal teardown trigger: it carries the final usage
// and may be preceded by late transcript deltas. This backstop only exists so
// a session that never confirms cannot hold the microphone forever; when it
// fires, finalization is explicitly unconfirmed.
export const CLOSE_BACKSTOP_MS = 15_000;
// A session-level error may end the session or may only cut the current
// speech. The event alone does not say which, so we wait for evidence: any
// further session event proves it is alive.
export const SESSION_ERROR_GRACE_MS = 10_000;
// "disconnected" is recoverable in WebRTC; "failed"/"closed" are not.
export const DISCONNECT_GRACE_MS = 10_000;
// How often the server is told this call still has somebody on it. The server
// closes a call it stops hearing from, because a locked screen, a closed tab
// and a dropped network all leave the same silence and none of them sends
// session.close.
export const HEARTBEAT_MS = 20_000;
// When the owner hangs up before the delegate wrote the minutes, the delegate
// is asked for them and gets this long to answer. It is billed voice time and
// the owner has already said he is done, so it is short; if nothing arrives,
// the transcript rescue happens exactly as before.
export const MINUTES_ON_HANGUP_MS = 8_000;
// How long the call tolerates a microphone that is not demonstrably live
// (page hidden, track muted or ended) before hanging up by itself. A delegate
// talking to nobody still burns voice seconds and still asks the session
// questions, so silence has to have an end.
export const MIC_GRACE_MS = 30_000;
// session.thinking.append takes at most 500 TOKENS, and tokens are not
// characters: Spanish runs ~4 characters per token, CJK closer to one, and an
// emoji can cost three. We bound UTF-8 bytes at a pessimistic 2 bytes per
// token and spend only 400 of the 500, so an estimate that is still optimistic
// cannot cross the real limit.
export const THINKING_TOKEN_BUDGET = 400;
const BYTES_PER_TOKEN = 2;

const REQUEST_HEADERS = { 'Content-Type': 'application/json', 'X-Moa-Request': '1' };

// What the owner reads when a call will not start. Every sentence names the
// problem and says what to do about it; none of them is the server's response
// body. A raw `{"error":"live session rate limit exceeded"}` in a toast is not
// an error message, it is a leak of the wire format.
const FAILURE_COPY = {
  rate_limited: 'Could not start the call: too many voice sessions open. Wait a few seconds and try again.',
  upstream_busy: 'Could not start the call: the voice provider has too many sessions open for this project. Wait a few seconds and try again.',
  no_api_key: 'Voice calls need an OpenAI API key on this server. Add one to its configuration, then try again.',
  upstream_unreachable: 'Could not reach the voice provider. Check the server\u2019s connection and try again.',
  upstream_refused: 'The voice provider refused to start the call. The reason is in the server log.',
  upstream_unreadable: 'The voice provider answered something this server could not use. Try again; the response is in the server log.',
  conversation_unavailable: 'This conversation could not be prepared for a call. Reopen it and try again.',
  unknown_session: 'This conversation is no longer open on the server. Reload the page and try again.',
  device_revoked: 'This device is no longer paired. Pair it again to make calls.',
  bad_request: 'The server rejected the call request. Reload the page and try again.',
};

export function callFailureMessage(status, cause) {
  const known = FAILURE_COPY[cause];
  if (known) return known;
  if (status === 429) return FAILURE_COPY.rate_limited;
  if (status === 403) return FAILURE_COPY.device_revoked;
  if (status >= 500) return 'The voice service is unavailable right now. Try again in a moment.';
  return 'Could not start the call. Reload the page and try again.';
}

export const MIC_LIVE = 'live';
export const MIC_NOT_LIVE = 'not-live';
export const MIC_UNKNOWN = 'unknown';

function truncate(text, max) {
  const value = String(text ?? '');
  if (value.length <= max) return value;
  return value.slice(0, max - 1) + '…';
}

function utf8Bytes(text) {
  if (typeof TextEncoder !== 'undefined') return new TextEncoder().encode(text).length;
  return text.length * 4; // pessimistic: no measurement available
}

// Truncate on code points, never on UTF-16 units: half a surrogate pair is not
// a character and would be sent as replacement garbage.
export function truncateForTokens(text, tokenBudget = THINKING_TOKEN_BUDGET) {
  const value = String(text ?? '');
  const maxBytes = tokenBudget * BYTES_PER_TOKEN;
  if (utf8Bytes(value) <= maxBytes) return value;
  const chars = Array.from(value);
  let low = 0;
  let high = chars.length;
  while (low < high) {
    const mid = Math.ceil((low + high) / 2);
    if (utf8Bytes(chars.slice(0, mid).join('') + '…') <= maxBytes) low = mid;
    else high = mid - 1;
  }
  return chars.slice(0, low).join('') + '…';
}

// The transcript is the rescue payload, not captions: it keeps speaker and
// order so an interrupted call can still be read as a conversation.
export function renderTranscript(turns) {
  return turns
    .map(({ speaker, text }) => `${speaker === 'owner' ? 'Owner' : 'Delegate'}: ${text.trim()}`)
    .filter((line) => !/:\s*$/.test(line))
    .join('\n');
}

// The minutes are whatever the delegate wrote, verbatim. There is no second
// client-side section: the backend prompt already asks for Decided / Still open
// / Pending confirmation inside the minutes, in the language of the call. A
// separate field rendered here produced an English heading appended to Spanish
// minutes, contradicting the section the delegate had already written.
export function formatMinutes({ minutes }) {
  return String(minutes || '').trim();
}

// What a finished call cost, in one line, for the notice that replaces the
// panel. "about" is not hedging for its own sake: without a confirmed
// session.closed the duration is the last figure seen, not the billed one.
export function callSpendNotice(meta) {
  const cost = meta?.cost;
  if (!cost || typeof cost.voiceUSD !== 'number') return '';
  const mins = Math.floor((cost.billedSeconds || 0) / 60);
  const secs = Math.round((cost.billedSeconds || 0) % 60);
  const duration = `${mins}:${String(secs).padStart(2, '0')}`;
  const prefix = meta.usageConfirmed ? '' : 'about ';
  const backend = `${cost.backendModel || 'The backend model'} is billed separately and is not counted here`;
  return `${duration} of voice, ${prefix}$${cost.voiceUSD.toFixed(2)}. ${backend}.`;
}

// The call's result is APPENDED as its own block and never replaces anything.
// The owner may have typed while the call ran, and an insertion at the caret
// (or over a selection) would destroy what he wrote. Losing his own words to
// the delegate's minutes is the one outcome this feature may never produce.
export function appendCallResult(value, text) {
  const base = String(value ?? '').replace(/\s+$/, '');
  const block = String(text ?? '').trim();
  if (!block) return String(value ?? '');
  return base ? `${base}\n\n${block}` : block;
}

function cancelledError(reason) {
  const error = new Error(`voice call cancelled (${reason})`);
  error.voiceLiveCancelled = true;
  return error;
}

export class VoiceLiveController {
  constructor(options = {}) {
    this.sessionId = options.sessionId || '';
    this.note = options.note || '';
    this.fetch = options.fetch || ((...args) => globalThis.fetch(...args));
    this.RTCPeerConnection = options.RTCPeerConnection || globalThis.RTCPeerConnection;
    this.getUserMedia = options.getUserMedia
      || ((constraints) => navigator.mediaDevices.getUserMedia(constraints));
    this.Audio = options.Audio || globalThis.Audio;
    this.setTimeout = options.setTimeout || ((fn, ms) => setTimeout(fn, ms));
    this.clearTimeout = options.clearTimeout || ((id) => clearTimeout(id));
    this.now = options.now || Date.now;
    this.documentRef = options.document ?? (typeof document !== 'undefined' ? document : null);

    this.callbacks = {};
    this.setCallbacks(options);

    this.epoch = 0;
    this.disposed = false;
    this.pc = null;
    this.channel = null;
    this.stream = null;
    this.track = null;
    this.audio = null;
    this.askTimers = new Set();
    this.micGraceTimer = null;
    this.closeTimer = null;
    this.minutesTimer = null;
    this.heartbeatTimer = null;
    this.sessionErrorTimer = null;
    this.disconnectTimer = null;
    this.onVisibility = null;
    this.endWaiters = [];
    this.resetCall();
  }

  // Every per-call fact is reset here. Carrying any of it across calls means a
  // second call can deliver the FIRST call's minutes, or start at 5/5
  // questions, or report the previous call's voice seconds as its cost.
  resetCall() {
    this.phase = 'idle';          // idle | connecting | live | closing | ended
    this.error = '';
    this.questionsUsed = 0;
    this.pendingAsks = 0;
    this.micState = MIC_UNKNOWN;
    this.voiceSeconds = 0;
    this.startedAt = 0;
    this.endedReason = '';
    this.ownerId = '';
    this.liveSessionId = '';
    this.pricing = null;          // rates handed over by the server at connect
    this.transcript = [];         // [{ speaker, text }]
    this.minutes = null;          // { minutes, pending } once end_call ran
    this.sessionStarted = false;
    this.sessionClosed = false;
    this.transportFailed = false;
    this.cancelledEpoch = 0;
    this.delivered = false;
    this.nextEventId = 0;
    this.startedWaiters = [];
    this.closedWaiters = [];
    this.channelOpenWaiters = [];
    this.minutesWaiters = [];
    this.connectAborts = [];
    // Backend tool rounds, by delegation id: whether its response is still
    // emitting items, which of its function calls still lack an output, and
    // whether any output was sent since the last response.create.
    this.delegations = new Map();
    this.wantsContinue = false;
  }

  setCallbacks({ onState, onResult, onError } = {}) {
    this.callbacks = { onState, onResult, onError };
  }

  state() {
    return {
      phase: this.phase,
      error: this.error,
      questionsUsed: this.questionsUsed,
      maxQuestions: MAX_QUESTIONS,
      pendingAsks: this.pendingAsks,
      micState: this.micState,
      voiceSeconds: this.voiceSeconds,
      startedAt: this.startedAt,
      endedReason: this.endedReason,
      cost: this.cost(),
      active: this.phase === 'connecting' || this.phase === 'live' || this.phase === 'closing',
    };
  }

  // Two prices, and only one of them can be stated. Voice duration is metered
  // by the provider and priced at a published per-minute rate, so it is known.
  // The backend model is billed per token with cache and long-context rates
  // that live in core's pricing table, and the forwarded Responses events do
  // not reliably carry usage: any figure computed here would be a partial
  // subtotal priced at the wrong rate, which is worse than no figure. So the
  // backend is named and excluded, never estimated.
  cost() {
    const rate = Number(this.pricing?.voice_usd_per_minute);
    const floor = Number(this.pricing?.voice_min_billed_seconds) || 0;
    // The floor applies from the moment the session exists upstream, not from
    // the first usage event: those 15 seconds are billed on creation, so a call
    // that reported nothing yet still costs them.
    const billedSeconds = this.liveSessionId ? Math.max(this.voiceSeconds || 0, floor) : 0;
    const voiceUSD = rate > 0 && billedSeconds > 0 ? (billedSeconds / 60) * rate : null;
    return {
      voiceUSD,
      billedSeconds,
      backendModel: this.pricing?.backend_model || '',
      backendCounted: false,
    };
  }

  emit() {
    if (this.disposed) return;
    this.callbacks.onState?.(this.state());
  }

  reportError(message) {
    this.error = message;
    this.callbacks.onError?.(message);
  }

  // --- connect ------------------------------------------------------------

  async start() {
    if (this.phase !== 'idle' && this.phase !== 'ended') return false;
    this.resetCall();
    const epoch = ++this.epoch;
    this.phase = 'connecting';
    this.emit();
    try {
      await this.connect(epoch);
    } catch (error) {
      if (error?.voiceLiveCancelled) {
        // The owner hung up mid-connect. connect() has already closed any
        // session the server managed to create.
        this.finish(this.endedReason || 'cancelled');
        return false;
      }
      const message = error?.voiceLiveMessage || `Could not start the call: ${error?.message || String(error)}`;
      this.reportError(message);
      // A failed connect has nothing to rescue, but it must still leave the
      // controller reusable and the microphone released.
      this.finish('failed');
      return false;
    }
    return true;
  }

  cancelled(epoch) {
    return this.epoch !== epoch || this.cancelledEpoch === epoch;
  }

  // A pending connect step (ICE gathering, session.started) must not keep the
  // hangup waiting for its own timeout.
  abortConnect(error) {
    for (const abort of this.connectAborts.splice(0)) abort(error);
  }

  async connect(epoch) {
    this.stream = await this.getUserMedia({ audio: true });
    if (this.cancelled(epoch)) throw cancelledError(this.endedReason);

    const pc = new this.RTCPeerConnection({});
    this.pc = pc;
    pc.ontrack = (event) => this.attachRemoteAudio(event);
    pc.onconnectionstatechange = () => this.handleConnectionState(pc.connectionState);

    for (const track of this.stream.getTracks?.() || []) {
      pc.addTrack(track, this.stream);
      if (!this.track) this.track = track;
    }
    this.watchMicrophone();

    // The data channel MUST exist before createOffer: it is what puts the
    // application m-line in the SDP we send, and the server's answer is built
    // against that offer. Creating it later would need a renegotiation the
    // live session never performs.
    const channel = pc.createDataChannel('oai-events');
    this.channel = channel;
    channel.onmessage = (event) => this.receive(event.data);
    channel.onopen = () => {
      for (const resolve of this.channelOpenWaiters.splice(0)) resolve();
    };
    channel.onclose = () => {
      // The documented terminal signal: the channel closed without a
      // session.closed, so final usage is unconfirmed and nothing more will
      // ever arrive.
      if (!this.sessionClosed) this.handleTransportFailure('connection-lost');
    };

    const offer = await pc.createOffer();
    if (this.cancelled(epoch)) throw cancelledError(this.endedReason);
    await pc.setLocalDescription(offer);
    if (this.cancelled(epoch)) throw cancelledError(this.endedReason);

    // Rejects on timeout: an incomplete candidate set is not an offer worth
    // spending a (billable) session on.
    await this.waitForIce(pc);
    if (this.cancelled(epoch)) throw cancelledError(this.endedReason);

    const sdp = pc.localDescription?.sdp || offer.sdp;
    const answer = await this.requestSession(sdp);
    // From here on a live session EXISTS server-side and is billable until
    // somebody closes it, whatever happens to this attempt.
    this.ownerId = answer.owner_id || '';
    this.liveSessionId = answer.live_session_id || '';
    this.pricing = answer.pricing || null;
    // The server now has a session to close on our behalf; from here it must
    // keep hearing that somebody is on the call.
    this.startHeartbeat();
    await pc.setRemoteDescription({ type: 'answer', sdp: answer.sdp });

    if (this.cancelled(epoch)) {
      await this.closeOrphanSession();
      throw cancelledError(this.endedReason);
    }

    // Never send session.start: the HTTP request already started the session.
    await this.waitForStarted();
    if (this.cancelled(epoch)) {
      await this.closeOrphanSession();
      throw cancelledError(this.endedReason);
    }

    this.phase = 'live';
    this.startedAt = this.now();
    this.updateMicState();
    this.emit();
  }

  attachRemoteAudio(event) {
    if (!this.Audio) return;
    const remote = event?.streams?.[0];
    if (!remote) return;
    if (!this.audio) {
      this.audio = new this.Audio();
      this.audio.autoplay = true;
    }
    this.audio.srcObject = remote;
    this.audio.play?.()?.catch?.(() => { /* autoplay is best effort */ });
  }

  // A promise that settles on an event, on a timeout, or on an abort (hangup,
  // transport failure). Used for every wait inside connect.
  connectStep({ timeoutMs, onTimeout, register }) {
    return new Promise((resolve, reject) => {
      let settled = false;
      let timer = null;
      const done = (fn, value) => {
        if (settled) return;
        settled = true;
        if (timer !== null) this.clearTimeout(timer);
        const index = this.connectAborts.indexOf(abort);
        if (index >= 0) this.connectAborts.splice(index, 1);
        fn(value);
      };
      const abort = (error) => done(error ? reject : resolve, error);
      this.connectAborts.push(abort);
      if (timeoutMs != null) {
        timer = this.setTimeout(() => {
          const failure = onTimeout?.();
          done(failure ? reject : resolve, failure);
        }, timeoutMs);
      }
      register(() => done(resolve), (error) => done(reject, error));
    });
  }

  waitForIce(pc) {
    if (pc.iceGatheringState === 'complete') return Promise.resolve();
    return this.connectStep({
      timeoutMs: ICE_TIMEOUT_MS,
      onTimeout: () => {
        const error = new Error('timed out while gathering ICE candidates');
        error.voiceLiveMessage = 'Could not reach the voice service: the network never finished negotiating. Try again.';
        return error;
      },
      register: (resolve) => {
        pc.onicegatheringstatechange = () => {
          if (pc.iceGatheringState === 'complete') resolve();
        };
      },
    });
  }

  waitForStarted() {
    if (this.sessionStarted) return Promise.resolve();
    return this.connectStep({
      timeoutMs: SESSION_START_TIMEOUT_MS,
      onTimeout: () => {
        const error = new Error('session.started never arrived');
        error.voiceLiveMessage = 'The call did not connect. Try again.';
        return error;
      },
      register: (resolve) => { this.startedWaiters.push(resolve); },
    });
  }

  waitForChannelOpen() {
    if (!this.channel || this.channel.readyState === 'open' || this.channel.readyState === undefined) {
      return Promise.resolve();
    }
    return new Promise((resolve) => {
      const timer = this.setTimeout(() => {
        const index = this.channelOpenWaiters.indexOf(done);
        if (index >= 0) this.channelOpenWaiters.splice(index, 1);
        resolve();
      }, CHANNEL_OPEN_TIMEOUT_MS);
      const done = () => {
        this.clearTimeout(timer);
        resolve();
      };
      this.channelOpenWaiters.push(done);
    });
  }

  waitForClosedEvent(timeoutMs) {
    if (this.sessionClosed || this.transportFailed) return Promise.resolve();
    return new Promise((resolve) => {
      const timer = this.setTimeout(() => {
        const index = this.closedWaiters.indexOf(done);
        if (index >= 0) this.closedWaiters.splice(index, 1);
        resolve();
      }, timeoutMs);
      const done = () => {
        this.clearTimeout(timer);
        resolve();
      };
      this.closedWaiters.push(done);
    });
  }

  async requestSession(sdp) {
    const response = await this.fetch('/api/voice/live/session', {
      method: 'POST',
      headers: REQUEST_HEADERS,
      body: JSON.stringify({ session_id: this.sessionId, sdp, note: this.note }),
    });
    if (!response.ok) {
      // The body is read for its machine cause only. What the owner sees is
      // our sentence for that cause, never the server's wire format.
      let cause = '';
      try {
        cause = (await response.json?.())?.cause || '';
      } catch { /* an unparseable body just leaves the status to speak */ }
      const error = new Error(`voice live session: HTTP ${response.status}${cause ? ` (${cause})` : ''}`);
      error.voiceLiveMessage = callFailureMessage(response.status, cause);
      throw error;
    }
    return response.json();
  }

  // --- server-side session lifetime ---------------------------------------

  // The heartbeat is what tells the server this call is still being had. It
  // stops on the first 404: a call the server no longer tracks cannot be kept
  // alive, and pinging it forever would be noise.
  startHeartbeat() {
    if (!this.liveSessionId || this.heartbeatTimer !== null) return;
    const beat = async () => {
      this.heartbeatTimer = null;
      if (!this.liveSessionId || this.phase === 'idle' || this.phase === 'ended') return;
      let stop = false;
      try {
        const response = await this.callEndpoint('/api/voice/live/heartbeat');
        stop = response?.status === 404;
      } catch { /* a transient failure is just a missed beat */ }
      if (stop || this.phase === 'idle' || this.phase === 'ended') return;
      schedule();
    };
    const schedule = () => {
      if (this.heartbeatTimer !== null) return;
      this.heartbeatTimer = this.setTimeout(() => { void beat(); }, HEARTBEAT_MS);
    };
    schedule();
  }

  stopHeartbeat() {
    if (this.heartbeatTimer === null) return;
    this.clearTimeout(this.heartbeatTimer);
    this.heartbeatTimer = null;
  }

  // Told to the server as the call ends. The graceful close over the data
  // channel stays the primary path; this is the one that also works when the
  // channel is already dead, and closing twice is not an error.
  async releaseServerSession(liveSessionId) {
    if (!liveSessionId) return;
    try {
      await this.callEndpoint('/api/voice/live/close', liveSessionId);
    } catch { /* the server's sweeper closes what this request could not */ }
  }

  callEndpoint(path, liveSessionId = this.liveSessionId) {
    return this.fetch(path, {
      method: 'POST',
      headers: REQUEST_HEADERS,
      // keepalive so the request survives the page going away, which is one of
      // the cases this whole mechanism exists for.
      keepalive: true,
      body: JSON.stringify({ session_id: this.sessionId, live_session_id: liveSessionId }),
    });
  }

  // The server created a session for an attempt we are abandoning. It bills
  // until it is closed, and session.close only travels over the data channel,
  // so the handshake is finished for the sole purpose of hanging up.
  async closeOrphanSession() {
    if (!this.liveSessionId || this.transportFailed) return;
    await this.waitForChannelOpen();
    this.send({ type: 'session.close' });
    await this.waitForClosedEvent(CLOSE_BACKSTOP_MS);
  }

  // --- events -------------------------------------------------------------

  // Every client command carries an event_id so a later `error` can be matched
  // to the command it rejected (docs: `error.client_event_id`). Without it a
  // rejected mute would be indistinguishable from a session-ending failure.
  send(message) {
    const eventId = `moa_${++this.nextEventId}`;
    try {
      this.channel?.send(JSON.stringify({ event_id: eventId, ...message }));
    } catch { /* a dead channel is handled by the transport-failure path */ }
    return eventId;
  }

  receive(raw) {
    let event;
    try {
      event = typeof raw === 'string' ? JSON.parse(raw) : raw;
    } catch {
      return;
    }
    this.handleEvent(event);
  }

  handleEvent(event, delegationId = '') {
    if (!event || typeof event !== 'object') return;
    // Any event other than the error itself proves the session is still alive.
    if (event.type !== 'error') this.clearSessionErrorWatchdog();
    switch (event.type) {
      case 'session.started':
        // Latched, not just signalled: session.started can arrive while
        // setRemoteDescription is still awaiting, before anybody is listening.
        // A lost start would time out a session that had actually connected.
        this.sessionStarted = true;
        for (const resolve of this.startedWaiters.splice(0)) resolve();
        break;
      // Delegation events arrive wrapped. The inner event is the real one, and
      // an arguments-done event is not a finished call: only
      // response.output_item.done carries call_id + name + arguments.
      case 'response.event':
        this.handleEvent(event.event, event.delegation_id || '');
        break;
      case 'response.created':
        this.delegation(delegationId).open = true;
        break;
      case 'response.completed':
      case 'response.incomplete':
      case 'response.failed': {
        const round = this.delegation(delegationId);
        round.open = false;
        this.maybeContinue(round);
        break;
      }
      case 'response.output_item.done':
        if (event.item?.type === 'function_call') {
          if (event.item.call_id) this.delegation(delegationId).calls.add(event.item.call_id);
          void this.dispatchTool(event.item, delegationId);
        }
        break;
      case 'session.input_transcript.delta':
        this.appendTranscript('owner', event.delta);
        break;
      case 'session.output_transcript.delta':
        this.appendTranscript('delegate', event.delta);
        break;
      case 'session.usage.updated':
        this.voiceSeconds = Number(event.usage?.seconds ?? event.seconds ?? this.voiceSeconds) || this.voiceSeconds;
        this.emit();
        break;
      case 'session.closed':
        this.sessionClosed = true;
        this.voiceSeconds = Number(event.usage?.seconds ?? this.voiceSeconds) || this.voiceSeconds;
        for (const resolve of this.closedWaiters.splice(0)) resolve();
        this.finish(this.minutes ? 'completed' : (this.endedReason || event.reason || 'closed'));
        break;
      case 'error':
        this.handleSessionError(event);
        break;
    }
  }

  handleSessionError(event) {
    const message = event.error?.message || 'The voice session reported an error.';
    this.reportError(message);
    const clientEventId = event.error?.client_event_id || event.client_event_id || '';
    // A rejected command of ours. This data channel has exactly one client, so
    // anything carrying a client event id is a command we sent, and per the
    // docs those do not end the session — an interrupted speech or a refused
    // mute must not hang up a working call. Matching against a remembered set
    // of ids would be worse: an evicted id would read as a session failure,
    // and a false terminal ends a call that was working.
    if (clientEventId) return;
    this.armSessionErrorWatchdog();
  }

  // A session-level error may end the session or may only cut the current
  // speech; the event does not say which. So we wait for evidence instead of
  // guessing: any further event cancels this. If nothing arrives, the call is
  // over in practice, and it must end through the normal path — rescuing the
  // transcript and releasing the microphone — not sit in a panel that still
  // claims to be on a call.
  armSessionErrorWatchdog() {
    if (this.sessionErrorTimer !== null || this.phase !== 'live') return;
    this.sessionErrorTimer = this.setTimeout(() => {
      this.sessionErrorTimer = null;
      if (this.phase !== 'live') return;
      void this.close('session-error');
    }, SESSION_ERROR_GRACE_MS);
  }

  clearSessionErrorWatchdog() {
    if (this.sessionErrorTimer === null) return;
    this.clearTimeout(this.sessionErrorTimer);
    this.sessionErrorTimer = null;
  }

  handleConnectionState(state) {
    if (state === 'connected') {
      if (this.disconnectTimer !== null) {
        this.clearTimeout(this.disconnectTimer);
        this.disconnectTimer = null;
      }
      return;
    }
    if (state === 'failed' || state === 'closed') {
      this.handleTransportFailure('connection-lost');
      return;
    }
    if (state !== 'disconnected' || this.disconnectTimer !== null) return;
    // "disconnected" is recoverable: ICE can restore it. Give it a bounded
    // grace before calling the call lost.
    this.disconnectTimer = this.setTimeout(() => {
      this.disconnectTimer = null;
      if (this.pc?.connectionState === 'disconnected') this.handleTransportFailure('connection-lost');
    }, DISCONNECT_GRACE_MS);
  }

  // A proven transport failure is not a graceful close: nothing can be sent
  // and nothing more will arrive, so final usage stays unconfirmed and we stop
  // waiting for events that cannot come.
  handleTransportFailure(reason) {
    if (this.transportFailed || this.phase === 'ended' || this.phase === 'idle') return;
    this.transportFailed = true;
    for (const resolve of this.closedWaiters.splice(0)) resolve();
    for (const resolve of this.channelOpenWaiters.splice(0)) resolve();
    if (this.phase === 'connecting') {
      const error = new Error('the connection failed before the call started');
      error.voiceLiveMessage = 'The call lost its connection before it started.';
      this.abortConnect(error);
      return;
    }
    this.finish(this.minutes ? 'completed' : reason);
  }

  appendTranscript(speaker, delta) {
    const text = String(delta ?? '');
    if (!text) return;
    const last = this.transcript[this.transcript.length - 1];
    if (last && last.speaker === speaker) last.text += text;
    else this.transcript.push({ speaker, text });
  }

  // --- tools --------------------------------------------------------------

  async dispatchTool(item, delegationId = '') {
    const callId = item.call_id;
    const respond = (output) => this.respondTool(callId, output, delegationId);
    let args = {};
    try {
      args = item.arguments ? JSON.parse(item.arguments) : {};
    } catch {
      respond({ error: 'arguments were not valid JSON' });
      return;
    }
    switch (item.name) {
      case 'book_list':
        respond(await this.bookList());
        break;
      case 'book_read':
        respond(await this.bookRead(args.path));
        break;
      case 'ask_session':
        respond(await this.askSession(args.question));
        break;
      case 'end_call':
        respond({ status: 'ok', message: 'Minutes received. Closing the call.' });
        await this.endCall(args);
        break;
      default:
        respond({ error: `unknown tool ${item.name}` });
    }
  }

  delegation(id) {
    let round = this.delegations.get(id);
    if (!round) {
      round = { open: false, calls: new Set(), answered: false };
      this.delegations.set(id, round);
    }
    return round;
  }

  respondTool(callId, output, delegationId = '') {
    if (!callId) return;
    this.send({
      type: 'response.item.create',
      item: { type: 'function_call_output', call_id: callId, output: JSON.stringify(output) },
    });
    const round = this.delegation(delegationId);
    round.calls.delete(callId);
    round.answered = true;
    this.maybeContinue(round);
  }

  // The backend response is suspended on its function calls, and
  // response.create is what resumes it — but only once EVERY call of that
  // response has its output. The backend runs tools in parallel, so resuming
  // after the first output is rejected (`function_call_outputs_required`,
  // "Missing function call outputs for: call_…"), which is what reached the
  // owner as an error toast. A response still open may emit more calls, so
  // the round waits for its terminal event too.
  // response.create names no response, so it also waits for every other round
  // to settle: the hang-up's request for minutes must not resume a response
  // that still owes tool outputs.
  maybeContinue(round = null) {
    if (round && !round.open && round.calls.size === 0 && round.answered) {
      round.answered = false;
      this.wantsContinue = true;
    }
    if (!this.wantsContinue || this.backendBusy()) return;
    this.wantsContinue = false;
    this.send({ type: 'response.create' });
  }

  backendBusy() {
    for (const round of this.delegations.values()) {
      if (round.open || round.calls.size > 0) return true;
    }
    return false;
  }

  appendThinking(content) {
    this.send({
      type: 'session.thinking.append',
      delegation_id: null,
      content: truncateForTokens(content),
    });
  }

  async ownerBookFetch(path) {
    if (!this.ownerId) {
      return { error: 'The book is unavailable: this conversation has no owner. Do not guess its contents.' };
    }
    const response = await this.fetch(path, { headers: { 'X-Moa-Request': '1' } });
    if (!response.ok) {
      const detail = (await response.text?.())?.trim?.() || `HTTP ${response.status}`;
      return { error: `Could not read the book: ${detail}` };
    }
    return { data: await response.json() };
  }

  async bookList() {
    const result = await this.ownerBookFetch(`/api/owners/${encodeURIComponent(this.ownerId)}/book`);
    if (result.error) return { status: 'unavailable', message: result.error };
    const files = (result.data?.files || []).map((entry) => entry.path);
    return { status: 'ok', files };
  }

  async bookRead(rawPath) {
    const path = String(rawPath || '').trim();
    if (!path) return { status: 'error', message: 'book_read needs a path.' };
    const encoded = path.split('/').map(encodeURIComponent).join('/');
    const result = await this.ownerBookFetch(`/api/owners/${encodeURIComponent(this.ownerId)}/book/${encoded}`);
    if (result.error) return { status: 'unavailable', message: result.error };
    return { status: 'ok', path, content: result.data?.content || '' };
  }

  async askSession(rawQuestion) {
    const question = String(rawQuestion || '').trim();
    if (!question) return { status: 'error', message: 'ask_session needs a question.' };
    // The cap lives here, not in the prompt: a limit the model can talk itself
    // out of is not a limit.
    if (this.questionsUsed >= MAX_QUESTIONS) {
      return {
        status: 'limit_reached',
        message: `The limit of ${MAX_QUESTIONS} questions for this call is reached. Do not ask again: leave the point as pending confirmation in the minutes.`,
      };
    }
    // Reserve the slot synchronously, BEFORE the first await. Tool calls are
    // dispatched concurrently, so two asks that both read the counter before
    // either POSTs would otherwise spend one slot between them and the cap
    // would be advisory.
    this.questionsUsed += 1;
    this.pendingAsks += 1;
    this.emit();
    let askId = '';
    try {
      const response = await this.fetch('/api/voice/live/ask', {
        method: 'POST',
        headers: REQUEST_HEADERS,
        // The Live session id groups every question of THIS call under one
        // transcript block, instead of scattering loose messages through the
        // conversation's history.
        body: JSON.stringify({ session_id: this.sessionId, question, call_id: this.liveSessionId }),
      });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      askId = (await response.json())?.ask_id || '';
    } catch (error) {
      // The question never reached the session, so it never cost a slot.
      this.questionsUsed -= 1;
      this.pendingAsks -= 1;
      this.emit();
      return { status: 'error', message: `The question could not be delivered: ${error?.message || String(error)}` };
    }
    // Poll out of band: the function output must come back now, so the
    // delegate can keep the conversation alive instead of going silent.
    this.pollAnswer(askId, question);
    return {
      status: 'asked',
      message: 'The question was sent to the session. The answer will arrive in your context shortly; keep talking in the meantime and do not wait in silence.',
      questions_left: MAX_QUESTIONS - this.questionsUsed,
    };
  }

  pollAnswer(askId, question) {
    const deadline = this.now() + ASK_TIMEOUT_MS;
    const settle = (content) => {
      this.pendingAsks = Math.max(0, this.pendingAsks - 1);
      this.emit();
      if (this.phase === 'live') this.appendThinking(content);
    };
    const tick = async () => {
      if (this.disposed || this.phase === 'ended') return;
      let status = 'pending';
      let answer = '';
      try {
        const url = `/api/voice/live/ask?session_id=${encodeURIComponent(this.sessionId)}&ask_id=${encodeURIComponent(askId)}`;
        const response = await this.fetch(url, { headers: { 'X-Moa-Request': '1' } });
        if (response.ok) {
          const body = await response.json();
          status = body?.status || 'pending';
          answer = body?.answer || '';
        }
      } catch { /* a transient failure is just another pending tick */ }
      if (this.disposed || this.phase === 'ended') return;
      if (status === 'answered') {
        settle(`Answer from the session to "${truncate(question, 200)}": ${answer}`);
        return;
      }
      if (status === 'failed') {
        settle(`The session could not answer "${truncate(question, 200)}". Leave that point as pending confirmation.`);
        return;
      }
      if (this.now() >= deadline) {
        settle(`No answer arrived in time for "${truncate(question, 200)}". The owner did not answer; leave that point as pending confirmation instead of assuming an answer.`);
        return;
      }
      schedule();
    };
    const schedule = () => {
      const timer = this.setTimeout(() => {
        this.askTimers.delete(timer);
        void tick();
      }, ASK_POLL_MS);
      this.askTimers.add(timer);
    };
    schedule();
  }

  async endCall(args) {
    const minutes = { minutes: String(args?.minutes || '').trim() };
    // Only minutes that actually say something count as minutes. An empty
    // end_call must never suppress the transcript rescue: that would throw
    // away the entire call on the delegate's last mistake.
    if (!this.minutes && formatMinutes(minutes).trim()) {
      this.minutes = minutes;
      // A hangup may be waiting for exactly this.
      for (const resolve of this.minutesWaiters.splice(0)) resolve();
    }
    await this.close(this.minutes ? 'completed' : 'no-minutes');
  }

  // --- microphone truth ---------------------------------------------------

  watchMicrophone() {
    const track = this.track;
    if (track) {
      track.onmute = () => this.updateMicState();
      track.onunmute = () => this.updateMicState();
      track.onended = () => this.updateMicState();
    }
    if (this.documentRef?.addEventListener) {
      this.onVisibility = () => this.handleVisibility();
      this.documentRef.addEventListener('visibilitychange', this.onVisibility);
    }
    this.updateMicState();
  }

  handleVisibility() {
    const hidden = !!this.documentRef?.hidden;
    // A hidden page cannot prove the microphone is capturing, and a call that
    // keeps listening from the background is worse than one that pauses: mute
    // explicitly, so what the panel says and what the session hears agree.
    this.send({ type: hidden ? 'session.input_audio.mute' : 'session.input_audio.unmute' });
    this.updateMicState();
  }

  // Never claims "live" on the strength of absent events: hidden is unknown,
  // a muted/ended track is not live, and only a live track on a visible page
  // is reported as listening.
  computeMicState() {
    if (this.documentRef?.hidden) return MIC_UNKNOWN;
    const track = this.track;
    if (!track) return MIC_NOT_LIVE;
    if (track.readyState && track.readyState !== 'live') return MIC_NOT_LIVE;
    if (track.muted || track.enabled === false) return MIC_NOT_LIVE;
    return MIC_LIVE;
  }

  updateMicState() {
    const next = this.computeMicState();
    const changed = next !== this.micState;
    this.micState = next;
    if (next === MIC_LIVE) this.clearMicGrace();
    else if (this.phase === 'live') this.armMicGrace();
    if (changed) this.emit();
  }

  armMicGrace() {
    if (this.micGraceTimer !== null) return;
    this.micGraceTimer = this.setTimeout(() => {
      this.micGraceTimer = null;
      if (this.phase !== 'live' || this.computeMicState() === MIC_LIVE) return;
      void this.close('mic-lost');
    }, MIC_GRACE_MS);
  }

  clearMicGrace() {
    if (this.micGraceTimer === null) return;
    this.clearTimeout(this.micGraceTimer);
    this.micGraceTimer = null;
  }

  // --- ending -------------------------------------------------------------

  // hangup is the owner's button. It never tears anything down by itself:
  // session.closed does, so the final usage and any late transcript delta are
  // still received.
  hangup() {
    return this.close('hangup');
  }

  whenEnded() {
    if (this.phase === 'ended' || this.phase === 'idle') return Promise.resolve();
    return new Promise((resolve) => this.endWaiters.push(resolve));
  }

  close(reason) {
    if (this.phase === 'ended' || this.phase === 'idle') return Promise.resolve();
    if (this.phase === 'closing') return this.whenEnded();
    if (this.phase === 'connecting') {
      // Cancel the start in flight. connect() checks this after every await
      // and, if the server already created a session, closes it before giving
      // up: an orphan live session keeps billing with nobody listening.
      this.cancelledEpoch = this.epoch;
      this.phase = 'closing';
      this.endedReason = reason;
      this.emit();
      this.abortConnect(cancelledError(reason));
      return this.whenEnded();
    }
    this.phase = 'closing';
    this.endedReason = reason;
    this.emit();
    if (this.transportFailed || !this.channelUsable()) {
      // Not a graceful close: the transport is proven dead, so session.closed
      // can never arrive and waiting for it would only delay the rescue.
      this.finish(reason);
      return Promise.resolve();
    }
    if (reason === 'hangup' && !this.minutes) {
      // The owner hung up first. Which of the two ends the call must not
      // decide whether there are minutes at all.
      void this.closeAfterMinutes(reason);
      return this.whenEnded();
    }
    this.sendClose(reason);
    return this.whenEnded();
  }

  sendClose(reason) {
    this.send({ type: 'session.close' });
    this.closeTimer = this.setTimeout(() => {
      this.closeTimer = null;
      // Incomplete finalization (documented case): usage stays unconfirmed,
      // but the microphone cannot be held hostage by a silent session.
      this.finish(reason);
    }, CLOSE_BACKSTOP_MS);
  }

  // Asks the backend for the minutes now, waits a bounded moment, then closes
  // either way. The request goes to the backend directly, as a queued message
  // plus response.create: asking the voice model to delegate it instead (an
  // instructions append) was measured against the live service and the voice
  // model never delegated in 42s, while the direct request had end_call back
  // in about 2s. The voice model may still say something as it happens, and
  // the owner has already left the conversation, so its audio is muted.
  async closeAfterMinutes(reason) {
    if (this.audio) this.audio.muted = true;
    this.send({
      type: 'response.item.create',
      item: {
        type: 'message',
        role: 'user',
        content: [{
          type: 'input_text',
          text: 'The owner has just hung up. Call end_call now with the minutes of this call, written in the language of the call.',
        }],
      },
    });
    this.wantsContinue = true;
    this.maybeContinue();
    await this.waitForMinutes();
    if (this.phase === 'ended' || this.phase === 'idle') return;
    if (this.minutes) {
      this.endedReason = 'completed';
      this.emit();
    }
    const ending = this.minutes ? 'completed' : reason;
    if (this.transportFailed || !this.channelUsable()) {
      this.finish(ending);
      return;
    }
    this.sendClose(ending);
  }

  waitForMinutes() {
    if (this.minutes) return Promise.resolve();
    return new Promise((resolve) => {
      const done = () => {
        if (this.minutesTimer !== null) {
          this.clearTimeout(this.minutesTimer);
          this.minutesTimer = null;
        }
        resolve();
      };
      this.minutesTimer = this.setTimeout(() => {
        this.minutesTimer = null;
        const index = this.minutesWaiters.indexOf(done);
        if (index >= 0) this.minutesWaiters.splice(index, 1);
        resolve();
      }, MINUTES_ON_HANGUP_MS);
      this.minutesWaiters.push(done);
    });
  }

  channelUsable() {
    const state = this.channel?.readyState;
    if (!this.channel) return false;
    return state === undefined || state === 'open' || state === 'connecting';
  }

  // finish is the single exit: every ending (hangup, error, lost connection,
  // lost microphone, a cancelled start) goes through it, and it always
  // delivers something. Minutes when the delegate produced them, the raw
  // transcript otherwise — nothing said is ever lost.
  finish(reason) {
    if (this.delivered) return;
    this.delivered = true;
    this.endedReason = reason;
    this.phase = 'ended';
    const payload = this.result(reason);
    const meta = {
      reason,
      minutes: !!this.minutes,
      voiceSeconds: this.voiceSeconds,
      cost: this.cost(),
      // Only session.closed confirms the final usage. Anything else is the
      // last figure we happened to see.
      usageConfirmed: this.sessionClosed,
    };
    const callbacks = this.callbacks;
    const liveSessionId = this.liveSessionId;
    this.teardown();
    this.emit();
    for (const resolve of this.endWaiters.splice(0)) resolve();
    // Last: the server is told the call is over, so it stops holding a
    // session that nothing is using.
    void this.releaseServerSession(liveSessionId);
    if (payload) callbacks.onResult?.(payload, meta);
  }

  result(reason) {
    if (this.minutes) return formatMinutes(this.minutes);
    const body = renderTranscript(this.transcript).trim();
    if (!body) return '';
    const why = reason === 'mic-lost' ? 'the microphone stopped being available'
      : reason === 'connection-lost' ? 'the connection was lost'
        : reason === 'session-error' ? 'the voice session failed'
          : reason === 'no-minutes' ? 'the delegate ended the call without writing any'
            : 'it was interrupted';
    return `The voice call ended before the delegate wrote any minutes (${why}). This is the raw transcript, in order:\n\n${body}`;
  }

  teardown() {
    this.clearMicGrace();
    this.clearSessionErrorWatchdog();
    this.stopHeartbeat();
    for (const timer of this.askTimers) this.clearTimeout(timer);
    this.askTimers.clear();
    for (const timer of [this.closeTimer, this.disconnectTimer, this.minutesTimer]) {
      if (timer !== null) this.clearTimeout(timer);
    }
    this.closeTimer = null;
    this.disconnectTimer = null;
    this.minutesTimer = null;
    if (this.onVisibility && this.documentRef?.removeEventListener) {
      this.documentRef.removeEventListener('visibilitychange', this.onVisibility);
    }
    this.onVisibility = null;
    this.startedWaiters = [];
    this.closedWaiters = [];
    this.channelOpenWaiters = [];
    for (const resolve of this.minutesWaiters.splice(0)) resolve();
    this.connectAborts = [];
    for (const track of this.stream?.getTracks?.() || []) track.stop?.();
    this.stream = null;
    this.track = null;
    if (this.channel) this.channel.onclose = null;
    try { this.channel?.close?.(); } catch { /* already gone */ }
    this.channel = null;
    try { this.pc?.close?.(); } catch { /* already gone */ }
    this.pc = null;
    if (this.audio) {
      this.audio.pause?.();
      this.audio.srcObject = null;
      this.audio = null;
    }
    this.pendingAsks = 0;
    this.micState = MIC_UNKNOWN;
  }

  dispose() {
    if (this.disposed) return;
    // Nothing can be delivered into an unmounted component, and no state may
    // be pushed into it — but a live session must still be closed properly:
    // it bills until it is, so the graceful close runs to completion here
    // instead of being cut short by an immediate teardown.
    this.disposed = true;
    this.callbacks = {};
    void this.close('disposed');
  }
}
