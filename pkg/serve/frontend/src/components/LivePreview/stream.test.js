import { test, expect } from 'bun:test';
import {
  streamEvents,
  stageState,
  reconcile,
  admit,
  gone,
  toolObject,
  toolText,
  TOOL_MS,
  TEXT_MS,
  EXIT_MS,
} from './stream.js';

// The preview covers the transcript, so the run has to be visible over the app —
// but as moments, not as a place. These cover WHAT floats (streamEvents) and HOW
// LONG it lives (reconcile); the markup — icon, color, the rise and the fade — is
// presentation keyed off `kind`.

// ── what floats ─────────────────────────────────────────────────────────────

test('an empty session floats nothing: idle leaves the app alone', () => {
  expect(streamEvents(null)).toEqual([]);
  expect(streamEvents({ messages: [] })).toEqual([]);
});

test('a running tool is one card, in the ledger\'s grammar and kind', () => {
  const session = {
    state: 'running',
    messages: [
      { _type: 'tool_start', tool_call_id: 't1', tool_name: 'edit', status: 'running', args: { path: 'src/style.css' } },
    ],
  };
  expect(streamEvents(session)).toEqual([
    { id: 'tool:t1', kind: 'tool', tool: 'edit', toolKind: 'edit', text: 'Editing style.css', running: true },
  ]);
});

test('a finished tool keeps its id and stops being live', () => {
  const done = streamEvents({
    messages: [
      { _type: 'tool_start', tool_call_id: 't1', tool_name: 'bash', status: 'done', args: { command: 'npm run lint' } },
    ],
  });
  expect(done).toEqual([
    { id: 'tool:t1', kind: 'tool', tool: 'bash', toolKind: 'bash', text: 'Running npm run lint', running: false },
  ]);
});

test('read and multiedit collapse to the kinds the ledger colors', () => {
  const one = (tool, args) => streamEvents({
    messages: [{ _type: 'tool_start', tool_call_id: 'x', tool_name: tool, status: 'running', args }],
  })[0];
  expect(one('read', { path: 'src/main.js' })).toMatchObject({ toolKind: 'read', text: 'Reading main.js' });
  expect(one('multiedit', { path: 'a/b/index.html' })).toMatchObject({ toolKind: 'edit', text: 'Editing index.html' });
  expect(one('grep', { pattern: 'foo' })).toMatchObject({ toolKind: 'read', text: 'Searching foo' });
});

test('assistant prose floats as its own card, raw markdown and uncut', () => {
  const text = 'Cambié `foo.css`:\n\n- una cosa\n- otra';
  const events = streamEvents({
    messages: [
      { role: 'assistant', content: [{ type: 'text', text: 'antes' }] },
      { _type: 'tool_start', tool_call_id: 't1', tool_name: 'edit', status: 'done', args: {} },
      { role: 'assistant', content: [{ type: 'text', text }] },
    ],
  });
  expect(events.map((e) => e.kind)).toEqual(['text', 'tool', 'text']);
  expect(events[2]).toEqual({ id: 'text:1', kind: 'text', text, streaming: false });
});

// The id is the contract: the overlay keeps a card alive across renders by id,
// so text still arriving must land on the SAME id it will have once the reducer
// materializes it into a message — otherwise the card is reborn on every delta.
test('streaming text keeps the id the materialized message will get', () => {
  const growing = streamEvents({
    state: 'running',
    messages: [{ role: 'assistant', content: [{ type: 'text', text: 'primero' }] }],
    streamingText: 'segu',
  });
  expect(growing[1]).toEqual({ id: 'text:1', kind: 'text', text: 'segu', streaming: true });

  const settled = streamEvents({
    messages: [
      { role: 'assistant', content: [{ type: 'text', text: 'primero' }] },
      { role: 'assistant', content: [{ type: 'text', text: 'segundo' }] },
    ],
  });
  expect(settled[1].id).toBe('text:1');
  expect(settled[1].streaming).toBe(false);
});

test('a user turn is not the agent talking', () => {
  expect(streamEvents({ messages: [{ role: 'user', content: 'hola' }] })).toEqual([]);
});

test('empty prose floats nothing', () => {
  expect(streamEvents({ messages: [{ role: 'assistant', content: [{ type: 'text', text: '  ' }] }] })).toEqual([]);
  expect(streamEvents({ state: 'running', messages: [], streamingText: '  ' })).toEqual([]);
});

test('waiting on a human is the newest event, with a stable id', () => {
  const events = streamEvents({
    state: 'running',
    pendingAsk: { id: 'a1' },
    messages: [{ role: 'assistant', content: [{ type: 'text', text: 'listo' }] }],
  });
  expect(events[events.length - 1]).toEqual({ id: 'waiting', kind: 'waiting', text: 'Waiting for you' });
  expect(streamEvents({ state: 'permission', messages: [] })).toEqual([
    { id: 'waiting', kind: 'waiting', text: 'Waiting for you' },
  ]);
});

test('only the tail is projected: this is a stream, not a transcript', () => {
  const messages = [];
  for (let i = 0; i < 30; i++) {
    messages.push({ _type: 'tool_start', tool_call_id: `t${i}`, tool_name: 'read', status: 'done', args: { path: `f${i}.js` } });
  }
  const events = streamEvents({ messages });
  expect(events).toHaveLength(8);
  expect(events[7].id).toBe('tool:t29');
});

// One line on a phone: a deep path is an ellipsis with a filename hidden in it,
// so paths show their leaf. Commands and queries are NOT paths and survive whole
// for CSS to clip.
test('paths shrink to their leaf, other objects stay whole', () => {
  expect(toolObject('read', { path: 'pkg/serve/frontend/src/style.css' })).toBe('style.css');
  expect(toolObject('write', { path: '/tmp/x/index.html' })).toBe('index.html');
  expect(toolObject('bash', { command: 'go build ./...' })).toBe('go build ./...');
  expect(toolObject('web_search', { query: 'preact signals' })).toBe('preact signals');
  expect(toolObject('read', {})).toBe('');
});

test('a tool with no usable object still says something honest', () => {
  expect(toolText('tasks', {})).toBe('Calling tasks');
  expect(toolText('read', {})).toBe('Reading…');
});

// ── the stage's edge ────────────────────────────────────────────────────────

test('the stage edge follows the tool in flight, and rests when idle', () => {
  expect(stageState(null)).toEqual({ mode: 'idle', kind: null });
  expect(stageState({ state: 'permission', messages: [] })).toEqual({ mode: 'waiting', kind: null });
  expect(stageState({
    state: 'running',
    messages: [{ _type: 'tool_start', tool_call_id: 't1', tool_name: 'write', status: 'running', args: {} }],
  })).toEqual({ mode: 'working', kind: 'edit' });
  expect(stageState({ state: 'running', messages: [] })).toEqual({ mode: 'working', kind: null });
});

// ── the life of a card ──────────────────────────────────────────────────────

const tool = (id, running = false) => ({ id: `tool:${id}`, kind: 'tool', toolKind: 'read', text: id, running });
const prose = (id, streaming = false) => ({ id: `text:${id}`, kind: 'text', text: 'hola', streaming });

test('a new event becomes a card that knows when it dies', () => {
  const [card] = reconcile([], [tool('a')], 1000);
  expect(card.id).toBe('tool:a');
  expect(card.at).toBe(1000);
  expect(card.expiresAt).toBe(1000 + TOOL_MS);
  expect(card.leaving).toBeUndefined();
});

test('a tool call appears and disappears on its own', () => {
  let cards = reconcile([], [tool('a')], 0);
  cards = reconcile(cards, [tool('a')], TOOL_MS - 1);
  expect(cards[0].leaving).toBeUndefined();
  cards = reconcile(cards, [tool('a')], TOOL_MS);
  expect(cards[0].leaving).toBe(true);
  // Still on screen while it animates out, gone once the animation is over.
  cards = reconcile(cards, [tool('a')], TOOL_MS + EXIT_MS - 1);
  expect(cards).toHaveLength(1);
  cards = reconcile(cards, [tool('a')], TOOL_MS + EXIT_MS);
  expect(cards).toEqual([]);
});

test('a card that is still happening does not expire', () => {
  let cards = reconcile([], [tool('a', true)], 0);
  expect(cards[0].expiresAt).toBe(null);
  cards = reconcile(cards, [tool('a', true)], 60_000);
  expect(cards[0].leaving).toBeUndefined();
  // It only starts counting once the tool ends.
  cards = reconcile(cards, [tool('a', false)], 60_000);
  expect(cards[0].expiresAt).toBe(60_000 + TOOL_MS);
});

test('streaming prose grows in the SAME card and dies after it settles', () => {
  let cards = reconcile([], [{ ...prose(0, true), text: 'Voy' }], 0);
  const first = cards[0];
  cards = reconcile(cards, [{ ...prose(0, true), text: 'Voy a cambiar' }], 500);
  expect(cards[0].id).toBe(first.id);
  expect(cards[0].at).toBe(0);            // never reborn: no re-entry animation
  expect(cards[0].text).toBe('Voy a cambiar');
  expect(cards[0].expiresAt).toBe(null);

  cards = reconcile(cards, [{ ...prose(0, false), text: 'Voy a cambiar esto' }], 900);
  expect(cards[0].expiresAt).toBe(900 + TEXT_MS);
  cards = reconcile(cards, [{ ...prose(0, false), text: 'Voy a cambiar esto' }], 900 + TEXT_MS);
  expect(cards[0].leaving).toBe(true);
});

test('a wait never expires — it is a question, not a moment', () => {
  const waiting = { id: 'waiting', kind: 'waiting', text: 'Waiting for you' };
  let cards = reconcile([], [waiting], 0);
  cards = reconcile(cards, [waiting], 10 * 60_000);
  expect(cards[0].leaving).toBeUndefined();
  expect(cards[0].expiresAt).toBe(null);
  // Answered: the event is gone from the model, so the card leaves normally.
  cards = reconcile(cards, [], 10 * 60_000);
  expect(cards[0].expiresAt).toBe(10 * 60_000 + TOOL_MS);
});

test('at most three float at once: the oldest go early', () => {
  let cards = reconcile([], [tool('a'), tool('b'), tool('c'), tool('d')], 0);
  const live = cards.filter((c) => !c.leaving).map((c) => c.id);
  expect(live).toEqual(['tool:b', 'tool:c', 'tool:d']);
  expect(cards.find((c) => c.id === 'tool:a').leaving).toBe(true);

  // ...and a burst does not knock the wait off screen.
  const waiting = { id: 'waiting', kind: 'waiting', text: 'Waiting for you' };
  cards = reconcile([], [waiting, tool('a'), tool('b'), tool('c')], 0);
  expect(cards.find((c) => c.id === 'waiting').leaving).toBeUndefined();
  expect(cards.find((c) => c.id === 'tool:a').leaving).toBe(true);
});

test('a leaving card is never resurrected by a late event', () => {
  let cards = reconcile([], [tool('a')], 0);
  cards = reconcile(cards, [tool('a')], TOOL_MS);
  expect(cards[0].leaving).toBe(true);
  cards = reconcile(cards, [tool('a', true)], TOOL_MS + 10);
  expect(cards[0].leaving).toBe(true);
});

// ── opening on a run already in progress ────────────────────────────────────

test('opening the preview does not replay the transcript', () => {
  const events = [tool('a'), prose(0), tool('b', true)];
  const muted = new Set(['tool:a', 'text:0']);
  expect(admit(events, muted).map((e) => e.id)).toEqual(['tool:b']);
});

test('...but a run already parked on a human is shown immediately', () => {
  const waiting = { id: 'waiting', kind: 'waiting', text: 'Waiting for you' };
  expect(admit([tool('a'), waiting], new Set(['tool:a', 'waiting']))).toEqual([waiting]);
});

// streamEvents projects the TRANSCRIPT, so a finished tool keeps being reported
// long after its card is gone. Without muting what left, the same moment would
// rise, fade and rise again forever — which is exactly what a first run of this
// did on screen: a four-card carousel looping over the app.
test('a card that finished its life is not floated again', () => {
  const events = [tool('a'), tool('b')];
  const shown = reconcile([], events, 0);
  const leaving = reconcile(shown, events, TOOL_MS);
  const empty = reconcile(leaving, events, TOOL_MS + EXIT_MS);
  expect(empty).toEqual([]);

  const muted = new Set(gone(shown, empty));
  expect([...muted].sort()).toEqual(['tool:a', 'tool:b']);
  expect(admit(events, muted)).toEqual([]);
});

test('gone names exactly what left the screen', () => {
  const before = [{ id: 'a' }, { id: 'b' }, { id: 'c' }];
  expect(gone(before, [{ id: 'b' }])).toEqual(['a', 'c']);
  expect(gone(before, before)).toEqual([]);
});
