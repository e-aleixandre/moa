// command-policy.test.js — run with `bun test`
// Mirror of pkg/bus/queue_policy_test.go: the client classifier must stay in
// lockstep with the server's ClassifyCommand table.
import { test, expect } from 'bun:test';
import { classifyCommand, POLICY_INSTANT, POLICY_QUEUE, POLICY_REJECT } from './command-policy.js';

test('queue commands (rewrite/reconfigure the run) wait for idle', () => {
  for (const raw of ['/compact', '/prepare-compact', '/clear', '/model sonnet', '/thinking high', '/verify']) {
    expect(classifyCommand(raw)).toBe(POLICY_QUEUE);
  }
});

test('reject commands (mode/rewind) are refused while busy', () => {
  for (const raw of ['/undo', '/branch', '/back', '/plan']) {
    expect(classifyCommand(raw)).toBe(POLICY_REJECT);
  }
});

test('side-state commands run instantly', () => {
  for (const raw of ['/rename foo', '/permissions', '/path list', '/tasks', '/schedule list', '/whoknows']) {
    expect(classifyCommand(raw)).toBe(POLICY_INSTANT);
  }
});

test('goal is argument-dependent', () => {
  expect(classifyCommand('/goal')).toBe(POLICY_INSTANT);
  expect(classifyCommand('/goal status')).toBe(POLICY_INSTANT);
  expect(classifyCommand('/goal stop')).toBe(POLICY_INSTANT);
  expect(classifyCommand('/goal ship the thing')).toBe(POLICY_QUEUE);
  expect(classifyCommand('/goal start')).toBe(POLICY_QUEUE);
});

test('leading slash is optional and case-insensitive; empty is instant', () => {
  expect(classifyCommand('compact')).toBe(POLICY_QUEUE);
  expect(classifyCommand('/COMPACT')).toBe(POLICY_QUEUE);
  expect(classifyCommand('')).toBe(POLICY_INSTANT);
  expect(classifyCommand('/')).toBe(POLICY_INSTANT);
});
