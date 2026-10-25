// voice-call.js — one voice call with a session, through moa only.
//
// POST /api/voice/calls opens the call on the server, which talks to the
// provider; this browser only ever opens moa's own WebSocket for it, sends
// binary PCM (s16le mono 24 kHz) plus the mute/hangup/ping controls, and
// plays the PCM moa sends back. The same path serves every provider. Paid
// providers are only ever named by an explicit user action (start({provider})).
//
// Nothing a call produces is written into the composer: the minutes are a
// record the server saves in the conversation. onMinutes/onResult are not
// part of this API and are never called.

import { voiceStateDiagnostics } from './voice-state-diagnostics.js';

const REQUEST_HEADERS = { 'Content-Type': 'application/json', 'X-Moa-Request': '1' };

// Captured audio waiting in the socket beyond this is dropped instead of
// queued: one second, the server's own bound for each direction.
export const MAX_SOCKET_BUFFERED = 48000;
export const MAX_ASKS = 5;

const ACTIVE = new Set(['starting', 'connecting', 'listening', 'speaking', 'consulting', 'ending']);

export function billingLabel(billing, provider = '') {
  if (!billing) return '';
  if (billing.kind === 'per_minute' && typeof billing.usd_per_minute === 'number') {
    return `$${billing.usd_per_minute.toFixed(2)}/min`;
  }
  // Never "included" or "free": whether the plan covers a call is not verified.
  if (billing.kind === 'plan_unverified') return `Uses your ${PROVIDER_NAMES[provider] || 'plan'}`;
  return '';
}

export const PROVIDER_NAMES = {
  'chatgpt-plan': 'ChatGPT plan',
  'grok-plan': 'Grok plan',
  'xai-api': 'Grok API',
  'openai-live-api': 'OpenAI API',
};

// What the user reads when a call does not start or stops. Each line says
// what to do next; none is a server body.
const START_COPY = {
  no_plan: 'Connect a ChatGPT or Grok plan in Settings → Providers to call.',
  credentials: 'Sign in to your plan again in Settings → Providers, then call.',
  unauthorized: 'Your plan sign-in was rejected. Sign in again in Settings → Providers.',
  forbidden: 'Your plan does not include voice right now. Try again later.',
  rate_limited: 'Too many calls just now. Wait a few seconds and call again.',
  unavailable: 'The voice service did not answer. Call again in a moment.',
  timeout: 'The voice service took too long to answer. Call again.',
  closing: 'moa is restarting. Call again in a moment.',
  call_active: 'You are already on a call. Hang up first.',
  session_not_found: 'This conversation is no longer open. Reopen it and call again.',
  shutting_down: 'moa is restarting. Call again in a moment.',
  unauthenticated: 'Sign in to moa again, then call.',
  request_not_live: 'That call is no longer waiting. Its question is in the conversation.',
  unknown_provider: 'That voice provider is not available. Pick another in Settings → Calls.',
  microphone: 'Allow the microphone for moa, then call again.',
  network: 'Could not reach moa. Check the connection and call again.',
};

export function startFailureMessage(error, reason) {
  if (error === 'plan_unavailable' || error === 'provider_unavailable') {
    return START_COPY[reason] || START_COPY.unavailable;
  }
  return START_COPY[error] || START_COPY[reason] || 'The call could not start. Try again.';
}

const ENDED_COPY = {
  hangup: '',
  end_call: '',
  client_lost: 'The call ended: this device lost the connection.',
  max_duration: 'The call reached its time limit.',
  upstream_closed: 'The voice service ended the call.',
  upstream_failed: 'The voice service dropped the call.',
  slow_client: 'The call ended: the connection was too slow.',
  audio_backpressure: 'The call ended: the connection was too slow.',
  device_revoked: 'This device was signed out, so the call ended.',
  shutdown: 'moa restarted, so the call ended.',
  protocol_violation: 'The call ended unexpectedly. Call again.',
  transcript_limit: 'The call reached its length limit.',
};

export function endedMessage(reason) {
  if (reason in ENDED_COPY) return ENDED_COPY[reason];
  return reason ? 'The call ended.' : '';
}

function initialState(sessionId) {
  return {
    phase: 'idle',
    active: false,
    sessionId,
    callId: '',
    provider: '',
    billing: null,
    billingLabel: '',
    paidOption: null,
    callRequestId: '',
    error: '',
    reason: '',
    message: '',
    muted: false,
    startedAt: 0,
    endedReason: '',
    asks: 0,
    maxAsks: MAX_ASKS,
    needsTap: null,
    passed: [],
    droppedFrames: 0,
  };
}

export class VoiceCallController {
  // audio is one engine, or createAudio builds a fresh one for every call
  // (a browser engine releases everything it acquired when it stops).
  // fallback ({WebSocket, createAudio}) is the page's own transport, used
  // when another transport (the iOS app's) drops before it ever had audio.
  constructor({ sessionId, fetch: fetchImpl, WebSocket: WebSocketImpl, audio, createAudio, onState, now, fallback, onFallback } = {}) {
    this.sessionId = sessionId;
    this.fetch = fetchImpl || ((...args) => globalThis.fetch(...args));
    this.WebSocket = WebSocketImpl || globalThis.WebSocket;
    this.audio = audio;
    this.createAudio = createAudio || null;
    this.onState = onState || (() => {});
    this.fallback = fallback || null;
    this.onFallback = onFallback || (() => {});
    this.now = now || (() => Date.now());
    this.s = initialState(sessionId);
    this.ws = null;
    // A new generation invalidates every callback of the previous attempt:
    // late capture frames, late socket messages, a slow hangup.
    this.gen = 0;
  }

  state() {
    return this.s;
  }

  set(patch, cause = 7) {
    if (patch.phase && patch.phase !== this.s.phase) this.diagnostics?.event(20, cause, this.s.phase, patch.phase);
    this.s = { ...this.s, ...patch };
    this.s.active = ACTIVE.has(this.s.phase);
    this.onState(this.s);
  }

  // start opens a call. Without provider the server picks the plan; a paid
  // provider is only ever passed here by the user's explicit choice.
  async start({ provider, callRequestId } = {}) {
    if (this.s.active) return;
    const gen = ++this.gen;
    if (this.createAudio) this.audio = this.createAudio();
    this.audio?.prepare?.();
    this.set({
      ...initialState(this.sessionId),
      phase: 'starting',
      provider: provider || '',
      callRequestId: callRequestId || '',
    });
    const body = { session_id: this.sessionId };
    if (provider) body.provider = provider;
    if (callRequestId) body.call_request_id = callRequestId;
    let res;
    let data = null;
    try {
      res = await this.fetch('/api/voice/calls', { method: 'POST', headers: REQUEST_HEADERS, body: JSON.stringify(body) });
      data = await res.json().catch(() => null);
    } catch {
      if (gen === this.gen) this.fail('network', '', null);
      return;
    }
    if (gen !== this.gen) return;
    if (res.status !== 201 || !data?.call_id) {
      const error = typeof data?.error === 'string' ? data.error : `http_${res.status}`;
      const reason = typeof data?.reason === 'string' ? data.reason : '';
      // Only a plan failure may offer the paid alternative, and only one the
      // server named as usable; it is never taken from here.
      // An owner's request never offers it: it already became a written question.
      const offer = !callRequestId && error === 'plan_unavailable' && typeof data?.paid_option?.provider === 'string'
        && data.paid_option.provider.endsWith('-api')
        ? { provider: data.paid_option.provider, billing: data.paid_option.billing || null }
        : null;
      this.fail(error, reason, offer);
      return;
    }
    this.set({
      phase: 'connecting',
      callId: data.call_id,
      provider: data.provider || provider || '',
      billing: data.billing || null,
      billingLabel: billingLabel(data.billing, data.provider || provider),
    });
    await this.connect(gen, data);
  }

  // adopt mirrors a call that already runs elsewhere on this device (the
  // iOS app kept it through a reload): no POST, the socket attaches to it.
  async adopt({ callId, muted = false } = {}) {
    if (this.s.active || !callId) return;
    const gen = ++this.gen;
    if (this.createAudio) this.audio = this.createAudio();
    this.set({ ...initialState(this.sessionId), phase: 'connecting', callId, muted: !!muted, startedAt: this.now() });
    await this.connect(gen, { call_id: callId });
    if (gen === this.gen && this.s.phase === 'connecting') this.set({ phase: 'listening' });
  }

  fail(error, reason, paidOption) {
    this.audio?.stop?.();
    this.set({ phase: 'failed', error, reason, message: startFailureMessage(error, reason), paidOption });
    this.diagnostics?.stop(); this.diagnostics = null;
  }

  async connect(gen, { call_id: callId, ws_url: wsURL }) {
    this.diagnostics?.stop(); this.diagnostics = null;
    const loc = globalThis.location;
    const path = typeof wsURL === 'string' && wsURL.startsWith('/') ? wsURL : `/api/voice/calls/${encodeURIComponent(callId)}/ws`;
    const url = `${loc?.protocol === 'http:' ? 'ws:' : 'wss:'}//${loc?.host}${path}`;
    this.audioReady = false;
    this.heldPhase = '';
    const ws = new this.WebSocket(url);
    ws.binaryType = 'arraybuffer';
    this.ws = ws;
    const opened = new Promise((resolve) => {
      ws.onopen = () => resolve(true);
      ws.onclose = (ev) => {
        resolve(false);
        if (gen !== this.gen) return;
        if (this.fallback && !this.audioReady && this.s.phase !== 'ending') {
          this.useFallback(callId, wsURL, ev?.reason || 'closed_before_audio');
          return;
        }
        this.finish(this.s.phase === 'ending' ? 'hangup' : 'client_lost');
      };
    });
    ws.onerror = () => {};
    ws.onmessage = (event) => {
      if (gen === this.gen && this.ws === ws) this.receive(event.data);
    };
    if (!(await opened) || gen !== this.gen) return;
    try {
      await this.audio.start((pcm) => this.capture(gen, ws, pcm), {
        onPlayback: (e) => { if (gen === this.gen) this.playback(e); },
      });
    } catch (err) {
      // A hangup while the microphone was opening is not a microphone error.
      if (gen !== this.gen || this.s.phase === 'ending') return;
      // The app's own audio failed (not the user's permission): the page
      // takes the call over, whether or not the app's close has arrived yet.
      if (this.fallback && err?.message === 'audio_failed') {
        this.useFallback(callId, wsURL, 'audio_failed');
        return;
      }
      // No microphone: end the call on the server too, so nothing keeps
      // running for a call that cannot hear.
      this.sendJSON({ type: 'hangup' });
      this.finish('microphone');
      this.set({ message: START_COPY.microphone });
      return;
    }
    if (gen !== this.gen) return;
    this.audioReady = true;
    // What the server said while the microphone was being granted is shown
    // now, when the call can actually be heard.
    if (this.heldPhase && this.s.phase === 'connecting') this.set({ phase: this.heldPhase }, 6);
    this.heldPhase = '';
    if (this.s.muted) this.audio.setMuted?.(true);
  }

  // useFallback reattaches the same call over the page's own socket and
  // audio; the server keeps a call open for a few seconds without a client.
  useFallback(callId, wsURL, note) {
    const { WebSocket: WebSocketImpl, createAudio } = this.fallback;
    this.fallback = null;
    const gen = ++this.gen;
    this.audio?.stop?.();
    this.WebSocket = WebSocketImpl;
    this.createAudio = createAudio;
    this.audio = createAudio();
    this.audio.prepare?.();
    this.onFallback(note);
    const path = typeof wsURL === 'string' && wsURL.startsWith('/') ? wsURL : `/api/voice/calls/${encodeURIComponent(callId)}/ws`;
    const sep = path.includes('?') ? '&' : '?';
    this.connect(gen, { call_id: callId, ws_url: `${path}${sep}client_note=${encodeURIComponent(`native:${note}`.slice(0, 120))}` });
  }

  capture(gen, ws, pcm) {
    if (gen !== this.gen || this.ws !== ws || this.s.muted || this.s.phase === 'ending') return;
    if (ws.readyState !== 1) return;
    // A stalled socket gets fresh audio, never an ever-growing backlog.
    if ((ws.bufferedAmount || 0) > MAX_SOCKET_BUFFERED) {
      this.s.droppedFrames++;
      return;
    }
    ws.send(pcm);
  }

  playback(event) {
    const type = event?.type;
    const cause = this.diagnosticInterrupt ? 4 : type === 'playing' ? 1 : 2;
    if (type === 'playing' || type === 'drained') this.diagnostics?.event(22, cause, this.s.phase, this.s.phase);
    if (type === 'playing' && this.s.phase === 'listening') this.set({ phase: 'speaking' }, cause);
    else if (type === 'drained' && this.s.phase === 'speaking') this.set({ phase: 'listening' }, cause);
  }

  receive(data) {
    if (data instanceof ArrayBuffer || ArrayBuffer.isView(data)) {
      if (!ACTIVE.has(this.s.phase) || this.s.phase === 'ending') return;
      this.audio.play(data);
      return;
    }
    let msg;
    try {
      msg = JSON.parse(data);
    } catch {
      return;
    }
    switch (msg?.type) {
      case 'state': {
        if (!this.diagnostics) this.diagnostics = voiceStateDiagnostics(msg.audio_diagnostics, this.ws, this.gen);
        const cause = msg.interrupt ? 4 : 3;
        this.diagnostics?.event(21, cause, this.s.phase, msg.state);
        if (msg.interrupt) {
          this.diagnosticInterrupt = true;
          try { this.audio.interrupt(); } finally { this.diagnosticInterrupt = false; }
        }
        if (this.s.phase === 'ending') return;
        const patch = {};
        if (!this.s.startedAt) patch.startedAt = this.now();
        if (msg.state === 'consulting') {
          patch.phase = 'consulting';
          patch.asks = Math.min(MAX_ASKS, this.s.asks + 1);
        } else if (msg.state === 'listening' || msg.state === 'speaking') {
          if (this.audioReady === false) this.heldPhase = msg.state;
          else patch.phase = msg.state;
        }
        this.set(patch, cause);
        return;
      }
      case 'speech_started':
        this.diagnostics?.event(21, 5, this.s.phase, 'listening');
        this.audio.interrupt();
        if (this.s.phase === 'speaking') this.set({ phase: 'listening' }, 5);
        return;
      case 'needs_tap':
        if (typeof msg.item_id === 'string') {
          this.set({ needsTap: { itemId: msg.item_id, kind: msg.kind === 'permission' ? 'permission' : 'ask' } });
        }
        return;
      case 'passed':
        if (typeof msg.delivery_id === 'string' && !this.s.passed.some((p) => p.deliveryId === msg.delivery_id)) {
          this.set({ passed: [...this.s.passed, { deliveryId: msg.delivery_id, summary: String(msg.summary || ''), queued: !!msg.queued }] });
        }
        return;
      case 'native_mute':
        // The app's own call was muted from the system call screen.
        if (this.s.active) this.set({ muted: !!msg.muted });
        return;
      case 'ended':
        this.finish(typeof msg.reason === 'string' ? msg.reason : '');
        return;
      default:
    }
  }

  sendJSON(v) {
    const ws = this.ws;
    if (ws && ws.readyState === 1) {
      try { ws.send(JSON.stringify(v)); } catch { /* closing */ }
    }
  }

  setMuted(muted) {
    if (!this.s.active) return;
    this.audio.setMuted?.(muted);
    this.sendJSON({ type: 'mute', muted: !!muted });
    this.set({ muted: !!muted });
  }

  // hangup asks the server to end the call and waits until it has: the
  // server answers once the record of the call is saved. The microphone and
  // playback stop at once.
  async hangup() {
    if (!this.s.active) return;
    const { callId } = this.s;
    this.audio.stop();
    if (this.s.phase === 'starting' || !callId) {
      // Nothing exists on the server yet; the pending start is abandoned.
      this.gen++;
      this.set({ phase: 'ended', endedReason: 'hangup' });
      return;
    }
    this.set({ phase: 'ending' });
    try {
      const res = await this.fetch(`/api/voice/calls/${encodeURIComponent(callId)}/hangup`, { method: 'POST', headers: REQUEST_HEADERS });
      if (!res.ok && res.status !== 404) throw new Error(`hangup ${res.status}`);
    } catch {
      this.sendJSON({ type: 'hangup' });
    }
    if (this.s.phase !== 'ended' && this.s.callId === callId) this.finish('hangup');
  }

  finish(reason) {
    if (this.s.phase === 'ended' || this.s.phase === 'idle' || this.s.phase === 'failed') return;
    this.gen++;
    this.audio?.stop?.();
    const ws = this.ws;
    this.ws = null;
    if (ws) {
      ws.onmessage = null;
      ws.onclose = null;
      try { ws.close(); } catch { /* gone */ }
    }
    this.set({ phase: 'ended', endedReason: reason, message: endedMessage(reason), needsTap: null });
    this.diagnostics?.stop(); this.diagnostics = null;
  }

  // dispose ends a call whose screen is going away for good.
  dispose() {
    if (this.s.active) this.hangup();
  }
}
