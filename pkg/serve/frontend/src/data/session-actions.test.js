// session-actions.test.js — run with `bun test`
//
// Verifies the session poll (loadSessions) preserves WS/live-only fields that
// the /api/sessions response doesn't carry — in particular the OpenAI usage
// percents, which have no poller to restore them (regression: they flickered
// away every poll tick).
import { test, expect, beforeEach, mock } from 'bun:test';

// Mock only the network call, keeping api.js's other exports intact (other
// modules transitively import syncConnections/reconnectAll/etc. from it).
let apiResponse = [];
const realApi = await import('./api.js');
mock.module('./api.js', () => ({
  ...realApi,
  api: async () => apiResponse,
}));

const { store, setState } = await import('./store.js');
const { loadSessions, openPersistedSubagent, openBashJob, sendMessage } = await import('./session-actions.js');
const { handleWsRunTokens, handleWsStateChange } = await import('./ws-handlers.js');

beforeEach(() => {
  setState({ sessions: {}, tileTree: null, activeSession: null });
});

test('loadSessions preserves OpenAI rate-limit percents across a poll', async () => {
  // Seed an existing OpenAI session carrying live-only usage percents.
  setState({
    sessions: {
      s1: {
        id: 's1', provider: 'openai', state: 'idle', subagents: {},
        rlFiveHourPct: 42, rlSevenDayPct: 55,
      },
    },
  });
  // The poll response knows nothing about the usage percents.
  apiResponse = [{ id: 's1', title: 'S1', state: 'idle', provider: 'openai', cwd: '/x' }];

  await loadSessions();

  const s1 = store.get().sessions.s1;
  expect(s1.rlFiveHourPct).toBe(42);
  expect(s1.rlSevenDayPct).toBe(55);
});

test('loadSessions leaves rate-limit percents undefined for a fresh session', async () => {
  apiResponse = [{ id: 's2', title: 'S2', state: 'idle', provider: 'openai', cwd: '/y' }];

  await loadSessions();

  const s2 = store.get().sessions.s2;
  expect(s2.rlFiveHourPct).toBeUndefined();
  expect(s2.rlSevenDayPct).toBeUndefined();
});

test('loadSessions adopts the server state for a visible-but-saved session (just resumed)', async () => {
  // Regression: tapping a saved session makes it visible (activeSession) while
  // still 'saved'. resumeSession POSTs /resume (server flips it to idle) then
  // polls. A saved session has NO WS socket, so the poll must own its state —
  // otherwise wsOwns kept 'saved', leaving the dot grey and the stream empty
  // (no WS ever opens) until the app was reopened.
  setState({
    sessions: { s1: { id: 's1', state: 'saved', provider: 'anthropic', subagents: {}, messages: [] } },
    activeSession: 's1',
    isMobile: true,
  });
  apiResponse = [{ id: 's1', title: 'S1', state: 'idle', provider: 'anthropic', cwd: '/x' }];

  // resumeSession's poll makes the (now non-saved) session connectable, so
  // syncConnections opens a socket — stub location so openWs doesn't throw in
  // the jsdom-less test runner.
  const savedLocation = globalThis.location;
  globalThis.location = { protocol: 'http:', host: 'localhost', search: '' };
  try {
    await loadSessions();
  } finally {
    if (savedLocation === undefined) delete globalThis.location;
    else globalThis.location = savedLocation;
    setState({ isMobile: false });
  }

  expect(store.get().sessions.s1.state).toBe('idle');
});

test('sendMessage mid-run records the image count on the optimistic steer chip', async () => {
  // A running session: the send becomes a steer, and the optimistic chip must
  // carry the number of attached images so the UI can badge it and warn on
  // pull-back/abort (base64 is not tracked locally, only the count).
  setState({ sessions: { s1: { id: 's1', state: 'running', subagents: {}, pendingSteers: null, messages: [] } } });
  apiResponse = { action: 'steer' };

  await sendMessage('s1', 'look at these', [
    { name: 'a.png', mime: 'image/png', data: 'AAAA', isImage: true },
    { name: 'b.png', mime: 'image/png', data: 'BBBB', isImage: true },
    { name: 'notes.txt', mime: 'text/plain', data: 'Q0M=', isImage: false },
  ]);

  const steers = store.get().sessions.s1.pendingSteers;
  expect(steers).toHaveLength(1);
  expect(steers[0].text).toBe('look at these');
  expect(steers[0].images).toBe(2);
  expect(steers[0].confirmed).toBe(true);
});

test('sendMessage mid-run without images omits the images field', async () => {
  setState({ sessions: { s1: { id: 's1', state: 'running', subagents: {}, pendingSteers: null, messages: [] } } });
  apiResponse = { action: 'steer' };

  await sendMessage('s1', 'just text', []);

  const steers = store.get().sessions.s1.pendingSteers;
  expect(steers).toHaveLength(1);
  expect(steers[0].images).toBeUndefined();
});

test('openPersistedSubagent restores newest-first transcripts to chronological order', async () => {
  setState({ sessions: { s1: { id: 's1', subagents: {} } } });
  apiResponse = {
    order: 'newest_first',
    task: 'inspect ordering',
    messages: [
      { id: 'newest-tool', role: 'tool', tool: 'bash', status: 'ok', target: '{"command":"go test"}' },
      { id: 'older-user', role: 'user', text: 'run the tests' },
    ],
  };

  await openPersistedSubagent('s1', 'job-1');

  const messages = store.get().sessions.s1.subagents['job-1'].messages;
  expect(messages[0]).toMatchObject({ role: 'user', _msg_id: 'older-user' });
  expect(messages[1]).toMatchObject({ _type: 'tool_start', tool_call_id: 'newest-tool' });
});

test('loadSessions preserves the live per-run token tally across a poll', async () => {
  // A run finished with a token tally; the poll (which changes the title, e.g.
  // a fresh brief) must not drop the live-only counts.
  setState({
    sessions: {
      s1: {
        id: 's1', state: 'idle', subagents: {},
        runTokensUp: 41200, runTokensDown: 8700, runStartedAtMs: 123,
      },
    },
  });
  apiResponse = [{ id: 's1', title: 'A new title', state: 'idle', cwd: '/x' }];

  await loadSessions();

  const s1 = store.get().sessions.s1;
  expect(s1.title).toBe('A new title'); // the poll did replace the object
  expect(s1.runTokensUp).toBe(41200);
  expect(s1.runTokensDown).toBe(8700);
});

test('sendMessage from idle resets the token tally to start the new run at zero', async () => {
  // The previous run's totals persist at idle; sending a new message begins a
  // fresh run and must zero the tally optimistically (the WS state_change reset
  // can't fire — this patch already made the session running).
  setState({
    sessions: {
      s1: {
        id: 's1', state: 'idle', subagents: {}, pendingSteers: null, messages: [],
        runTokensUp: 41200, runTokensDown: 8700,
      },
    },
  });
  apiResponse = { action: 'send' };

  await sendMessage('s1', 'next task', []);

  const s1 = store.get().sessions.s1;
  expect(s1.state).toBe('running');
  expect(s1.runTokensUp).toBe(0);
  expect(s1.runTokensDown).toBe(0);
});

test('sendMessage replaces an accepted optimistic image with its durable descriptor', async () => {
  setState({
    sessions: {
      s1: { id: 's1', state: 'idle', subagents: {}, pendingSteers: null, messages: [] },
    },
  });
  apiResponse = {
    action: 'send',
    attachments: [{ id: 'att_x', mime: 'image/png', size: 123, name: 'a.png', url: '/api/sessions/s1/attachments/att_x' }],
  };

  await sendMessage('s1', 'look at this', [
    { name: 'a.png', mime: 'image/png', data: 'AAAA', isImage: true },
  ]);

  const content = store.get().sessions.s1.messages[0].content;
  expect(content[0]).toEqual({
    type: 'image', attachment_id: 'att_x', attachment_size: 123, mime_type: 'image/png', filename: 'a.png',
  });
  expect(content[0].data).toBeUndefined();
  expect(content[1]).toEqual({ type: 'text', text: 'look at this' });
});

test('sendMessage replaces an optimistic file chip with its durable document descriptor', async () => {
  setState({
    sessions: {
      s1: { id: 's1', state: 'idle', subagents: {}, pendingSteers: null, messages: [] },
    },
  });
  apiResponse = {
    action: 'send',
    attachments: [{ id: 'att_file', kind: 'file', mime: 'text/csv', size: 12, name: 'report.csv', url: '/api/sessions/s1/attachments/att_file' }],
  };

  await sendMessage('s1', 'review this', [
    { name: 'report.csv', mime: 'text/csv', data: 'YSxiCjEsMgo=', isImage: false },
  ]);

  expect(store.get().sessions.s1.messages[0].content[0]).toEqual({
    type: 'document', attachment_id: 'att_file', attachment_size: 12, mime_type: 'text/csv', filename: 'report.csv',
  });
});

test('sendMessage swaps mixed image and document attachments in their original order', async () => {
  setState({
    sessions: {
      s1: { id: 's1', state: 'idle', subagents: {}, pendingSteers: null, messages: [] },
    },
  });
  apiResponse = {
    action: 'send',
    attachments: [
      { id: 'att_image', kind: 'image', mime: 'image/png', size: 123, name: 'screen.png', url: '/api/sessions/s1/attachments/att_image' },
      { id: 'att_file', kind: 'file', mime: 'application/pdf', size: 456, name: 'report.pdf', url: '/api/sessions/s1/attachments/att_file' },
    ],
  };

  await sendMessage('s1', 'review both', [
    { name: 'screen.png', mime: 'image/png', data: 'AAAA', isImage: true },
    { name: 'report.pdf', mime: 'application/pdf', data: 'BBBB', isImage: false },
  ]);

  const content = store.get().sessions.s1.messages[0].content;
  expect(content).toEqual([
    { type: 'image', attachment_id: 'att_image', attachment_size: 123, mime_type: 'image/png', filename: 'screen.png' },
    { type: 'document', attachment_id: 'att_file', attachment_size: 456, mime_type: 'application/pdf', filename: 'report.pdf' },
    { type: 'text', text: 'review both' },
  ]);
  expect(content[0].data).toBeUndefined();
  expect(content[1].data).toBeUndefined();
});

test('sendMessage keeps an optimistic image inline when the response has no attachments', async () => {
  setState({
    sessions: {
      s1: { id: 's1', state: 'idle', subagents: {}, pendingSteers: null, messages: [] },
    },
  });
  apiResponse = { action: 'send' };

  await sendMessage('s1', 'look at this', [
    { name: 'a.png', mime: 'image/png', data: 'AAAA', isImage: true },
  ]);

  expect(store.get().sessions.s1.messages[0].content[0]).toEqual({
    type: 'image', data: 'AAAA', mime_type: 'image/png',
  });
});

test('sendMessage keeps a non-image optimistic echo as a file chip, never inline text', async () => {
  setState({
    sessions: {
      s1: { id: 's1', state: 'idle', subagents: {}, pendingSteers: null, messages: [] },
    },
  });
  apiResponse = { action: 'send' };

  await sendMessage('s1', 'look at this', [
    { name: 'notes.txt', mime: 'text/plain', data: 'aGVsbG8=', isImage: false },
  ]);

  expect(store.get().sessions.s1.messages[0].content[0]).toEqual({
    type: 'document', mime_type: 'text/plain', filename: 'notes.txt',
  });
});

test('sendMessage keeps optimistic images inline when the descriptor count does not match', async () => {
  setState({
    sessions: {
      s1: { id: 's1', state: 'idle', subagents: {}, pendingSteers: null, messages: [] },
    },
  });
  // Two images sent (both classified as image client-side) but the server only
  // stored one durably (e.g. the first failed magic-byte validation and became
  // text/disk). Positional pairing would mis-assign the descriptor, so the swap
  // must not run — the optimistic echo stays inline until a reload reconciles.
  apiResponse = {
    action: 'send',
    attachments: [{ id: 'att_y', mime: 'image/png', size: 9, name: 'b.png', url: '/api/sessions/s1/attachments/att_y' }],
  };

  await sendMessage('s1', 'two', [
    { name: 'a.svg', mime: 'image/svg+xml', data: 'AAAA', isImage: true },
    { name: 'b.png', mime: 'image/png', data: 'BBBB', isImage: true },
  ]);

  const content = store.get().sessions.s1.messages[0].content;
  expect(content[0]).toEqual({ type: 'image', data: 'AAAA', mime_type: 'image/svg+xml' });
  expect(content[1]).toEqual({ type: 'image', data: 'BBBB', mime_type: 'image/png' });
});

test('sendMessage keeps mixed optimistic attachments intact when the descriptor count is partial', async () => {
  setState({
    sessions: {
      s1: { id: 's1', state: 'idle', subagents: {}, pendingSteers: null, messages: [] },
    },
  });
  apiResponse = {
    action: 'send',
    attachments: [{ id: 'att_image', kind: 'image', mime: 'image/png', size: 123, name: 'screen.png', url: '/api/sessions/s1/attachments/att_image' }],
  };

  await sendMessage('s1', 'review both', [
    { name: 'screen.png', mime: 'image/png', data: 'AAAA', isImage: true },
    { name: 'report.pdf', mime: 'application/pdf', data: 'BBBB', isImage: false },
  ]);

  const content = store.get().sessions.s1.messages[0].content;
  expect(content[0]).toEqual({ type: 'image', data: 'AAAA', mime_type: 'image/png' });
  expect(content[1]).toEqual({ type: 'document', mime_type: 'application/pdf', filename: 'report.pdf' });
  expect(content[1].attachment_id).toBeUndefined();
  expect(content[2]).toEqual({ type: 'text', text: 'review both' });
});

test('sendMessage adopts the server-minted msg_id when the server re-minted ours', async () => {
  // The server refuses a msg_id that is malformed or already used in this
  // session's history and mints a fresh one. Without adopting it, the
  // authoritative user_message broadcast would not dedup against our optimistic
  // echo and the message would render twice.
  setState({
    sessions: {
      s1: { id: 's1', state: 'idle', subagents: {}, pendingSteers: null, messages: [] },
    },
  });
  apiResponse = { action: 'send', msg_id: 'server-minted-1' };

  await sendMessage('s1', 'hola', []);

  const messages = store.get().sessions.s1.messages;
  expect(messages.length).toBe(1);
  expect(messages[0]._msg_id).toBe('server-minted-1');
});

test('sendMessage drops its optimistic echo when the broadcast already landed under the effective id', async () => {
  // The user_message broadcast can beat the /send response. It inserted the
  // message under the effective ID, so our echo is now the duplicate.
  setState({
    sessions: {
      s1: { id: 's1', state: 'idle', subagents: {}, pendingSteers: null, messages: [] },
    },
  });
  apiResponse = { action: 'send', msg_id: 'server-minted-2' };
  const { updateSession } = await import('./store.js');

  const pending = sendMessage('s1', 'hola', []);
  // Simulate the broadcast arriving before the response resolves.
  const cur = store.get().sessions.s1;
  updateSession('s1', {
    messages: [...cur.messages, { role: 'user', _msg_id: 'server-minted-2', content: [{ type: 'text', text: 'hola' }] }],
  });
  await pending;

  const messages = store.get().sessions.s1.messages;
  expect(messages.length).toBe(1);
  expect(messages[0]._msg_id).toBe('server-minted-2');
});

test('sendMessage keeps its own msg_id when the server honored it', async () => {
  setState({
    sessions: {
      s1: { id: 's1', state: 'idle', subagents: {}, pendingSteers: null, messages: [] },
    },
  });
  let sentMsgId = null;
  apiResponse = { action: 'send' };
  await sendMessage('s1', 'hola', []);
  sentMsgId = store.get().sessions.s1.messages[0]._msg_id;
  expect(sentMsgId).toBeTruthy();
  expect(sentMsgId.startsWith('c-')).toBe(true);
});

test('sendMessage turns its optimistic message into a chip when the server queued it', async () => {
  // Local state said idle, but a run (or a queued item) was there by the time
  // the server decided: the response says "steer". The optimistic message is
  // fiction — it must become the confirmed chip, not a phantom message that
  // disappears on reload.
  setState({
    sessions: {
      s1: {
        id: 's1', state: 'idle', subagents: {}, pendingSteers: null, messages: [],
        runTokensUp: 700, runTokensDown: 120, runStartedAtMs: 555,
      },
    },
  });
  apiResponse = { action: 'steer', steer_id: 'srv-steer-1' };

  const action = await sendMessage('s1', 'hola', []);

  const s1 = store.get().sessions.s1;
  expect(action).toBe('steer');
  expect(s1.messages).toHaveLength(0);
  expect(s1.pendingSteers).toHaveLength(1);
  expect(s1.pendingSteers[0]).toMatchObject({ id: 'srv-steer-1', text: 'hola', confirmed: true });
  // The optimistic "fresh run" patch is undone for the start time, but the
  // tally is left at 0/0: a run we didn't know about is in flight and 700/120
  // belongs to an older, finished run.
  expect(s1.runTokensUp).toBe(0);
  expect(s1.runTokensDown).toBe(0);
  expect(s1.runStartedAtMs).toBe(555);
});

test('sendMessage drops the queued chip when the steer was already delivered before the response', async () => {
  // The Steered event can beat the /send response: it moved the message into
  // the transcript, which is the authoritative removal — do not resurrect it.
  setState({
    sessions: {
      s1: { id: 's1', state: 'idle', subagents: {}, pendingSteers: null, messages: [] },
    },
  });
  apiResponse = { action: 'steer', steer_id: 'srv-steer-2' };
  const { updateSession } = await import('./store.js');

  const pending = sendMessage('s1', 'hola', []);
  const cur = store.get().sessions.s1;
  updateSession('s1', {
    messages: [
      ...cur.messages,
      { role: 'user', _msg_id: 'm-1', _steer_id: 'srv-steer-2', content: [{ type: 'text', text: 'hola' }] },
    ],
  });
  await pending;

  const s1 = store.get().sessions.s1;
  expect(s1.pendingSteers).toBeNull();
  expect(s1.messages).toHaveLength(1);
  expect(s1.messages[0]._steer_id).toBe('srv-steer-2');
});

test('sendMessage turns its optimistic chip into a message when the server started a run', async () => {
  // Local state said running, but the run had ended: the response says "send".
  // The chip is fiction — show the message under the effective msg_id instead.
  setState({
    sessions: {
      s1: { id: 's1', state: 'running', subagents: {}, pendingSteers: null, messages: [] },
    },
  });
  apiResponse = { action: 'send', msg_id: 'srv-msg-3' };

  const action = await sendMessage('s1', 'hola', []);

  const s1 = store.get().sessions.s1;
  expect(action).toBe('send');
  expect(s1.pendingSteers).toBeNull();
  expect(s1.messages).toHaveLength(1);
  expect(s1.messages[0]).toMatchObject({ role: 'user', _msg_id: 'srv-msg-3' });
  expect(s1.messages[0].content).toEqual([{ type: 'text', text: 'hola' }]);
});

test('sendMessage drops the chip without doubling the message when the broadcast won the race', async () => {
  setState({
    sessions: {
      s1: { id: 's1', state: 'running', subagents: {}, pendingSteers: null, messages: [] },
    },
  });
  apiResponse = { action: 'send', msg_id: 'srv-msg-4' };
  const { updateSession } = await import('./store.js');

  const pending = sendMessage('s1', 'hola', []);
  updateSession('s1', {
    messages: [{ role: 'user', _msg_id: 'srv-msg-4', content: [{ type: 'text', text: 'hola' }] }],
  });
  await pending;

  const s1 = store.get().sessions.s1;
  expect(s1.pendingSteers).toBeNull();
  expect(s1.messages).toHaveLength(1);
  expect(s1.messages[0]._msg_id).toBe('srv-msg-4');
});

test('sendMessage adopts durable descriptors when the chip prediction lost to a run', async () => {
  // We predicted a chip (local state said running) but the server started a run
  // and stored the attachments. The adopted message must carry the durable
  // descriptors: rebuilding local blocks would render "not available on this
  // device", and the user_message broadcast dedups by msg_id so it never
  // repairs them.
  setState({
    sessions: {
      s1: { id: 's1', state: 'running', subagents: {}, pendingSteers: null, messages: [] },
    },
  });
  apiResponse = {
    action: 'send',
    msg_id: 'srv-msg-5',
    attachments: [
      { id: 'att_image', kind: 'image', mime: 'image/png', size: 123, name: 'screen.png', url: '/api/sessions/s1/attachments/att_image' },
      { id: 'att_file', kind: 'file', mime: 'application/pdf', size: 456, name: 'report.pdf', url: '/api/sessions/s1/attachments/att_file' },
    ],
  };

  await sendMessage('s1', 'review both', [
    { name: 'screen.png', mime: 'image/png', data: 'AAAA', isImage: true },
    { name: 'report.pdf', mime: 'application/pdf', data: 'BBBB', isImage: false },
  ]);

  const s1 = store.get().sessions.s1;
  expect(s1.pendingSteers).toBeNull();
  expect(s1.messages).toHaveLength(1);
  expect(s1.messages[0].content).toEqual([
    { type: 'image', attachment_id: 'att_image', attachment_size: 123, mime_type: 'image/png', filename: 'screen.png' },
    { type: 'document', attachment_id: 'att_file', attachment_size: 456, mime_type: 'application/pdf', filename: 'report.pdf' },
    { type: 'text', text: 'review both' },
  ]);
});

test('sendMessage keeps the adopted chip-to-message blocks local when the descriptor count is partial', async () => {
  setState({
    sessions: {
      s1: { id: 's1', state: 'running', subagents: {}, pendingSteers: null, messages: [] },
    },
  });
  apiResponse = {
    action: 'send',
    msg_id: 'srv-msg-6',
    attachments: [{ id: 'att_image', kind: 'image', mime: 'image/png', size: 123, name: 'screen.png', url: '/api/sessions/s1/attachments/att_image' }],
  };

  await sendMessage('s1', 'review both', [
    { name: 'screen.png', mime: 'image/png', data: 'AAAA', isImage: true },
    { name: 'report.pdf', mime: 'application/pdf', data: 'BBBB', isImage: false },
  ]);

  const content = store.get().sessions.s1.messages[0].content;
  expect(content[0]).toEqual({ type: 'image', data: 'AAAA', mime_type: 'image/png' });
  expect(content[1]).toEqual({ type: 'document', mime_type: 'application/pdf', filename: 'report.pdf' });
});

test('sendMessage does not overwrite a live token tally when the server queued the message', async () => {
  // We predicted a run and zeroed the tally; the server queued instead. Real
  // run_tokens for the run actually in flight landed while the POST was open:
  // they must survive untouched.
  setState({
    sessions: {
      s1: {
        id: 's1', state: 'idle', subagents: {}, pendingSteers: null, messages: [],
        runTokensUp: 700, runTokensDown: 120, runStartedAtMs: 555,
      },
    },
  });
  apiResponse = { action: 'steer', steer_id: 'srv-steer-3' };

  const pending = sendMessage('s1', 'hola', []);
  handleWsRunTokens('s1', { up: 1500, down: 300 });
  await pending;

  const s1 = store.get().sessions.s1;
  expect(s1.runTokensUp).toBe(1500);
  expect(s1.runTokensDown).toBe(300);
  expect(s1.pendingSteers).toHaveLength(1);
});

test('sendMessage does not restore the previous run when a new run started during the POST', async () => {
  // The new run's counters are still 0/0, exactly what our optimistic patch
  // left behind: comparing values cannot tell them apart, so the runStartedAtMs
  // restore must key off "a WS event wrote the per-run fields" instead.
  setState({
    sessions: {
      s1: {
        id: 's1', state: 'idle', subagents: {}, pendingSteers: null, messages: [],
        runTokensUp: 700, runTokensDown: 120, runStartedAtMs: 555,
      },
    },
  });
  apiResponse = { action: 'steer', steer_id: 'srv-steer-4' };

  const pending = sendMessage('s1', 'hola', []);
  handleWsStateChange('s1', { state: 'running' });
  await pending;

  const s1 = store.get().sessions.s1;
  expect(s1.runTokensUp).toBe(0);
  expect(s1.runTokensDown).toBe(0);
  expect(s1.runStartedAtMs).not.toBe(555);
  expect(s1.pendingSteers).toHaveLength(1);
});

test('sendMessage leaves the tally to the run in flight when the response beats its state_change', async () => {
  // Worst case: another client already started a run, our snapshot still says
  // idle, and the POST resolves BEFORE that run's state_change frame arrives —
  // so the epoch guard sees no change. Restoring 700/120 here would show the
  // previous run's totals, and the late state_change wouldn't reset them
  // (it sees us already "running"). The tally must stay 0/0 until run_tokens.
  setState({
    sessions: {
      s1: {
        id: 's1', state: 'idle', subagents: {}, pendingSteers: null, messages: [],
        runTokensUp: 700, runTokensDown: 120, runStartedAtMs: 555,
      },
    },
  });
  apiResponse = { action: 'steer', steer_id: 'srv-steer-5' };

  await sendMessage('s1', 'hola', []);

  let s1 = store.get().sessions.s1;
  expect(s1.runTokensUp).toBe(0);
  expect(s1.runTokensDown).toBe(0);

  // The delayed frames for the run that was already in flight.
  handleWsStateChange('s1', { state: 'running' });
  handleWsRunTokens('s1', { up: 2400, down: 610 });

  s1 = store.get().sessions.s1;
  expect(s1.runTokensUp).toBe(2400);
  expect(s1.runTokensDown).toBe(610);
  expect(s1.pendingSteers).toHaveLength(1);
});

test('openBashJob opens a live background job and closes any subagent view', () => {
  setState({
    sessions: {
      s1: {
        id: 's1', subagents: { 'bash-1': { jobId: 'bash-1', kind: 'bash', status: 'running', messages: [] } },
        viewingSubagent: 'sa-1',
      },
    },
  });

  openBashJob('s1', 'bash-1');

  const s1 = store.get().sessions.s1;
  expect(s1.viewingBashJob).toBe('bash-1');
  // Only one thing is being looked at: opening the job leaves the subagent.
  expect(s1.viewingSubagent).toBeNull();
});

test('openBashJob is a no-op for a job the store never had (no disk fallback)', () => {
  setState({ sessions: { s1: { id: 's1', subagents: {} } } });

  openBashJob('s1', 'bash-gone');

  expect(store.get().sessions.s1.viewingBashJob).toBeUndefined();
});

test('loadSessions preserves the open bash job view across a poll', async () => {
  setState({ sessions: { s1: { id: 's1', state: 'idle', subagents: {}, viewingBashJob: 'bash-1' } } });
  apiResponse = [{ id: 's1', title: 'S1', state: 'idle', cwd: '/x' }];

  await loadSessions();

  expect(store.get().sessions.s1.viewingBashJob).toBe('bash-1');
});
