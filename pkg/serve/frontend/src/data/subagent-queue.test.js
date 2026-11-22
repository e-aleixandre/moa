// subagent-queue.test.js — a subagent's view shows its child's queue exactly
// as the session shows its own: the wrapped frames build it, the reconnect
// snapshot restores it, the two gestures act on the child's whole queue, and a
// recalled queue comes back to that subagent's steer box, not the parent's.
import { test, expect, beforeEach, afterEach } from 'bun:test';

const { store, setState } = await import('./store.js');
const { reduceSubagentQueue, subagentComposerKey } = await import('./subagent-queue.js');
const { handleWsInit } = await import('./ws/init.js');
const { handleWsSubagentEvent, handleWsSubagentEnd, flushSubagentEvents } = await import('./ws/subagents.js');
const { recallSubagentQueue, redirectSubagentQueue, steerSubagent } = await import('./session-actions.js');
const { __resetSteerRestoreForTests } = await import('./steer-restore.js');
const { getToasts, removeToast } = await import('./notifications.js');

let oldRAF;
beforeEach(() => {
  oldRAF = globalThis.requestAnimationFrame;
  globalThis.requestAnimationFrame = () => 1;
  __resetSteerRestoreForTests();
  setState({ sessions: { s1: { id: 's1', state: 'running', messages: [], subagents: {} } }, composerDrops: {}, activeSession: null });
  getToasts().forEach(({ id }) => removeToast(id));
});
afterEach(() => {
  if (oldRAF) globalThis.requestAnimationFrame = oldRAF;
  else delete globalThis.requestAnimationFrame;
});

const settle = () => new Promise((resolve) => setTimeout(resolve, 0));
const child = (pending_steers) => handleWsInit('s1', {
  state: 'running', messages: [],
  subagents: [{ job_id: 'J1', status: 'running', async: true, model: 'm', messages: [], pending_steers }],
});
const queue = () => store.get().sessions.s1.subagents.J1.pendingSteers;
const frame = (type, data) => {
  handleWsSubagentEvent('s1', { job_id: 'J1', event: { type, data } });
  flushSubagentEvents();
};

test('the reducer queues, marks, delivers and clears like the session queue', () => {
  let q = reduceSubagentQueue(null, { type: 'steer_queued', data: { id: 'a', text: 'one' } }, []);
  q = reduceSubagentQueue(q, { type: 'steer_queued', data: { id: 'b', text: 'two', redirect: true } }, []);
  expect(q).toEqual([{ id: 'a', text: 'one', confirmed: true }, { id: 'b', text: 'two', confirmed: true, redirect: true }]);
  // A duplicate, or a line whose delivery already reached the transcript, is not queued again.
  expect(reduceSubagentQueue(q, { type: 'steer_queued', data: { id: 'a', text: 'one' } }, [])).toBe(q);
  expect(reduceSubagentQueue(null, { type: 'steer_queued', data: { id: 'z', text: 'late' } }, [{ _steer_id: 'z' }])).toBe(null);
  q = reduceSubagentQueue(q, { type: 'steers_redirected', data: { steer_ids: ['a', 'b'] } }, []);
  expect(q.every((s) => s.redirect)).toBe(true);
  q = reduceSubagentQueue(q, { type: 'steer', data: { id: 'a' } }, []);
  expect(q.map((s) => s.id)).toEqual(['b']);
  expect(reduceSubagentQueue(q, { type: 'steers_canceled', data: { discarded_steer_ids: ['b'] } }, [])).toBe(null);
  expect(reduceSubagentQueue(q, { type: 'text_delta', data: {} }, [])).toBe(q);
});

test('the reconnect snapshot restores the child queue with its redirect mark', () => {
  child([{ id: 'a', text: 'one' }, { id: 'b', text: 'two', redirect: true }]);
  expect(queue().map((s) => [s.id, !!s.redirect])).toEqual([['a', false], ['b', true]]);
});

test('wrapped frames build the child queue and the delivering steer retires its line', () => {
  child(undefined);
  expect(queue()).toBe(null);
  frame('steer_queued', { id: 'a', text: 'from the owner' });
  frame('steer_queued', { id: 'b', text: 'from the parent' });
  expect(queue().map((s) => s.text)).toEqual(['from the owner', 'from the parent']);
  frame('steers_redirected', { steer_ids: ['a', 'b'] });
  expect(queue().every((s) => s.redirect)).toBe(true);
  frame('steer', { id: 'a', msg_id: 'm1', text: 'from the owner' });
  expect(queue().map((s) => s.id)).toEqual(['b']);
  // The delivered message is in the child's record.
  expect(store.get().sessions.s1.subagents.J1.messages.some((m) => m._steer_id === 'a')).toBe(true);
  // Another client's recall drops the lines without touching this client's box.
  frame('steers_canceled', { discarded_steer_ids: ['b'], recall_id: 'someone-else' });
  expect(queue()).toBe(null);
  expect(store.get().composerDrops[subagentComposerKey('s1', 'J1')]).toBeUndefined();
});

test('the marker does not outlive the child', () => {
  child([{ id: 'a', text: 'one' }]);
  handleWsSubagentEnd('s1', { job_id: 'J1', status: 'completed' });
  expect(queue()).toBe(null);
});

test('Bring back withdraws the child queue and puts it in that subagent box, not the parent\'s', async () => {
  child([{ id: 'a', text: 'one' }, { id: 'b', text: 'two' }]);
  const calls = [];
  globalThis.fetch = (path, opts) => {
    calls.push({ path, body: JSON.parse(opts.body) });
    return Promise.resolve(new Response(JSON.stringify({
      discarded_steer_ids: ['a', 'b'],
      discarded_steers: [{ id: 'a', text: 'one' }, { id: 'b', text: 'two' }],
    }), { status: 200 }));
  };
  expect(recallSubagentQueue('s1', 'J1')).toBe(true);
  // A second tap while the first is in flight does not post again.
  expect(recallSubagentQueue('s1', 'J1')).toBe(false);
  await settle();
  expect(calls).toHaveLength(1);
  expect(calls[0].path).toBe('/api/sessions/s1/subagents/J1/steers/cancel');
  expect(typeof calls[0].body.recall_id).toBe('string');
  expect(queue()).toBe(null);
  const drop = store.get().composerDrops[subagentComposerKey('s1', 'J1')];
  expect(drop.text).toContain('one');
  expect(drop.text).toContain('two');
  expect(drop.focus).toBe(true);
  expect(store.get().composerDrops.s1).toBeUndefined();
  // Its own broadcast arriving after the reply restores nothing twice.
  frame('steers_canceled', { discarded_steer_ids: ['a', 'b'], recall_id: calls[0].body.recall_id });
  expect(store.get().composerDrops[subagentComposerKey('s1', 'J1')]).toBe(drop);
});

test('Bring back restores from its own broadcast when that wins the race with the reply', async () => {
  child([{ id: 'a', text: 'one' }]);
  let reply;
  const calls = [];
  globalThis.fetch = (path, opts) => {
    calls.push(JSON.parse(opts.body));
    return new Promise((resolve) => { reply = () => resolve(new Response(JSON.stringify({ discarded_steer_ids: ['a'], discarded_steers: [{ id: 'a', text: 'one' }] }), { status: 200 })); });
  };
  recallSubagentQueue('s1', 'J1');
  frame('steers_canceled', { discarded_steer_ids: ['a'], recall_id: calls[0].recall_id });
  expect(queue()).toBe(null);
  const drop = store.get().composerDrops[subagentComposerKey('s1', 'J1')];
  expect(drop.text).toContain('one');
  reply();
  await settle();
  expect(store.get().composerDrops[subagentComposerKey('s1', 'J1')]).toBe(drop);
});

test('a failed Bring back keeps the queue and says so', async () => {
  child([{ id: 'a', text: 'one' }]);
  globalThis.fetch = () => Promise.resolve(new Response('boom', { status: 500 }));
  recallSubagentQueue('s1', 'J1');
  await settle();
  await settle();
  expect(queue().map((s) => s.id)).toEqual(['a']);
  expect(getToasts().some((t) => t.title === "Couldn't recall — try again")).toBe(true);
});

test('Redirect now marks the child queue at once and keeps the server\'s answer', async () => {
  child([{ id: 'a', text: 'one' }, { id: 'b', text: 'two' }]);
  const calls = [];
  let reply;
  globalThis.fetch = (path) => {
    calls.push(path);
    return new Promise((resolve) => { reply = () => resolve(new Response(JSON.stringify({ redirected_steer_ids: ['a'] }), { status: 200 })); });
  };
  expect(redirectSubagentQueue('s1', 'J1')).toBe(true);
  expect(queue().every((s) => s.redirect)).toBe(true);
  expect(redirectSubagentQueue('s1', 'J1')).toBe(false);
  expect(calls).toEqual(['/api/sessions/s1/subagents/J1/steers/redirect']);
  reply();
  await settle();
  // b reached the server after the mark (or was refused): it goes back to plain.
  expect(queue().map((s) => [s.id, !!s.redirect])).toEqual([['a', true], ['b', false]]);
});

test('a failed Redirect now undoes the optimistic mark', async () => {
  child([{ id: 'a', text: 'one' }]);
  globalThis.fetch = () => Promise.resolve(new Response('boom', { status: 500 }));
  redirectSubagentQueue('s1', 'J1');
  await settle();
  await settle();
  expect(queue()[0].redirect).toBeUndefined();
  expect(getToasts().some((t) => t.title === "Couldn't redirect — try again")).toBe(true);
});

test('steerSubagent sends redirect only when asked', async () => {
  const bodies = [];
  globalThis.fetch = (path, opts) => {
    bodies.push(JSON.parse(opts.body));
    return Promise.resolve(new Response(JSON.stringify({ queued: true }), { status: 200 }));
  };
  await steerSubagent('s1', 'J1', 'plain');
  await steerSubagent('s1', 'J1', 'urgent', { redirect: true });
  expect(bodies).toEqual([{ text: 'plain' }, { text: 'urgent', redirect: true }]);
});
