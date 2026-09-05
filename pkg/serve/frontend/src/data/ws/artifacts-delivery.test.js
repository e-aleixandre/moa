// artifacts-delivery.test.js — the refresh signal: a successful send_file
// tool_end (foreground or delegated) refreshes an open drawer that belongs to
// that very conversation, without any new bus event. Run with `bun test`.

import { test, expect, beforeEach, afterEach } from 'bun:test';
import { store, setState } from '../store.js';
import { ARTIFACTS_CLOSED } from '../artifacts-model.js';
import { artifactsSlice, openArtifactsList } from '../artifacts.js';
import { handleWsToolEnd } from './tools.js';
import { handleWsSubagentEvent } from './subagents.js';

const originalFetch = globalThis.fetch;
const originalRaf = globalThis.requestAnimationFrame;
let calls;

function sendFileResult(sessionId, fileId) {
  return [
    `Sent report.md`,
    JSON.stringify({ file_id: fileId, name: 'report.md', size: 12, mime: 'text/markdown', url: `/api/sessions/${sessionId}/files/${fileId}` }),
  ].join('\n');
}

beforeEach(() => {
  calls = [];
  // The subagent path schedules a transcript flush on the next frame; the
  // refresh signal itself is synchronous, so a no-op frame keeps this focused.
  globalThis.requestAnimationFrame = () => 0;
  globalThis.fetch = (path) => {
    calls.push(path);
    return Promise.resolve(new Response(JSON.stringify({ artifacts: [] }), { status: 200 }));
  };
  setState({
    artifacts: ARTIFACTS_CLOSED,
    sessions: {
      A: { id: 'A', messages: [{ _type: 'tool_start', tool_call_id: 't1', tool_name: 'send_file', status: 'running' }], subagents: {} },
      B: { id: 'B', messages: [], subagents: {} },
    },
  });
});

afterEach(() => {
  globalThis.fetch = originalFetch;
  globalThis.requestAnimationFrame = originalRaf;
});

test('a successful send_file refreshes the open drawer of its own conversation', () => {
  openArtifactsList('A');
  calls.length = 0;
  handleWsToolEnd('A', { tool_call_id: 't1', tool_name: 'send_file', result: sendFileResult('A', 'f1') });
  expect(calls).toEqual(['/api/sessions/A/artifacts']);
  expect(artifactsSlice(store.get()).ownerSessionId).toBe('A');
});

test('a delivery in another conversation leaves the open drawer alone', () => {
  openArtifactsList('A');
  calls.length = 0;
  handleWsToolEnd('B', { tool_call_id: 't9', tool_name: 'send_file', result: sendFileResult('B', 'f2') });
  expect(calls).toEqual([]);
});

test('a failed or rejected send_file publishes nothing, so nothing refreshes', () => {
  openArtifactsList('A');
  calls.length = 0;
  handleWsToolEnd('A', { tool_call_id: 't1', tool_name: 'send_file', is_error: true, result: sendFileResult('A', 'f1') });
  handleWsToolEnd('A', { tool_call_id: 't1', tool_name: 'send_file', rejected: true, result: sendFileResult('A', 'f1') });
  expect(calls).toEqual([]);
});

test('another tool ending does not refresh the collection', () => {
  openArtifactsList('A');
  calls.length = 0;
  handleWsToolEnd('A', { tool_call_id: 't1', tool_name: 'bash', result: 'done' });
  expect(calls).toEqual([]);
});

test('a delegated send_file refreshes the parent conversation drawer', () => {
  setState({
    sessions: {
      ...store.get().sessions,
      A: { ...store.get().sessions.A, subagents: { j1: { jobId: 'j1', status: 'running', messages: [] } } },
    },
  });
  openArtifactsList('A');
  calls.length = 0;
  handleWsSubagentEvent('A', {
    job_id: 'j1',
    event: { type: 'tool_end', data: { tool_call_id: 'n1', tool_name: 'send_file', result: sendFileResult('A', 'f3') } },
  });
  expect(calls).toEqual(['/api/sessions/A/artifacts']);
});

test('a tool_end whose result is not a file descriptor refreshes nothing', () => {
  openArtifactsList('A');
  calls.length = 0;
  handleWsToolEnd('A', { tool_call_id: 't1', tool_name: 'send_file', result: 'no json here' });
  expect(calls).toEqual([]);
});
