// steer-queued.test.js — a voice call's relay or question that waits behind a
// running turn shows in the conversation's queue as soon as the call passes it
// on, on every client, and becomes its call-block row when the turn reads it.
// Run with `bun test`.

import { test, expect } from 'bun:test';
import { store, setState } from '../store.js';
import { handleWsSteerQueued, handleWsSteer, handleWsSteersCanceled } from './stream.js';
import { mergeSteers } from './init.js';
import { projectStream } from '../stream-model.js';

const queued = { id: 'st-1', text: 'Passed on to this session: Rebase the branch', non_recallable: true };
const delivered = {
  id: 'st-1', msg_id: 'm-1', timestamp: 1792120628, text: '<voice_relay model_summary>…',
  custom: { source: 'voice_call', kind: 'relay', call_id: 'c1', delivery_id: 'c1/1', model_summary: 'Rebase the branch' },
};
const sess = () => store.get().sessions.s1;

test('a queued relay shows in the queue before the turn reads it', () => {
  setState({ sessions: { s1: { messages: [], pendingSteers: null } } });
  handleWsSteerQueued('s1', queued);
  expect(sess().pendingSteers).toEqual([{ id: 'st-1', text: queued.text, confirmed: true, non_recallable: true }]);
});

test('delivery retires the queued line and the relay becomes its block row', () => {
  setState({ sessions: { s1: { messages: [], pendingSteers: null } } });
  handleWsSteerQueued('s1', queued);
  handleWsSteer('s1', delivered);
  expect(sess().pendingSteers).toBeNull();
  const blocks = projectStream(sess());
  expect(blocks.map((b) => b.kind)).toEqual(['voice_call']);
  expect(blocks[0].exchanges[0]).toMatchObject({ voiceKind: 'relay', question: 'Rebase the branch' });
});

test('a delivery that overtakes its queued frame leaves no stale line', () => {
  setState({ sessions: { s1: { messages: [], pendingSteers: null } } });
  handleWsSteer('s1', delivered);
  handleWsSteerQueued('s1', queued);
  expect(sess().pendingSteers).toBeNull();
});

test('the same queued frame twice is one line', () => {
  setState({ sessions: { s1: { messages: [], pendingSteers: null } } });
  handleWsSteerQueued('s1', queued);
  handleWsSteerQueued('s1', queued);
  expect(sess().pendingSteers).toHaveLength(1);
});

test('a stopped queue clears the line and never hands the record back to the composer', () => {
  setState({ sessions: { s1: { messages: [], pendingSteers: null } }, composerDrops: {} });
  handleWsSteerQueued('s1', queued);
  handleWsSteersCanceled('s1', ['st-1'], 'op-not-ours');
  expect(sess().pendingSteers).toBeNull();
  expect(store.get().composerDrops.s1).toBeUndefined();
});

test('a reconnect snapshot keeps the line non-recallable', () => {
  expect(mergeSteers([{ id: 'st-1', text: queued.text, non_recallable: true }], null)).toEqual([
    { id: 'st-1', text: queued.text, command: false, images: 0, non_recallable: true, confirmed: true },
  ]);
});
