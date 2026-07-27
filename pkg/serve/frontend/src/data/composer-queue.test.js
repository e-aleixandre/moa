// composer-queue.test.js — run with `bun test`
//
// Covers the pure queue/steer decisions extracted from the Composer: the
// send-vs-enqueue gate, the recall/abort text merge, the dropped-image count,
// the chip summary, and by-id removal (WS reconciliation).
import { test, expect } from 'bun:test';
import {
  willEnqueue, combineQueueText, droppedImageCount, queueSummary,
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
