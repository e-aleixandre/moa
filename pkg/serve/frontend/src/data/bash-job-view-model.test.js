// bash-job-view-model.test.js — run with `bun test`
//
// Covers the BashJobView projection: command/cwd/status, the live output
// growing from the streaming buffer and swapping to the final result, the
// terminal outcome mapping, the stop-button gating (running vs cancelling vs
// terminal), the line cap, and the rebound (job gone / owned by a subagent).
import { test, expect } from 'bun:test';
import { bashJobView, MAX_LINES } from './bash-job-view-model.js';

// job builds the store shape ws-handlers keeps for a ROOT background bash
// (bashJobState in ws-handlers.js), which is what this projection reads.
const job = (over = {}) => ({
  jobId: 'b1', kind: 'bash', task: 'go test ./...', cwd: '/work',
  status: 'running', async: true,
  messages: [{
    _type: 'tool_start', tool_call_id: 'b1', tool_name: 'bash',
    args: { command: 'go test ./...', cwd: '/work' },
    status: 'running', result: null, streamingResult: null,
  }],
  ...over,
});

const sessionWith = (j) => ({ id: 's1', messages: [], subagents: { [j.jobId]: j } });

// ── rebound ────────────────────────────────────────────────────────────────
test('bashJobView returns null when the job no longer exists (rebound)', () => {
  expect(bashJobView({ id: 's1', messages: [], subagents: {} }, 'gone')).toBeNull();
});

test('bashJobView refuses a subagent id (only background bash has this view)', () => {
  const session = { id: 's1', messages: [], subagents: { sa1: { jobId: 'sa1', status: 'running', messages: [] } } };
  expect(bashJobView(session, 'sa1')).toBeNull();
});

test('bashJobView refuses a bash owned by a subagent (it lives in that transcript)', () => {
  const owned = job({ jobId: 'b2', ownerAgentId: 'sa1' });
  expect(bashJobView(sessionWith(owned), 'b2')).toBeNull();
});

// ── identity: the FULL command, not the dock's first line ──────────────────
test('the view carries the whole multi-line command and the cwd', () => {
  const cmd = 'set -e\ngo build ./...\ngo test ./...';
  const v = bashJobView(sessionWith(job({ task: cmd })), 'b1');
  expect(v.command).toBe(cmd);
  expect(v.cwd).toBe('/work');
  expect(v.status).toBe('running');
  expect(v.terminal).toBe(false);
  expect(v.outcome).toBeNull();
});

// ── live output ────────────────────────────────────────────────────────────
test('a live job reads its output from the streaming buffer', () => {
  const j = job();
  j.messages[0].streamingResult = 'compiling\nok\n';
  const v = bashJobView(sessionWith(j), 'b1');
  // The trailing newline must not become a phantom empty last line.
  expect(v.lines).toEqual(['compiling', 'ok']);
  expect(v.hiddenLines).toBe(0);
});

test('an ended job reads the final result once the streaming buffer is cleared', () => {
  const j = job({ status: 'completed' });
  j.messages[0] = { ...j.messages[0], status: 'done', result: 'PASS\n', streamingResult: null };
  const v = bashJobView(sessionWith(j), 'b1');
  expect(v.lines).toEqual(['PASS']);
  expect(v.output).toBe('PASS\n');
});

test('a job with no output yet projects zero lines rather than one empty one', () => {
  expect(bashJobView(sessionWith(job()), 'b1').lines).toEqual([]);
});

test('only the tail is projected past the line cap, and the rest is counted', () => {
  const j = job();
  const total = MAX_LINES + 25;
  j.messages[0].streamingResult = Array.from({ length: total }, (_, i) => `line ${i}`).join('\n');
  const v = bashJobView(sessionWith(j), 'b1');
  expect(v.lines.length).toBe(MAX_LINES);
  expect(v.hiddenLines).toBe(25);
  expect(v.lines[0]).toBe('line 25');
  expect(v.lines[MAX_LINES - 1]).toBe(`line ${total - 1}`);
});

// ── stop gating ────────────────────────────────────────────────────────────
test('only a running job can be stopped', () => {
  expect(bashJobView(sessionWith(job()), 'b1').canCancel).toBe(true);
});

test('a cancelling job is still live but no longer stoppable', () => {
  const v = bashJobView(sessionWith(job({ status: 'cancelling' })), 'b1');
  expect(v.canCancel).toBe(false);
  expect(v.cancelling).toBe(true);
  expect(v.terminal).toBe(false);
});

test('a terminal job offers no stop', () => {
  for (const status of ['completed', 'failed', 'cancelled']) {
    const v = bashJobView(sessionWith(job({ status })), 'b1');
    expect(v.canCancel).toBe(false);
    expect(v.terminal).toBe(true);
  }
});

// ── terminal outcome ───────────────────────────────────────────────────────
test('terminal statuses map to the same three outcomes as the subagent view', () => {
  const outcome = (status) => bashJobView(sessionWith(job({ status })), 'b1').outcome;
  expect(outcome('completed')).toBe('completed');
  expect(outcome('done')).toBe('completed');
  expect(outcome('failed')).toBe('failed');
  expect(outcome('error')).toBe('failed');
  expect(outcome('cancelled')).toBe('cancelled');
});

// ── container shapes ───────────────────────────────────────────────────────
test('bashJobView tolerates session.subagents as an array', () => {
  const session = { id: 's1', messages: [], subagents: [job()] };
  expect(bashJobView(session, 'b1').command).toBe('go test ./...');
});
