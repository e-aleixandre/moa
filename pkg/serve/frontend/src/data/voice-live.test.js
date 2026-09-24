import { test, expect } from 'bun:test';
import {
  VoiceLiveController, appendCallResult, callSpendNotice, truncateForTokens, callFailureMessage,
  MAX_QUESTIONS, ASK_POLL_MS, ASK_TIMEOUT_MS, MIC_GRACE_MS, ICE_TIMEOUT_MS,
  CLOSE_BACKSTOP_MS, SESSION_ERROR_GRACE_MS, THINKING_TOKEN_BUDGET,
  HEARTBEAT_MS, MINUTES_ON_HANGUP_MS,
  MIC_LIVE, MIC_NOT_LIVE, MIC_UNKNOWN,
} from './voice-live.js';

// A manual clock: the ask poll, the ICE wait, the close backstop and the
// microphone grace period are all timers, and a test that waited for real ones
// would be a slow test that proves less.
function fakeClock() {
  let now = 0;
  let nextId = 1;
  const timers = new Map();
  return {
    now: () => now,
    setTimeout: (fn, ms) => {
      const id = nextId++;
      timers.set(id, { fn, at: now + ms });
      return id;
    },
    clearTimeout: (id) => { timers.delete(id); },
    async advance(ms) {
      const target = now + ms;
      let guard = 0;
      for (;;) {
        const due = [...timers.entries()]
          .filter(([, timer]) => timer.at <= target)
          .sort((a, b) => a[1].at - b[1].at)[0];
        if (!due || guard++ > 10_000) break;
        const [id, timer] = due;
        timers.delete(id);
        now = timer.at;
        timer.fn();
        await flush();
      }
      now = target;
      await flush();
    },
    pending: () => timers.size,
  };
}

async function flush(times = 12) {
  for (let i = 0; i < times; i++) await Promise.resolve();
}

function deferred() {
  let resolve;
  const promise = new Promise((res) => { resolve = res; });
  return { promise, resolve };
}

class FakeChannel {
  constructor(label) {
    this.label = label;
    this.readyState = 'open';
    this.sent = [];
  }

  send(raw) {
    if (this.readyState !== 'open') throw new Error('channel is not open');
    this.sent.push(JSON.parse(raw));
  }

  close() { this.readyState = 'closed'; }
  deliver(event) { this.onmessage?.({ data: JSON.stringify(event) }); }
  typesSent() { return this.sent.map((message) => message.type); }
  outputs() {
    return this.sent
      .filter((message) => message.type === 'response.item.create')
      .map((message) => JSON.parse(message.item.output));
  }
}

function fakeTrack(overrides = {}) {
  return {
    readyState: 'live',
    muted: false,
    enabled: true,
    stop() { this.readyState = 'ended'; },
    ...overrides,
  };
}

function fakeStream(track) {
  return { getTracks: () => [track] };
}

function makePeerConnection(log, { iceGatheringState = 'complete', onSetRemote } = {}) {
  let channel = null;
  const pc = {
    connectionState: 'new',
    iceGatheringState,
    localDescription: null,
    remoteDescription: null,
    addTrack: () => log.push('addTrack'),
    createDataChannel: (label) => {
      log.push('createDataChannel');
      channel = new FakeChannel(label);
      return channel;
    },
    createOffer: async () => {
      log.push('createOffer');
      return { type: 'offer', sdp: 'OFFER' };
    },
    setLocalDescription: async (desc) => {
      log.push('setLocalDescription');
      pc.localDescription = desc;
    },
    setRemoteDescription: async (desc) => {
      log.push('setRemoteDescription');
      pc.remoteDescription = desc;
      if (onSetRemote) await onSetRemote(pc, channel);
    },
    close: () => log.push('close'),
    channel: () => channel,
    completeIce: () => {
      pc.iceGatheringState = 'complete';
      pc.onicegatheringstatechange?.();
    },
    setConnectionState: (state) => {
      pc.connectionState = state;
      pc.onconnectionstatechange?.();
    },
  };
  return pc;
}

function jsonResponse(body, status = 200) {
  return { ok: status >= 200 && status < 300, status, json: async () => body, text: async () => JSON.stringify(body) };
}

function setup(overrides = {}) {
  const log = [];
  const requests = [];
  const results = [];
  const errors = [];
  const states = [];
  const clock = fakeClock();
  const track = overrides.track || fakeTrack();
  const pc = makePeerConnection(log, overrides.pcOptions);
  const documentRef = overrides.document || { hidden: false, addEventListener() {}, removeEventListener() {} };

  const routes = {
    '/api/voice/live/session': () => jsonResponse({
      live_session_id: 'live-1', sdp: 'ANSWER', owner_id: 'owner-1', brief_messages: 12,
      pricing: {
        voice_usd_per_minute: 0.05,
        voice_min_billed_seconds: 15,
        backend_model: 'gpt-5.6-terra',
        backend_input_usd_per_mtok: 2,
        backend_output_usd_per_mtok: 12,
      },
    }),
    // The server-side lifetime of the call: a heartbeat while it runs and a
    // close when it ends. Present by default because every call makes them.
    '/api/voice/live/heartbeat': () => ({ ok: true, status: 204, json: async () => ({}), text: async () => '' }),
    '/api/voice/live/close': () => ({ ok: true, status: 204, json: async () => ({}), text: async () => '' }),
    ...overrides.routes,
  };
  const controller = new VoiceLiveController({
    sessionId: 'sess-1',
    document: documentRef,
    now: clock.now,
    setTimeout: clock.setTimeout,
    clearTimeout: clock.clearTimeout,
    RTCPeerConnection: function () { return pc; },
    getUserMedia: async () => fakeStream(track),
    Audio: function () { return { play: () => Promise.resolve() }; },
    fetch: async (url, init) => {
      requests.push({ url, init, body: init?.body ? JSON.parse(init.body) : null });
      const key = Object.keys(routes).find((prefix) => String(url).startsWith(prefix));
      if (!key) throw new Error(`unrouted ${url}`);
      return routes[key](url, init);
    },
    onState: (state) => states.push(state),
    onResult: (text, meta) => results.push({ text, meta }),
    onError: (message) => errors.push(message),
  });
  return { controller, log, requests, results, errors, states, clock, pc, track, documentRef };
}

async function connected(fixture) {
  const started = fixture.controller.start();
  await flush();
  fixture.pc.channel().deliver({ type: 'session.started', session: { id: 'live-1' } });
  await started;
  return fixture.pc.channel();
}

test('connect creates the oai-events data channel before the offer and never starts the session itself', async () => {
  const fixture = setup();
  const channel = await connected(fixture);

  expect(fixture.log.indexOf('createDataChannel')).toBeLessThan(fixture.log.indexOf('createOffer'));
  expect(fixture.log.indexOf('createOffer')).toBeLessThan(fixture.log.indexOf('setLocalDescription'));
  expect(fixture.log.indexOf('setLocalDescription')).toBeLessThan(fixture.log.indexOf('setRemoteDescription'));
  expect(channel.label).toBe('oai-events');

  const post = fixture.requests[0];
  expect(post.url).toBe('/api/voice/live/session');
  expect(post.body).toEqual({ session_id: 'sess-1', sdp: 'OFFER', note: '' });
  // The offer POST is what started the session; session.start would start a second one.
  expect(channel.typesSent()).not.toContain('session.start');
  expect(fixture.pc.remoteDescription).toEqual({ type: 'answer', sdp: 'ANSWER' });
  expect(fixture.controller.state().phase).toBe('live');
  expect(fixture.controller.ownerId).toBe('owner-1');
});

// Finding 1: session.started can arrive while setRemoteDescription is still
// awaiting, i.e. before anything is listening for it.
test('a session.started that arrives before anyone waits for it still starts the call', async () => {
  const fixture = setup({
    pcOptions: {
      onSetRemote: (pc, channel) => {
        channel.deliver({ type: 'session.started', session: { id: 'live-1' } });
      },
    },
  });
  const ok = await fixture.controller.start();
  expect(ok).toBe(true);
  expect(fixture.controller.state().phase).toBe('live');
  // No timeout fired and nothing was torn down.
  expect(fixture.errors).toEqual([]);
  expect(fixture.track.readyState).toBe('live');
});

// Finding 2: an incomplete candidate set is not an offer worth spending a
// (billable) session on.
test('ICE gathering that never completes fails the call instead of posting a partial offer', async () => {
  const fixture = setup({ pcOptions: { iceGatheringState: 'gathering' } });
  const started = fixture.controller.start();
  await flush();
  expect(fixture.requests).toHaveLength(0);

  await fixture.clock.advance(ICE_TIMEOUT_MS);
  expect(await started).toBe(false);
  // No session was ever created, so there is nothing billable to close.
  expect(fixture.requests).toHaveLength(0);
  expect(fixture.errors[0]).toContain('never finished negotiating');
  expect(fixture.controller.state().phase).toBe('ended');
  expect(fixture.track.readyState).toBe('ended');
});

test('the offer is posted as soon as ICE gathering completes', async () => {
  const fixture = setup({ pcOptions: { iceGatheringState: 'gathering' } });
  const started = fixture.controller.start();
  await flush();
  expect(fixture.requests).toHaveLength(0);

  await fixture.clock.advance(1000);
  fixture.pc.completeIce();
  await flush();
  fixture.pc.channel().deliver({ type: 'session.started' });
  expect(await started).toBe(true);
  expect(fixture.requests[0].url).toBe('/api/voice/live/session');
});

test('a tool call wrapped in response.event is executed and answered with output + response.create', async () => {
  const fixture = setup({
    routes: {
      '/api/owners/owner-1/book': () => jsonResponse({ owner_id: 'owner-1', files: [{ path: 'PROJECT.md' }, { path: 'areas/erp.md' }] }),
    },
  });
  const channel = await connected(fixture);

  // An arguments-done event alone is not a finished call and must do nothing.
  channel.deliver({ type: 'response.event', event: { type: 'response.function_call_arguments.done', call_id: 'c1', arguments: '{}' } });
  await flush();
  expect(channel.sent).toHaveLength(0);

  channel.deliver({
    type: 'response.event',
    event: {
      type: 'response.output_item.done',
      item: { type: 'function_call', call_id: 'c1', name: 'book_list', arguments: '{}' },
    },
  });
  await flush();

  expect(fixture.requests.at(-1).url).toBe('/api/owners/owner-1/book');
  expect(channel.typesSent()).toEqual(['response.item.create', 'response.create']);
  expect(channel.sent[0].item).toMatchObject({ type: 'function_call_output', call_id: 'c1' });
  expect(channel.outputs()[0]).toEqual({ status: 'ok', files: ['PROJECT.md', 'areas/erp.md'] });
  // Every client command carries an event_id so a later error can name it.
  expect(channel.sent.every((message) => typeof message.event_id === 'string' && message.event_id)).toBe(true);
});

// Recorded against the real service (gpt-live-1 delegating to gpt-5.6-terra):
// one backend response emitted two book_read calls, and resuming it after the
// first output was rejected with `function_call_outputs_required` — "Missing
// function call outputs for: call_…" — which surfaced as an error toast.
test('parallel tool calls of one backend response are resumed once, after every output and the response end', async () => {
  const gate = deferred();
  const fixture = setup({
    routes: {
      '/api/owners/owner-1/book/a.md': () => jsonResponse({ content: 'A' }),
      '/api/owners/owner-1/book/b.md': async () => { await gate.promise; return jsonResponse({ content: 'B' }); },
    },
  });
  const channel = await connected(fixture);
  const wrapped = (event) => channel.deliver({ type: 'response.event', delegation_id: 'item_1', event });
  const call = (callId, path) => wrapped({
    type: 'response.output_item.done',
    item: { type: 'function_call', call_id: callId, name: 'book_read', arguments: JSON.stringify({ path }) },
  });

  wrapped({ type: 'response.created', response: { id: 'resp_1', output: [] } });
  call('call_a', 'a.md');
  await flush();
  // a.md is answered, but the response is still emitting calls.
  expect(channel.typesSent()).toEqual(['response.item.create']);

  call('call_b', 'b.md');
  wrapped({ type: 'response.completed', response: { id: 'resp_1', output: [] } });
  await flush();
  // The response ended, but call_b's output is still being fetched.
  expect(channel.typesSent()).toEqual(['response.item.create']);

  gate.resolve();
  await flush();
  expect(channel.typesSent()).toEqual(['response.item.create', 'response.item.create', 'response.create']);
  expect(channel.sent.slice(0, 2).map((message) => message.item.call_id)).toEqual(['call_a', 'call_b']);

  // The continuation is a new response of the same delegation: its own tool
  // round resumes on its own.
  wrapped({ type: 'response.created', response: { id: 'resp_2', output: [] } });
  call('call_c', 'a.md');
  wrapped({ type: 'response.completed', response: { id: 'resp_2', output: [] } });
  await flush();
  expect(channel.typesSent()).toEqual([
    'response.item.create', 'response.item.create', 'response.create',
    'response.item.create', 'response.create',
  ]);
  expect(fixture.errors).toEqual([]);
});

test('the book is reported unavailable, never fabricated, when the session has no owner', async () => {
  const fixture = setup({
    routes: {
      '/api/voice/live/session': () => jsonResponse({ live_session_id: 'l', sdp: 'ANSWER', owner_id: '' }),
    },
  });
  const channel = await connected(fixture);
  channel.deliver({
    type: 'response.output_item.done',
    item: { type: 'function_call', call_id: 'c9', name: 'book_read', arguments: '{"path":"areas/erp.md"}' },
  });
  await flush();
  const output = channel.outputs()[0];
  expect(output.status).toBe('unavailable');
  expect(output.message).toContain('no owner');
  // No book request was attempted: only the session POST happened.
  expect(fixture.requests).toHaveLength(1);
});

test('ask_session acknowledges immediately and delivers the answer later as a thinking append', async () => {
  let status = 'pending';
  const fixture = setup({
    routes: {
      '/api/voice/live/ask': (url, init) => (init?.method === 'POST'
        ? jsonResponse({ ask_id: 'ask-1' })
        : jsonResponse({ status, answer: 'Ship it on Friday.' })),
    },
  });
  const channel = await connected(fixture);

  channel.deliver({
    type: 'response.output_item.done',
    item: { type: 'function_call', call_id: 'c2', name: 'ask_session', arguments: '{"question":"When do we ship?"}' },
  });
  await flush();

  const ack = channel.outputs()[0];
  expect(ack.status).toBe('asked');
  expect(ack.questions_left).toBe(MAX_QUESTIONS - 1);
  // The answer is NOT part of the function output: the delegate keeps talking.
  expect(JSON.stringify(ack)).not.toContain('Friday');
  expect(channel.typesSent()).toEqual(['response.item.create', 'response.create']);
  expect(fixture.controller.state().pendingAsks).toBe(1);

  await fixture.clock.advance(ASK_POLL_MS);
  expect(channel.typesSent()).toEqual(['response.item.create', 'response.create']);

  status = 'answered';
  await fixture.clock.advance(ASK_POLL_MS);
  const append = channel.sent.at(-1);
  expect(append.type).toBe('session.thinking.append');
  expect(append.delegation_id).toBe(null);
  expect(append.content).toContain('Ship it on Friday.');
  expect(fixture.controller.state().pendingAsks).toBe(0);
});

test('an unanswered question becomes an explicit pending-confirmation instruction, not silence', async () => {
  const fixture = setup({
    routes: {
      '/api/voice/live/ask': (url, init) => (init?.method === 'POST'
        ? jsonResponse({ ask_id: 'ask-2' })
        : jsonResponse({ status: 'pending' })),
    },
  });
  const channel = await connected(fixture);
  channel.deliver({
    type: 'response.output_item.done',
    item: { type: 'function_call', call_id: 'c3', name: 'ask_session', arguments: '{"question":"Do we bill this?"}' },
  });
  await flush();

  await fixture.clock.advance(ASK_TIMEOUT_MS + ASK_POLL_MS);
  const append = channel.sent.at(-1);
  expect(append.type).toBe('session.thinking.append');
  expect(append.content).toContain('pending confirmation');
});

test('the sixth question of a call is refused by the client, not by the prompt', async () => {
  const fixture = setup({
    routes: {
      '/api/voice/live/ask': (url, init) => (init?.method === 'POST'
        ? jsonResponse({ ask_id: 'ask-x' })
        : jsonResponse({ status: 'pending' })),
    },
  });
  const channel = await connected(fixture);

  for (let i = 0; i < MAX_QUESTIONS + 1; i++) {
    channel.deliver({
      type: 'response.output_item.done',
      item: { type: 'function_call', call_id: `q${i}`, name: 'ask_session', arguments: `{"question":"q${i}"}` },
    });
    await flush();
  }

  const outputs = channel.outputs();
  expect(outputs).toHaveLength(MAX_QUESTIONS + 1);
  expect(outputs.slice(0, MAX_QUESTIONS).every((output) => output.status === 'asked')).toBe(true);
  expect(outputs.at(-1).status).toBe('limit_reached');
  expect(outputs.at(-1).message).toContain('pending confirmation');
  expect(fixture.controller.state().questionsUsed).toBe(MAX_QUESTIONS);
  // Exactly five questions were POSTed: the sixth never reached the session.
  const posts = fixture.requests.filter((request) => request.url === '/api/voice/live/ask' && request.init?.method === 'POST');
  expect(posts).toHaveLength(MAX_QUESTIONS);
});

// Finding 6: tool calls are dispatched concurrently. Six asks issued before
// any POST resolves must still spend exactly five slots.
test('concurrent ask_session calls cannot spend the same question slot twice', async () => {
  const gate = deferred();
  const fixture = setup({
    routes: {
      '/api/voice/live/ask': async (url, init) => {
        if (init?.method !== 'POST') return jsonResponse({ status: 'pending' });
        await gate.promise;
        return jsonResponse({ ask_id: 'ask-c' });
      },
    },
  });
  const channel = await connected(fixture);

  for (let i = 0; i < MAX_QUESTIONS + 1; i++) {
    channel.deliver({
      type: 'response.output_item.done',
      item: { type: 'function_call', call_id: `p${i}`, name: 'ask_session', arguments: `{"question":"p${i}"}` },
    });
  }
  await flush();
  // Every POST is still in flight, yet the counter is already spent.
  expect(fixture.controller.state().questionsUsed).toBe(MAX_QUESTIONS);
  gate.resolve();
  await flush();

  const outputs = channel.outputs();
  expect(outputs.filter((output) => output.status === 'asked')).toHaveLength(MAX_QUESTIONS);
  expect(outputs.filter((output) => output.status === 'limit_reached')).toHaveLength(1);
  const posts = fixture.requests.filter((request) => request.url === '/api/voice/live/ask' && request.init?.method === 'POST');
  expect(posts).toHaveLength(MAX_QUESTIONS);
});

test('a question that never reached the session gives its slot back', async () => {
  const fixture = setup({
    routes: {
      '/api/voice/live/ask': (url, init) => (init?.method === 'POST'
        ? jsonResponse({ error: 'nope' }, 500)
        : jsonResponse({ status: 'pending' })),
    },
  });
  const channel = await connected(fixture);
  channel.deliver({
    type: 'response.output_item.done',
    item: { type: 'function_call', call_id: 'c8', name: 'ask_session', arguments: '{"question":"q"}' },
  });
  await flush();
  expect(channel.outputs()[0].status).toBe('error');
  expect(fixture.controller.state().questionsUsed).toBe(0);
  expect(fixture.controller.state().pendingAsks).toBe(0);
});

test('end_call closes the session and delivers the minutes verbatim', async () => {
  const fixture = setup();
  const channel = await connected(fixture);
  channel.deliver({
    type: 'response.output_item.done',
    item: {
      type: 'function_call',
      call_id: 'c4',
      name: 'end_call',
      arguments: JSON.stringify({
        minutes: 'Decidido: la factura sale el lunes.\n\nPendiente de confirmar: quién la firma.',
      }),
    },
  });
  await flush();
  expect(channel.typesSent()).toContain('session.close');
  expect(fixture.results).toHaveLength(0); // still waiting for session.closed

  channel.deliver({ type: 'session.closed', reason: 'close_requested', usage: { seconds: 184 } });
  await flush();

  expect(fixture.results).toHaveLength(1);
  // Verbatim: the delegate's own sections, in the call's language, with no
  // heading of ours appended after them.
  expect(fixture.results[0].text).toBe(
    'Decidido: la factura sale el lunes.\n\nPendiente de confirmar: quién la firma.',
  );
  expect(fixture.results[0].meta).toMatchObject({ minutes: true, voiceSeconds: 184, usageConfirmed: true });
  expect(fixture.track.readyState).toBe('ended');
});

// Finding 7: an empty end_call must not swallow the call.
test('end_call with empty minutes still rescues everything that was said', async () => {
  const fixture = setup();
  const channel = await connected(fixture);
  channel.deliver({ type: 'session.input_transcript.delta', delta: 'The invoice goes out Monday.' });
  channel.deliver({
    type: 'response.output_item.done',
    item: { type: 'function_call', call_id: 'c5', name: 'end_call', arguments: JSON.stringify({ minutes: '   ' }) },
  });
  await flush();
  channel.deliver({ type: 'session.closed', reason: 'close_requested', usage: { seconds: 20 } });
  await flush();

  expect(fixture.results).toHaveLength(1);
  expect(fixture.results[0].meta.minutes).toBe(false);
  expect(fixture.results[0].text).toContain('without writing any');
  expect(fixture.results[0].text).toContain('Owner: The invoice goes out Monday.');
});

test('an interrupted call with no minutes rescues the whole transcript, in order and with speakers', async () => {
  const fixture = setup();
  const channel = await connected(fixture);
  channel.deliver({ type: 'session.input_transcript.delta', delta: 'We need ' });
  channel.deliver({ type: 'session.input_transcript.delta', delta: 'the ERP invoice fixed.' });
  channel.deliver({ type: 'session.output_transcript.delta', delta: 'Understood, I will note it.' });
  await flush();

  fixture.pc.setConnectionState('failed');
  await flush();

  expect(fixture.results).toHaveLength(1);
  const text = fixture.results[0].text;
  expect(text).toContain('ended before the delegate wrote any minutes');
  expect(text).toContain('the connection was lost');
  expect(text.indexOf('Owner: We need the ERP invoice fixed.')).toBeGreaterThan(-1);
  expect(text.indexOf('Owner: We need the ERP invoice fixed.'))
    .toBeLessThan(text.indexOf('Delegate: Understood, I will note it.'));
  expect(fixture.results[0].meta.minutes).toBe(false);
  expect(fixture.results[0].meta.usageConfirmed).toBe(false);
});

// Finding 8: session.closed is the teardown trigger, so late deltas and the
// final usage still count.
test('a hangup keeps the call alive until session.closed, then delivers the final usage', async () => {
  const fixture = setup();
  const channel = await connected(fixture);
  channel.deliver({ type: 'session.output_transcript.delta', delta: 'Let me wrap up. ' });
  const hangup = fixture.controller.hangup();
  await flush();
  // The delegate is asked for the minutes first; the close follows.
  await fixture.clock.advance(MINUTES_ON_HANGUP_MS);
  expect(channel.typesSent()).toContain('session.close');
  expect(fixture.results).toHaveLength(0);
  expect(fixture.track.readyState).toBe('live'); // the microphone is not cut early

  // Late events still arrive during the drain, exactly as the docs describe.
  await fixture.clock.advance(CLOSE_BACKSTOP_MS - 1000);
  channel.deliver({ type: 'session.output_transcript.delta', delta: 'Noted.' });
  channel.deliver({ type: 'session.closed', reason: 'close_requested', usage: { seconds: 42 } });
  await hangup;

  expect(fixture.results[0].text).toContain('Delegate: Let me wrap up. Noted.');
  expect(fixture.results[0].meta).toMatchObject({ voiceSeconds: 42, usageConfirmed: true });
  expect(fixture.track.readyState).toBe('ended');
});

test('a session that never confirms its close releases the microphone at the backstop, with usage unconfirmed', async () => {
  const fixture = setup();
  const channel = await connected(fixture);
  channel.deliver({ type: 'session.usage.updated', usage: { seconds: 30 } });
  channel.deliver({ type: 'session.output_transcript.delta', delta: 'Hello?' });
  const hangup = fixture.controller.hangup();
  await flush();
  await fixture.clock.advance(MINUTES_ON_HANGUP_MS);
  await fixture.clock.advance(CLOSE_BACKSTOP_MS);
  await hangup;

  expect(fixture.results[0].text).toContain('Delegate: Hello?');
  expect(fixture.results[0].meta).toMatchObject({ voiceSeconds: 30, usageConfirmed: false });
  expect(fixture.track.readyState).toBe('ended');
});

test('a hangup on a dead transport rescues immediately instead of waiting for an event that cannot arrive', async () => {
  const fixture = setup();
  const channel = await connected(fixture);
  channel.deliver({ type: 'session.output_transcript.delta', delta: 'Half a sentence' });
  await flush();
  channel.readyState = 'closed';
  channel.onclose?.();
  await flush();

  // The channel close alone is terminal: it is the documented "disconnected
  // without final session usage" case.
  expect(fixture.results).toHaveLength(1);
  expect(fixture.results[0].text).toContain('Delegate: Half a sentence');
  expect(fixture.results[0].meta.usageConfirmed).toBe(false);
});

// Finding 8: a session created after the owner gave up is billable and must
// still be closed.
test('hanging up while connecting still closes a session the server had already created', async () => {
  const gate = deferred();
  const fixture = setup({
    routes: {
      '/api/voice/live/session': async () => {
        await gate.promise;
        return jsonResponse({ live_session_id: 'live-9', sdp: 'ANSWER', owner_id: 'owner-1' });
      },
    },
  });
  const started = fixture.controller.start();
  await flush();

  const hangup = fixture.controller.hangup();
  await flush();
  // The POST was already in flight: the server will create the session anyway.
  gate.resolve();
  await flush();

  const channel = fixture.pc.channel();
  expect(channel.typesSent()).toContain('session.close');
  expect(fixture.results).toHaveLength(0);
  channel.deliver({ type: 'session.closed', reason: 'close_requested', usage: { seconds: 15 } });
  await hangup;
  expect(await started).toBe(false);
  expect(fixture.controller.state().phase).toBe('ended');
  expect(fixture.track.readyState).toBe('ended');
  // Giving up is not an error the owner needs a toast for.
  expect(fixture.errors).toEqual([]);
});

test('hanging up before the offer is posted never creates a session at all', async () => {
  const fixture = setup({ pcOptions: { iceGatheringState: 'gathering' } });
  const started = fixture.controller.start();
  await flush();
  const hangup = fixture.controller.hangup();
  await hangup;

  expect(await started).toBe(false);
  expect(fixture.requests).toHaveLength(0);
  expect(fixture.controller.state().phase).toBe('ended');
  expect(fixture.track.readyState).toBe('ended');
});

// Finding 3.
test('a session-level error ends the call and rescues the transcript instead of leaving the panel on a call', async () => {
  const fixture = setup();
  const channel = await connected(fixture);
  channel.deliver({ type: 'session.output_transcript.delta', delta: 'We agreed on Monday.' });
  channel.deliver({ type: 'error', error: { type: 'server_error', code: 'internal_error', message: 'the session failed' } });
  await flush();
  expect(fixture.errors.at(-1)).toContain('the session failed');
  // Not terminal until the silence proves it: an error may only cut speech.
  expect(fixture.controller.state().phase).toBe('live');

  await fixture.clock.advance(SESSION_ERROR_GRACE_MS);
  expect(fixture.controller.state().phase).toBe('closing');
  await fixture.clock.advance(CLOSE_BACKSTOP_MS);

  expect(fixture.controller.state().phase).toBe('ended');
  expect(fixture.results[0].text).toContain('the voice session failed');
  expect(fixture.results[0].text).toContain('Delegate: We agreed on Monday.');
  expect(fixture.track.readyState).toBe('ended');
});

test('a rejected command of ours does not end the call', async () => {
  const fixture = setup();
  const channel = await connected(fixture);
  // Provoke a real command so its event_id is one of ours.
  fixture.documentRef.hidden = true;
  fixture.controller.handleVisibility();
  const eventId = channel.sent.at(-1).event_id;

  channel.deliver({
    type: 'error',
    error: { type: 'invalid_request_error', code: 'immutable_field_update', message: 'rejected', client_event_id: eventId },
  });
  await fixture.clock.advance(SESSION_ERROR_GRACE_MS * 2);

  expect(fixture.errors.at(-1)).toBe('rejected');
  expect(fixture.controller.state().phase).toBe('live');
  expect(fixture.results).toHaveLength(0);
});

test('an event after a session error proves the session is alive and cancels the ending', async () => {
  const fixture = setup();
  const channel = await connected(fixture);
  channel.deliver({ type: 'error', error: { message: 'transient' } });
  await fixture.clock.advance(SESSION_ERROR_GRACE_MS / 2);
  channel.deliver({ type: 'session.output_transcript.delta', delta: 'Still here.' });
  await fixture.clock.advance(SESSION_ERROR_GRACE_MS * 2);

  expect(fixture.controller.state().phase).toBe('live');
  expect(fixture.results).toHaveLength(0);
});

test('a hidden page is never reported as a live microphone, and the call is muted while it lasts', async () => {
  const listeners = {};
  const documentRef = {
    hidden: false,
    addEventListener: (name, fn) => { listeners[name] = fn; },
    removeEventListener: () => {},
  };
  const fixture = setup({ document: documentRef });
  const channel = await connected(fixture);
  expect(fixture.controller.state().micState).toBe(MIC_LIVE);

  documentRef.hidden = true;
  listeners.visibilitychange();
  await flush();
  expect(fixture.controller.state().micState).toBe(MIC_UNKNOWN);
  expect(channel.typesSent()).toContain('session.input_audio.mute');

  documentRef.hidden = false;
  listeners.visibilitychange();
  await flush();
  expect(fixture.controller.state().micState).toBe(MIC_LIVE);
  expect(channel.typesSent()).toContain('session.input_audio.unmute');

  // A muted track is not live either, and absent events never promote it back.
  fixture.track.muted = true;
  fixture.track.onmute();
  expect(fixture.controller.state().micState).toBe(MIC_NOT_LIVE);
});

test('a microphone that stays not live ends the call by itself instead of burning voice seconds', async () => {
  const fixture = setup();
  const channel = await connected(fixture);
  fixture.track.muted = true;
  fixture.track.onmute();
  await fixture.clock.advance(MIC_GRACE_MS);
  expect(channel.typesSent()).toContain('session.close');
  await fixture.clock.advance(CLOSE_BACKSTOP_MS);
  expect(fixture.controller.state().phase).toBe('ended');
  expect(fixture.controller.state().endedReason).toBe('mic-lost');
});

// Finding 8: unmounting must not abandon a paid session.
test('dispose closes the session gracefully instead of abandoning it mid-call', async () => {
  const fixture = setup();
  const channel = await connected(fixture);
  fixture.controller.dispose();
  await flush();

  expect(channel.typesSent()).toContain('session.close');
  // Still draining: the session is being closed, not dropped on the floor.
  expect(fixture.track.readyState).toBe('live');
  channel.deliver({ type: 'session.closed', reason: 'close_requested', usage: { seconds: 11 } });
  await flush();
  expect(fixture.track.readyState).toBe('ended');
  // Nothing is pushed into an unmounted component.
  expect(fixture.results).toHaveLength(0);
});

// Point 3: the owner must never read the wire format. Each cause gets its own
// sentence, and every one of them says what to do next.
test('a server without an API key says so and says what to do, without echoing the body', async () => {
  const fixture = setup({
    routes: {
      '/api/voice/live/session': () => jsonResponse({ error: 'no OpenAI API key is configured', cause: 'no_api_key' }, 503),
    },
  });
  const ok = await fixture.controller.start();
  expect(ok).toBe(false);
  expect(fixture.errors[0]).toBe('Voice calls need an OpenAI API key on this server. Add one to its configuration, then try again.');
  expect(fixture.errors[0]).not.toContain('{');
  expect(fixture.errors[0]).not.toContain('cause');
  expect(fixture.controller.state().phase).toBe('ended');
  expect(fixture.track.readyState).toBe('ended');
});

test('a rate limited start reads as an instruction, not as JSON', async () => {
  const fixture = setup({
    routes: {
      '/api/voice/live/session': () => jsonResponse({ error: 'too many voice sessions started', cause: 'rate_limited' }, 429),
    },
  });
  expect(await fixture.controller.start()).toBe(false);
  expect(fixture.errors[0]).toBe('Could not start the call: too many voice sessions open. Wait a few seconds and try again.');
});

test('every documented cause has its own actionable sentence, and an unknown one still gets one', () => {
  const causes = [
    'rate_limited', 'upstream_busy', 'no_api_key', 'upstream_unreachable', 'upstream_refused',
    'upstream_unreadable', 'conversation_unavailable', 'unknown_session', 'device_revoked', 'bad_request',
  ];
  const messages = causes.map((cause) => callFailureMessage(503, cause));
  expect(new Set(messages).size).toBe(causes.length);
  for (const message of messages) {
    expect(message).not.toContain('{');
    expect(message.length).toBeGreaterThan(20);
  }
  // A cause this client does not know about must still produce a sentence,
  // never an empty toast or a status code on its own.
  expect(callFailureMessage(503, 'something_new')).toBe('The voice service is unavailable right now. Try again in a moment.');
  expect(callFailureMessage(429, '')).toContain('Wait a few seconds');
  expect(callFailureMessage(418, '')).toBe('Could not start the call. Reload the page and try again.');
});

// Point 4: the server keeps the session id, so it can close what the browser
// never will. The client's part is to say it is still there, and to say when
// it is done.
test('the call is heartbeated while it runs and released on the server when it ends', async () => {
  const fixture = setup();
  const channel = await connected(fixture);

  await fixture.clock.advance(HEARTBEAT_MS);
  const beats = fixture.requests.filter((request) => request.url === '/api/voice/live/heartbeat');
  expect(beats).toHaveLength(1);
  expect(beats[0].body).toEqual({ session_id: 'sess-1', live_session_id: 'live-1' });
  expect(beats[0].init.keepalive).toBe(true);
  await fixture.clock.advance(HEARTBEAT_MS);
  expect(fixture.requests.filter((request) => request.url === '/api/voice/live/heartbeat')).toHaveLength(2);

  const hangup = fixture.controller.hangup();
  await fixture.clock.advance(MINUTES_ON_HANGUP_MS);
  channel.deliver({ type: 'session.closed', reason: 'close_requested', usage: { seconds: 12 } });
  await hangup;
  await flush();

  const closes = fixture.requests.filter((request) => request.url === '/api/voice/live/close');
  expect(closes).toHaveLength(1);
  expect(closes[0].body).toEqual({ session_id: 'sess-1', live_session_id: 'live-1' });
  // Nothing keeps pinging a call that is over.
  await fixture.clock.advance(HEARTBEAT_MS * 3);
  expect(fixture.requests.filter((request) => request.url === '/api/voice/live/heartbeat')).toHaveLength(2);
});

test('a call the server no longer tracks stops being heartbeated', async () => {
  const fixture = setup({
    routes: {
      '/api/voice/live/heartbeat': () => jsonResponse({ error: 'that call is not tracked by this server', cause: 'unknown_call' }, 404),
    },
  });
  await connected(fixture);
  await fixture.clock.advance(HEARTBEAT_MS * 4);
  expect(fixture.requests.filter((request) => request.url === '/api/voice/live/heartbeat')).toHaveLength(1);
});

test('a lost transport still tells the server the call is over', async () => {
  const fixture = setup();
  const channel = await connected(fixture);
  channel.readyState = 'closed';
  channel.onclose?.();
  await flush();
  expect(fixture.requests.filter((request) => request.url === '/api/voice/live/close')).toHaveLength(1);
});

// The pending defect: the minutes used to depend on who hung up first.
test('a hangup before end_call asks the delegate for the minutes and delivers them', async () => {
  const fixture = setup();
  const channel = await connected(fixture);
  channel.deliver({ type: 'session.output_transcript.delta', delta: 'We agreed on the plan. ' });
  fixture.controller.attachRemoteAudio({ streams: [{ id: 'remote' }] });

  const hangup = fixture.controller.hangup();
  await flush();
  // Asked of the backend directly, not of the voice model: measured against
  // the live service, an instructions append never got the voice model to
  // delegate, while a queued message + response.create had end_call in ~2s.
  const request = channel.sent.find((message) => message.type === 'response.item.create');
  expect(request.item).toMatchObject({ type: 'message', role: 'user' });
  expect(request.item.content[0].text).toContain('end_call');
  expect(channel.typesSent()).toEqual(['response.item.create', 'response.create']);
  // The owner has left: whatever the voice model says now is not for him.
  expect(fixture.controller.audio.muted).toBe(true);
  // Still not closed: the delegate is being given its bounded moment.
  expect(channel.typesSent()).not.toContain('session.close');
  expect(fixture.controller.state().endedReason).toBe('hangup');

  channel.deliver({
    type: 'response.output_item.done',
    item: { type: 'function_call', call_id: 'c1', name: 'end_call', arguments: JSON.stringify({ minutes: 'Decided: ship it.' }) },
  });
  await flush();
  expect(channel.typesSent()).toContain('session.close');
  channel.deliver({ type: 'session.closed', reason: 'close_requested', usage: { seconds: 20 } });
  await hangup;

  expect(fixture.results[0].text).toBe('Decided: ship it.');
  expect(fixture.results[0].meta).toMatchObject({ minutes: true, reason: 'completed' });
});

test('the hang-up asks for minutes only once a backend round owing tool outputs has them', async () => {
  const gate = deferred();
  const fixture = setup({
    routes: {
      '/api/owners/owner-1/book': async () => { await gate.promise; return jsonResponse({ files: [] }); },
    },
  });
  const channel = await connected(fixture);
  const wrapped = (event) => channel.deliver({ type: 'response.event', delegation_id: 'item_1', event });
  wrapped({ type: 'response.created', response: { id: 'resp_1', output: [] } });
  wrapped({ type: 'response.output_item.done', item: { type: 'function_call', call_id: 'c1', name: 'book_list', arguments: '{}' } });
  wrapped({ type: 'response.completed', response: { id: 'resp_1', output: [] } });

  const hangup = fixture.controller.hangup();
  await flush();
  // The request is queued, but resuming now would be rejected for c1.
  expect(channel.typesSent()).toEqual(['response.item.create']);

  gate.resolve();
  await flush();
  expect(channel.typesSent()).toEqual(['response.item.create', 'response.item.create', 'response.create']);

  await fixture.clock.advance(MINUTES_ON_HANGUP_MS);
  channel.deliver({ type: 'session.closed', reason: 'close_requested', usage: { seconds: 20 } });
  await hangup;
});

test('duplicate end_call events keep the first minutes and close only once', async () => {
  const fixture = setup();
  const channel = await connected(fixture);
  const hangup = fixture.controller.hangup();
  await flush();

  for (const [callId, minutes] of [['first', 'Decided: first answer.'], ['second', 'Decided: second answer.']]) {
    channel.deliver({
      type: 'response.output_item.done',
      item: { type: 'function_call', call_id: callId, name: 'end_call', arguments: JSON.stringify({ minutes }) },
    });
  }
  await flush();
  expect(channel.typesSent().filter((type) => type === 'session.close')).toHaveLength(1);
  channel.deliver({ type: 'session.closed', reason: 'close_requested', usage: { seconds: 20 } });
  await hangup;

  expect(fixture.results).toHaveLength(1);
  expect(fixture.results[0].text).toBe('Decided: first answer.');
});

test('a delegate that never answers the hangup still yields the transcript, within the bound', async () => {
  const fixture = setup();
  const channel = await connected(fixture);
  channel.deliver({ type: 'session.output_transcript.delta', delta: 'Half a plan.' });

  const hangup = fixture.controller.hangup();
  await fixture.clock.advance(MINUTES_ON_HANGUP_MS);
  expect(channel.typesSent()).toContain('session.close');
  channel.deliver({ type: 'session.closed', reason: 'close_requested', usage: { seconds: 20 } });
  await hangup;

  expect(fixture.results[0].text).toContain('Delegate: Half a plan.');
  expect(fixture.results[0].meta.minutes).toBe(false);
});

// The wait belongs to the owner's own hangup and to nothing else: a call that
// ends because the microphone or the connection died has nobody to ask.
test('an ending that is not a hangup closes immediately, without asking for minutes', async () => {
  const fixture = setup();
  const channel = await connected(fixture);
  fixture.track.muted = true;
  fixture.track.onmute();
  await fixture.clock.advance(MIC_GRACE_MS);
  expect(channel.typesSent()).toContain('session.close');
  expect(channel.typesSent()).not.toContain('response.item.create');
});

// Finding 5.
test('a second call starts clean instead of inheriting the first call s minutes and counters', async () => {
  const fixture = setup({
    routes: {
      '/api/voice/live/ask': (url, init) => (init?.method === 'POST' ? jsonResponse({ ask_id: 'a' }) : jsonResponse({ status: 'pending' })),
    },
  });
  const first = await connected(fixture);
  for (let i = 0; i < MAX_QUESTIONS; i++) {
    first.deliver({
      type: 'response.output_item.done',
      item: { type: 'function_call', call_id: `k${i}`, name: 'ask_session', arguments: `{"question":"k${i}"}` },
    });
    await flush();
  }
  first.deliver({
    type: 'response.output_item.done',
    item: { type: 'function_call', call_id: 'k9', name: 'end_call', arguments: JSON.stringify({ minutes: 'First call minutes.' }) },
  });
  await flush();
  first.deliver({ type: 'session.closed', reason: 'close_requested', usage: { seconds: 60 } });
  await flush();
  expect(fixture.results).toHaveLength(1);

  // A second call on the same controller: nothing from the first may survive.
  const second = await connected(fixture);
  expect(fixture.controller.state()).toMatchObject({
    questionsUsed: 0, pendingAsks: 0, voiceSeconds: 0, phase: 'live', endedReason: '',
  });
  second.deliver({ type: 'session.output_transcript.delta', delta: 'Second call.' });
  fixture.pc.setConnectionState('failed');
  await flush();

  expect(fixture.results).toHaveLength(2);
  expect(fixture.results[1].text).not.toContain('First call minutes.');
  expect(fixture.results[1].text).toContain('Second call.');
  expect(fixture.results[1].meta.voiceSeconds).toBe(0);
});

// Finding 4: the owner's own text is never destroyed by the call's result.
test('the call result is appended as its own block and never replaces the draft', () => {
  expect(appendCallResult('', 'Minutes')).toBe('Minutes');
  expect(appendCallResult('   ', 'Minutes')).toBe('Minutes');
  expect(appendCallResult('half a thought', 'Minutes')).toBe('half a thought\n\nMinutes');
  expect(appendCallResult('half a thought\n\n', 'Minutes')).toBe('half a thought\n\nMinutes');
  // Nothing to append must leave the draft exactly as it was.
  expect(appendCallResult('half a thought', '   ')).toBe('half a thought');
});

test('a thinking append stays under the token limit even when every character is expensive', () => {
  const latin = 'a'.repeat(5000);
  expect(truncateForTokens(latin).length).toBeLessThanOrEqual(THINKING_TOKEN_BUDGET * 2);
  const cjk = '需'.repeat(2000);
  const cut = truncateForTokens(cjk);
  // Three UTF-8 bytes per character: the character count has to shrink much
  // further than a naive character budget would allow.
  expect(new TextEncoder().encode(cut).length).toBeLessThanOrEqual(THINKING_TOKEN_BUDGET * 2);
  const emoji = '👩‍👩‍👧‍👦'.repeat(500);
  const cutEmoji = truncateForTokens(emoji);
  expect(new TextEncoder().encode(cutEmoji).length).toBeLessThanOrEqual(THINKING_TOKEN_BUDGET * 2);
  // Never split a surrogate pair: the result must survive a round trip.
  expect([...cutEmoji].every((char) => char.codePointAt(0) !== 0xfffd)).toBe(true);
  expect(truncateForTokens('short answer')).toBe('short answer');
});

// --- what a call cost -----------------------------------------------------
// The owner asked to see it. The rule he set is that an honest partial figure
// beats an invented total, so these pin the honesty, not just the arithmetic.

test('voice cost is metered duration at the published rate, with the billed floor', async () => {
  const fixture = setup();
  const channel = await connected(fixture);

  // The floor is charged on creation, so it applies before any usage event.
  expect(fixture.controller.state().cost.billedSeconds).toBe(15);

  channel.deliver({ type: 'session.usage.updated', usage: { seconds: 6 } });
  await flush();
  // 6 seconds of talking still cost the 15 the provider bills for creating the
  // session; showing $0.005 here would understate every short call.
  expect(fixture.controller.state().cost.billedSeconds).toBe(15);
  expect(fixture.controller.state().cost.voiceUSD).toBeCloseTo(0.0125, 6);

  channel.deliver({ type: 'session.usage.updated', usage: { seconds: 120 } });
  await flush();
  expect(fixture.controller.state().cost.voiceUSD).toBeCloseTo(0.1, 6);
});

test('the backend is named but never priced: a wrong number is worse than none', async () => {
  const fixture = setup();
  const channel = await connected(fixture);
  channel.deliver({ type: 'session.usage.updated', usage: { seconds: 60 } });
  await flush();

  // Even when a forwarded Responses event carries usage, no backend figure is
  // produced: input_tokens hides cache reads and long-context tiers that are
  // priced in core's table, so any arithmetic here would be confidently wrong.
  channel.deliver({
    type: 'response.event',
    delegation_id: 'item_1',
    event: { type: 'response.completed', response: { usage: { input_tokens: 10_000, output_tokens: 1_000 } } },
  });
  await flush();
  const cost = fixture.controller.state().cost;
  expect(cost.backendCounted).toBe(false);
  expect(cost.backendUSD).toBeUndefined();
  expect(cost.backendModel).toBe('gpt-5.6-terra');
});

test('with no pricing from the server no number is invented', async () => {
  const fixture = setup({
    routes: { '/api/voice/live/session': () => jsonResponse({ live_session_id: 'l', sdp: 'ANSWER', owner_id: 'owner-1' }) },
  });
  const channel = await connected(fixture);
  channel.deliver({ type: 'session.usage.updated', usage: { seconds: 90 } });
  await flush();
  expect(fixture.controller.state().cost.voiceUSD).toBe(null);
});

test('the closing notice separates what is billed from what is not, and hedges an unconfirmed figure', () => {
  const confirmed = callSpendNotice({
    usageConfirmed: true,
    cost: { voiceUSD: 0.12, billedSeconds: 144, backendCounted: false, backendModel: 'gpt-5.6-terra' },
  });
  expect(confirmed).toBe('2:24 of voice, $0.12. gpt-5.6-terra is billed separately and is not counted here.');

  const unconfirmed = callSpendNotice({
    usageConfirmed: false,
    cost: { voiceUSD: 0.12, billedSeconds: 144, backendCounted: false, backendModel: 'gpt-5.6-terra' },
  });
  expect(unconfirmed).toBe('2:24 of voice, about $0.12. gpt-5.6-terra is billed separately and is not counted here.');

  expect(callSpendNotice({ cost: null })).toBe('');
});
