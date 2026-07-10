import { test, expect } from 'bun:test';
import { applyOpsSnapshot, nextOpsReconnectDelay } from './ops-stream.js';

const snapshot = { projects: [] };

test('ops snapshots replace overview only when their version advances', () => {
  const initial = { sitrep: { spoken: 'rest' }, blockers: { blockers: [] }, overview: { projects: [{ canonical_cwd: '/old' }] }, streamVersion: 3 };
  expect(applyOpsSnapshot(initial, { type: 'snapshot', version: 3, snapshot })).toBe(initial);
  expect(applyOpsSnapshot(initial, { type: 'snapshot', version: 2, snapshot })).toBe(initial);

  expect(applyOpsSnapshot(initial, { type: 'snapshot', version: 4, snapshot })).toEqual({
    sitrep: initial.sitrep,
    blockers: initial.blockers,
    overview: snapshot,
    streamVersion: 4,
  });
});

test('ops reconnect delay is bounded', () => {
  expect(nextOpsReconnectDelay(1000)).toBe(2000);
  expect(nextOpsReconnectDelay(16000)).toBe(16000);
});
