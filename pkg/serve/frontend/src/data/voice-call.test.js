import { test, expect } from 'bun:test';

// Keep the missing production import inside each test: all three scenarios must
// be enumerated even while the new controller entry point is absent.
const ORIGIN = 'https://moa.test';
const PLAN = { kind: 'plan_unverified' };
const PAID_OPTIONS = [
  { provider: 'xai-api', billing: { kind: 'per_minute', usd_per_minute: 0.08 }, label: '$0.08/min' },
  { provider: 'openai-live-api', billing: { kind: 'per_minute', usd_per_minute: 0.05 }, label: '$0.05/min' },
];

async function flush(times = 12) {
  for (let i = 0; i < times; i++) await Promise.resolve();
}

function created(provider, billing = PLAN) {
  return {
    status: 201,
    body: { call_id: 'call-1', provider, billing, ws_url: '/api/voice/calls/call-1/ws' },
  };
}

// These doubles only record browser I/O and deliver server frames. In
// particular, none selects providers, interprets billing, or handles minutes.
function setup(VoiceCallController, { responses, sessionId = 'sess-1', ...callbacks }) {
  const requests = [];
  const sockets = [];
  const states = [];
  const navigations = [];
  const peerAttempts = [];
  const replies = [...responses];
  const played = [];
  let capture;
  const microphone = { readyState: 'new', stop() { this.readyState = 'ended'; } };
  const audioCalls = { start: 0, interrupt: 0, stop: 0 };
  const audio = {
    async start(onPCM) {
      audioCalls.start++;
      capture = onPCM;
      microphone.readyState = 'live';
    },
    play(pcm) { played.push(bytes(pcm)); },
    interrupt() { audioCalls.interrupt++; },
    stop() { audioCalls.stop++; microphone.stop(); },
  };

  class FakeWebSocket extends EventTarget {
    static CONNECTING = 0;
    static OPEN = 1;
    static CLOSING = 2;
    static CLOSED = 3;

    constructor(url) {
      super();
      this.url = String(url);
      this.readyState = FakeWebSocket.CONNECTING;
      this.sent = [];
      sockets.push(this);
    }

    emit(event) {
      this.dispatchEvent(event);
      this[`on${event.type}`]?.(event);
    }

    open() {
      this.readyState = FakeWebSocket.OPEN;
      this.emit(new Event('open'));
    }

    message(data) { this.emit(new MessageEvent('message', { data })); }
    control(message) { this.message(JSON.stringify(message)); }

    send(data) {
      if (this.readyState !== FakeWebSocket.OPEN) throw new Error('socket is not open');
      this.sent.push(typeof data === 'string' ? data : bytes(data));
    }

    close() {
      if (this.readyState === FakeWebSocket.CLOSED) return;
      this.readyState = FakeWebSocket.CLOSED;
      this.emit(new Event('close'));
    }
  }

  const fetch = async (input, init = {}) => {
    const url = new URL(String(input), ORIGIN);
    const request = {
      url, method: init.method || 'GET',
      body: init.body ? JSON.parse(init.body) : null,
    };
    requests.push(request);
    if (url.origin !== ORIGIN) throw new Error(`unexpected external request: ${url}`);
    let reply;
    if (url.pathname === '/api/voice/calls' && request.method === 'POST') {
      reply = replies.shift();
      if (!reply) throw new Error('unexpected extra call creation');
    } else if (url.pathname === '/api/voice/calls/call-1/hangup' && request.method === 'POST') {
      reply = { status: 204 };
    } else {
      throw new Error(`unrouted ${request.method} ${url.pathname}`);
    }
    return new Response(reply.body ? JSON.stringify(reply.body) : null, {
      status: reply.status, headers: { 'Content-Type': 'application/json' },
    });
  };

  const navigate = (url) => { navigations.push(String(url)); };
  const location = {
    origin: ORIGIN, protocol: 'https:', host: 'moa.test', hostname: 'moa.test',
    get href() { return `${ORIGIN}/`; },
    set href(url) { navigate(url); },
    assign: navigate, replace: navigate,
  };
  const globals = {
    location, open: navigate, window: { location, open: navigate },
    fetch, WebSocket: FakeWebSocket,
    RTCPeerConnection: function (...args) {
      peerAttempts.push(args);
      throw new Error('browser WebRTC is forbidden for voice calls');
    },
  };
  const originals = new Map(Object.keys(globals).map((key) => [
    key, Object.getOwnPropertyDescriptor(globalThis, key),
  ]));
  const restore = () => {
    for (const [key, descriptor] of originals) {
      if (descriptor) Object.defineProperty(globalThis, key, descriptor);
      else delete globalThis[key];
    }
  };
  for (const [key, value] of Object.entries(globals)) {
    Object.defineProperty(globalThis, key, { configurable: true, writable: true, value });
  }

  let controller;
  try {
    controller = new VoiceCallController({
      sessionId, fetch, WebSocket: FakeWebSocket, audio,
      ...callbacks,
      onState: (state) => { states.push(structuredClone(state)); },
    });
  } catch (error) {
    restore();
    throw error;
  }
  return {
    controller, requests, sockets, states, played, audioCalls, microphone,
    peerAttempts, navigations, restore,
    capture(pcm) {
      expect(typeof capture).toBe('function');
      // Retain the callback after stop, as an already queued worklet frame can
      // arrive late. The controller, not the fake microphone, must ignore it.
      capture(pcm);
    },
  };
}

function bytes(pcm) {
  if (pcm instanceof ArrayBuffer) return Array.from(new Uint8Array(pcm));
  if (ArrayBuffer.isView(pcm)) {
    return Array.from(new Uint8Array(pcm.buffer, pcm.byteOffset, pcm.byteLength));
  }
  throw new TypeError('audio frames must be binary PCM, not JSON or base64');
}

function creates(fixture) {
  return fixture.requests.filter((request) => request.url.pathname === '/api/voice/calls');
}

function expectMoaOnly(fixture) {
  expect(fixture.peerAttempts).toEqual([]);
  expect(fixture.navigations).toEqual([]);
  for (const { url } of fixture.requests) expect(url.origin).toBe(ORIGIN);
  for (const socket of fixture.sockets) {
    const url = new URL(socket.url, ORIGIN);
    expect(['wss:', 'https:']).toContain(url.protocol);
    expect(url.host).toBe('moa.test');
    expect(url.pathname).toBe('/api/voice/calls/call-1/ws');
    expect(url.search).toBe('');
  }
}

function listening(fixture, options) {
  return finishListening(fixture, fixture.controller.start(options));
}

async function finishListening(fixture, started) {
  await flush();
  expect(fixture.sockets).toHaveLength(1);
  const socket = fixture.sockets[0];
  socket.open();
  await flush();
  socket.control({ type: 'state', state: 'listening' });
  await started;
  await flush();
  expect(fixture.controller.state().phase).toBe('listening');
  return socket;
}

async function endFromServer(fixture, socket, metadata = {}) {
  socket.control({ type: 'ended', reason: 'hangup', ...metadata });
  await flush();
  expect(fixture.controller.state().phase).toBe('ended');
}

test('voice-call: plan billing shows plan; only plan_unavailable offers paid, and only an explicit click posts the paid provider', async () => {
  const { VoiceCallController } = await import('./voice-call.js');

  for (const provider of ['grok-plan', 'chatgpt-plan']) {
    const fixture = setup(VoiceCallController, { responses: [created(provider)] });
    try {
      expect(fixture.controller.state()).toMatchObject({
        phase: 'idle', billing: null, billingLabel: '', paidOption: null,
      });
      const socket = await listening(fixture);
      expect(creates(fixture).map((request) => request.body)).toEqual([{ session_id: 'sess-1' }]);
      expect(fixture.controller.state()).toMatchObject({
        provider, billing: PLAN, billingLabel: provider === 'grok-plan' ? 'Uses your Grok plan' : 'Uses your ChatGPT plan', paidOption: null,
      });
      expect(fixture.states.some((state) => state.billingLabel === `Uses your ${provider === 'grok-plan' ? 'Grok' : 'ChatGPT'} plan`)).toBe(true);
      expect(fixture.states.every((state) => !/included|free/i.test(state.billingLabel))).toBe(true);
      await endFromServer(fixture, socket);
      expect(fixture.states.every((state) => !state.billingLabel.includes('$'))).toBe(true);
      expect(fixture.states.every((state) => state.paidOption === null)).toBe(true);
      expect(creates(fixture)).toHaveLength(1);
      expectMoaOnly(fixture);
    } finally {
      fixture.restore();
    }
  }

  // A stray paid_option on another error must not become a paid action; a
  // plan failure without a configured paid key must not fabricate one either.
  for (const body of [
    { error: 'upstream_unavailable', reason: 'Try again', paid_option: PAID_OPTIONS[0] },
    { error: 'plan_unavailable', reason: 'Plan has no capacity' },
  ]) {
    const fixture = setup(VoiceCallController, { responses: [{ status: 503, body }] });
    try {
      await fixture.controller.start();
      await flush();
      expect(fixture.controller.state().paidOption).toBeNull();
      expect(fixture.states.every((state) => state.paidOption === null)).toBe(true);
      expect(fixture.states.every((state) => !state.billingLabel.includes('$'))).toBe(true);
      expect(creates(fixture).map((request) => request.body)).toEqual([{ session_id: 'sess-1' }]);
      expect(fixture.sockets).toHaveLength(0);
      expectMoaOnly(fixture);
    } finally {
      fixture.restore();
    }
  }

  for (const { provider, billing, label } of PAID_OPTIONS) {
    const paidOption = { provider, billing };
    const fixture = setup(VoiceCallController, {
      responses: [
        { status: 503, body: { error: 'plan_unavailable', reason: 'Plan limit reached', paid_option: paidOption } },
        created(provider, billing),
      ],
    });
    try {
      await fixture.controller.start();
      await flush();
      expect(fixture.controller.state()).toMatchObject({
        error: 'plan_unavailable', reason: 'Plan limit reached', paidOption,
      });
      expect(fixture.states.some((state) => state.paidOption?.provider === provider)).toBe(true);
      expect(fixture.states.every((state) => !state.billingLabel.includes('$'))).toBe(true);
      expect(creates(fixture).map((request) => request.body)).toEqual([{ session_id: 'sess-1' }]);
      expect(fixture.sockets).toHaveLength(0);
      expect(fixture.audioCalls.start).toBe(0);

      // This invocation models the paid button's click, not an automatic
      // retry hidden in the harness. Nothing has selected the API before it.
      const clickPaidAction = () => fixture.controller.start({
        provider: fixture.controller.state().paidOption.provider,
      });
      const socket = await finishListening(fixture, clickPaidAction());
      expect(creates(fixture).map((request) => request.body)).toEqual([
        { session_id: 'sess-1' }, { session_id: 'sess-1', provider },
      ]);
      expect(fixture.controller.state()).toMatchObject({
        provider, billing, billingLabel: label, paidOption: null,
      });
      expect(fixture.states.some((state) => state.billingLabel === label)).toBe(true);
      await endFromServer(fixture, socket);
      for (const state of fixture.states) {
        if (state.billingLabel.includes('$')) expect(state.billing.kind).toBe('per_minute');
      }
      expect(creates(fixture)).toHaveLength(2);
      expectMoaOnly(fixture);
    } finally {
      fixture.restore();
    }
  }
});

test('voice-call: all four providers use one moa WebSocket for captured and replayed PCM, never WebRTC or external links, and release the microphone on ending', async () => {
  const { VoiceCallController } = await import('./voice-call.js');

  for (const provider of ['grok-plan', 'chatgpt-plan', 'xai-api', 'openai-live-api']) {
    const billing = PAID_OPTIONS.find((option) => option.provider === provider)?.billing || PLAN;
    const fixture = setup(VoiceCallController, { responses: [created(provider, billing)] });
    try {
      // Explicitly selecting an API here models a user action as well.
      const socket = await listening(fixture, { provider });
      expect(creates(fixture).map((request) => request.body)).toEqual([{ session_id: 'sess-1', provider }]);
      expect(fixture.audioCalls.start).toBe(1);
      expect(fixture.microphone.readyState).toBe('live');
      expect(socket.binaryType).toBe('arraybuffer');

      // Nonzero offset and signed PCM16 boundaries catch text/base64 transport
      // and accidental transmission of the entire backing buffer.
      const captured = new Uint8Array([99, 0, 128, 255, 127, 1, 0, 88]).subarray(1, 7);
      fixture.capture(captured);
      await flush();
      expect(socket.sent.filter((frame) => Array.isArray(frame))).toEqual([[0, 128, 255, 127, 1, 0]]);
      expect(socket.sent.filter((frame) => typeof frame === 'string').map((frame) => JSON.parse(frame).type))
        .not.toContain('input_audio_buffer.append');

      socket.control({ type: 'state', state: 'speaking' });
      socket.message(new Uint8Array([255, 255, 0, 0, 0, 64]).buffer);
      await flush();
      expect(fixture.controller.state().phase).toBe('speaking');
      expect(fixture.played).toEqual([[255, 255, 0, 0, 0, 64]]);
      const interrupted = fixture.audioCalls.interrupt;
      socket.control({ type: 'speech_started' });
      await flush();
      expect(fixture.audioCalls.interrupt).toBe(interrupted + 1);

      if (provider.endsWith('-api')) {
        const ending = fixture.controller.hangup();
        await flush();
        expect(fixture.requests.filter((request) => request.url.pathname.endsWith('/hangup')))
          .toMatchObject([{ method: 'POST' }]);
        await endFromServer(fixture, socket);
        await ending;
      } else {
        await endFromServer(fixture, socket);
      }
      expect(fixture.audioCalls.stop).toBeGreaterThan(0);
      expect(fixture.microphone.readyState).toBe('ended');
      expect(socket.readyState).toBe(3);
      const sentAtEnd = socket.sent.length;
      const playedAtEnd = fixture.played.length;
      fixture.capture(new Uint8Array([1, 0]));
      socket.message(new Uint8Array([2, 0]).buffer);
      await flush();
      expect(socket.sent).toHaveLength(sentAtEnd);
      expect(fixture.played).toHaveLength(playedAtEnd);
      expect(fixture.sockets).toHaveLength(1);
      expectMoaOnly(fixture);
    } finally {
      fixture.restore();
    }
  }
});

test('voice-call: server-saved minutes never invoke draft-writing callbacks for owner or ordinary sessions and preserve empty or pre-existing drafts', async () => {
  const { VoiceCallController } = await import('./voice-call.js');

  // This checks the controller's callback boundary, NOT Composer DOM wiring.
  // A real Composer test must still exercise both call entry points and the
  // textarea/store draft while the real hook receives the terminal event.
  for (const sessionId of ['owner-session', 'ordinary-session']) {
    for (const initialDraft of ['', 'My unsent draft\n  keep the spacing 👋  ']) {
      let draft = initialDraft;
      const writes = [];
      const draftWriter = (callback) => (...args) => {
        writes.push({ callback, args });
        draft = String(args[0] ?? '');
      };
      const fixture = setup(VoiceCallController, {
        sessionId, responses: [created('chatgpt-plan')],
        onMinutes: draftWriter('onMinutes'), onResult: draftWriter('onResult'),
      });
      try {
        const socket = await listening(fixture);
        expect(creates(fixture).map((request) => request.body)).toEqual([{ session_id: sessionId }]);
        fixture.capture(new Uint8Array([1, 0]));
        socket.message(new Uint8Array([2, 0]).buffer);
        await flush();
        expect(socket.sent.filter((frame) => Array.isArray(frame))).toEqual([[1, 0]]);
        expect(fixture.played).toEqual([[2, 0]]);
        expect(writes).toEqual([]);
        expect(draft).toBe(initialDraft);

        // Even if completion includes already-saved minutes as metadata, it
        // is not a draft event and cannot reuse the legacy onResult path.
        const metadata = {
          minutes_saved: true, minutes: 'Decided: keep the record in the conversation.',
          transcript_url: '/api/voice/calls/call-1/transcript.md',
        };
        const ending = fixture.controller.hangup();
        await flush();
        await endFromServer(fixture, socket, metadata);
        await ending;
        // Queued duplicate completion must not deliver a legacy result either.
        socket.control({ type: 'ended', reason: 'hangup', ...metadata });
        await flush();
        expect(writes).toEqual([]);
        expect(draft).toBe(initialDraft);
        expect(fixture.microphone.readyState).toBe('ended');
        expect(fixture.requests.map((request) => request.url.pathname)).toEqual([
          '/api/voice/calls', '/api/voice/calls/call-1/hangup',
        ]);
        expect(fixture.states.some((state) => state.phase === 'listening')).toBe(true);
        expect(fixture.states.at(-1).phase).toBe('ended');
        expectMoaOnly(fixture);
      } finally {
        fixture.restore();
      }
    }
  }
});
