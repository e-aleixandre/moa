// composer-queue.test.js — run with `bun test`
//
// Covers the pure queue/steer decisions extracted from the Composer: the
// send-vs-enqueue gate, the recall/abort text merge, the dropped-image count,
// the chip summary, and by-id removal (WS reconciliation).
import { test, expect } from 'bun:test';
import {
  willEnqueue, combineQueueText, droppedImageCount, queueSummary, recallActivates, sendMayClear,
} from './composer-queue.js';

test('willEnqueue sends immediately when idle or errored', () => {
  expect(willEnqueue({ state: 'idle' })).toBe(false);
  expect(willEnqueue({ state: 'error' })).toBe(false);
});

test('willEnqueue enqueues when the agent is busy', () => {
  expect(willEnqueue({ state: 'running' })).toBe(true);
  expect(willEnqueue({ state: 'permission' })).toBe(true);
});

test('willEnqueue with no session never queues', () => {
  expect(willEnqueue(null)).toBe(false);
  expect(willEnqueue(undefined)).toBe(false);
});

test('combineQueueText joins queued texts after the current draft', () => {
  const steers = [{ id: 'a', text: 'first' }, { id: 'b', text: 'second' }];
  expect(combineQueueText('draft', steers)).toBe('draft\nfirst\nsecond');
});

test('combineQueueText with an empty draft omits the leading newline', () => {
  const steers = [{ id: 'a', text: 'only' }];
  expect(combineQueueText('', steers)).toBe('only');
});

test('combineQueueText with an empty queue keeps the draft untouched', () => {
  expect(combineQueueText('draft', [])).toBe('draft');
  expect(combineQueueText('draft', null)).toBe('draft');
});

test('combineQueueText keeps command chips full /command text', () => {
  const steers = [{ id: 'a', text: '/compact', command: true }];
  expect(combineQueueText('', steers)).toBe('/compact');
});

test('droppedImageCount sums images across chips', () => {
  const steers = [{ id: 'a', text: 'x', images: 2 }, { id: 'b', text: 'y' }, { id: 'c', text: 'z', images: 1 }];
  expect(droppedImageCount(steers)).toBe(3);
});

test('droppedImageCount is 0 for a text-only queue', () => {
  expect(droppedImageCount([{ id: 'a', text: 'x' }])).toBe(0);
  expect(droppedImageCount(null)).toBe(0);
});

test('queueSummary reports count and the last chip', () => {
  const steers = [{ id: 'a', text: 'first' }, { id: 'b', text: 'second' }];
  expect(queueSummary(steers)).toEqual({ count: 2, lastText: 'second', lastIsCommand: false, lastImages: 0 });
});

test('queueSummary strips the leading slash for a command chip', () => {
  const steers = [{ id: 'a', text: '/verify', command: true }];
  expect(queueSummary(steers)).toEqual({ count: 1, lastText: 'verify', lastIsCommand: true, lastImages: 0 });
});

test('queueSummary returns null for an empty queue', () => {
  expect(queueSummary([])).toBeNull();
  expect(queueSummary(null)).toBeNull();
});

// The chip renders where the send button was just tapped, so the click that
// completes that tap is delivered to a control that did not exist when the
// gesture started. Honouring it cancelled the message the user had just sent.
test('recallActivates rejects a click whose pointerdown never touched the chip', () => {
  expect(recallActivates({ pointerDownOnChip: false })).toBe(false);
  expect(recallActivates({})).toBe(false);
});

test('recallActivates honours a whole gesture made on the chip', () => {
  expect(recallActivates({ pointerDownOnChip: true })).toBe(true);
});

// Alt+↑ and Enter on a focused chip have no pointer at all; gating them on one
// would make the queue unreachable from the keyboard.
test('recallActivates always honours keyboard activation', () => {
  expect(recallActivates({ pointerDownOnChip: false, fromKeyboard: true })).toBe(true);
});

// The window between "send accepted" and "server answered" is where a recall
// can restore the very text being sent. Clearing then destroyed it.
test('sendMayClear lets an undisturbed send empty the composer', () => {
  expect(sendMayClear(3, 3)).toBe(true);
});

test('sendMayClear refuses to clear a composer written meanwhile', () => {
  expect(sendMayClear(3, 4)).toBe(false);
});
