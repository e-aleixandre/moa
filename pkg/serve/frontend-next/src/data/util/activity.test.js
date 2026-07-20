import { test, expect } from 'bun:test';
import {
  formatElapsed,
  activityPhase,
  activityLabel,
  activityAction,
  activityText,
  inFlightTool,
  liveVerb,
  WORKING_VERBS,
  workingVerb,
} from './activity.js';

// A session running a single tool. running() builds the minimal shape
// activityAction reads: the last tool_start message with status 'running'.
function running(tool_name, args) {
  return {
    state: 'running',
    messages: [
      { _type: 'user', text: 'hi' },
      { _type: 'tool_start', tool_name, args, status: 'running' },
    ],
  };
}

test('formatElapsed renders compact durations', () => {
  expect(formatElapsed(0)).toBe('0s');
  expect(formatElapsed(8000)).toBe('8s');
  expect(formatElapsed(59000)).toBe('59s');
  expect(formatElapsed(60000)).toBe('1m');
  expect(formatElapsed(63000)).toBe('1m03s');
  expect(formatElapsed(134000)).toBe('2m14s');
  expect(formatElapsed(3600000)).toBe('1h');
  expect(formatElapsed(3660000)).toBe('1h01m');
  expect(formatElapsed(-1)).toBe('');
});

test('activityPhase classifies coarse phases', () => {
  expect(activityPhase(null)).toBe(null);
  expect(activityPhase({ state: 'idle' })).toBe(null);
  expect(activityPhase({ state: 'running' })).toBe('working');
  expect(activityPhase({ state: 'running', thinkingText: 'hmm' })).toBe('thinking');
  expect(activityPhase({ state: 'permission' })).toBe('waiting');
  // ask_user keeps the run 'running' but blocks on the user via pendingAsk.
  expect(activityPhase({ state: 'running', pendingAsk: { id: 'a' } })).toBe('waiting');
  expect(activityPhase({ state: 'running', compacting: true })).toBe('compacting');
  expect(activityPhase({ state: 'running', autoVerifying: true })).toBe('verifying');
});

test('compacting/verifying take priority over run phase', () => {
  expect(activityPhase({ state: 'running', thinkingText: 'x', compacting: true })).toBe('compacting');
  expect(activityPhase({ state: 'running', autoVerifying: true })).toBe('verifying');
});

test('activityLabel maps phases to fixed copy', () => {
  expect(activityLabel('thinking')).toBe('Thinking');
  expect(activityLabel('waiting')).toBe('Waiting for you');
  expect(activityLabel('compacting')).toBe('Compacting context');
  expect(activityLabel('verifying')).toBe('Running auto-verify');
  expect(activityLabel('working')).toBe('Working');
  expect(activityLabel(null)).toBe(null);
});

test('inFlightTool finds the last running tool_start', () => {
  expect(inFlightTool(null)).toBe(null);
  expect(inFlightTool({ messages: [] })).toBe(null);
  expect(inFlightTool({ messages: [{ _type: 'tool_start', status: 'done' }] })).toBe(null);
  const s = {
    messages: [
      { _type: 'tool_start', tool_name: 'read', status: 'done' },
      { _type: 'tool_start', tool_name: 'bash', status: 'running' },
    ],
  };
  expect(inFlightTool(s).tool_name).toBe('bash');
});

test('activityAction maps non-bash tools to intent phrases', () => {
  expect(activityAction(running('read', { path: 'a.js' }))).toBe('Reading files');
  expect(activityAction(running('ls', {}))).toBe('Reading files');
  expect(activityAction(running('grep', { pattern: 'x' }))).toBe('Searching the code');
  expect(activityAction(running('find', {}))).toBe('Searching the code');
  expect(activityAction(running('write', { path: 'a' }))).toBe('Writing a file');
  expect(activityAction(running('edit', {}))).toBe('Editing code');
  expect(activityAction(running('multiedit', {}))).toBe('Editing code');
  expect(activityAction(running('fetch_content', {}))).toBe('Fetching a page');
  expect(activityAction(running('web_search', {}))).toBe('Searching the web');
  expect(activityAction(running('send_file', {}))).toBe('Sending a file');
  expect(activityAction(running('subagent', {}))).toBe('Running a subagent');
});

test('activityAction classifies bash commands into intents', () => {
  expect(activityAction(running('bash', { command: 'go test ./...' }))).toBe('Running tests');
  expect(activityAction(running('bash', { command: 'npm run test' }))).toBe('Running tests');
  expect(activityAction(running('bash', { command: 'pytest -q' }))).toBe('Running tests');
  expect(activityAction(running('bash', { command: 'go build ./...' }))).toBe('Building');
  expect(activityAction(running('bash', { command: 'make all' }))).toBe('Building');
  expect(activityAction(running('bash', { command: 'bun run build' }))).toBe('Building');
  expect(activityAction(running('bash', { command: 'go vet ./...' }))).toBe('Linting');
  expect(activityAction(running('bash', { command: 'eslint src' }))).toBe('Linting');
  expect(activityAction(running('bash', { command: 'go mod tidy' }))).toBe('Installing deps');
  expect(activityAction(running('bash', { command: 'npm install' }))).toBe('Installing deps');
  expect(activityAction(running('bash', { command: 'git commit -m x' }))).toBe('Committing');
  expect(activityAction(running('bash', { command: 'git push origin main' }))).toBe('Pushing');
  expect(activityAction(running('bash', { command: 'git status' }))).toBe('Running git');
  expect(activityAction(running('bash', { command: 'go run ./cmd' }))).toBe('Running the app');
  expect(activityAction(running('bash', { command: 'rg foo' }))).toBe('Inspecting files');
  expect(activityAction(running('bash', { command: 'echo hi && sleep 1' }))).toBe('Running a command');
});

test('activityAction only reads the first command line, lowercased', () => {
  expect(activityAction(running('bash', { command: 'GO TEST ./...\nrm -rf x' }))).toBe('Running tests');
});

test('activityAction accepts args as a JSON string', () => {
  expect(activityAction(running('bash', JSON.stringify({ command: 'go test ./...' })))).toBe('Running tests');
});

test('activityAction returns null with no in-flight tool', () => {
  expect(activityAction({ state: 'running', messages: [] })).toBe(null);
  expect(activityAction(null)).toBe(null);
});

test('workingVerb rotates deterministically from the run start', () => {
  const session = { runStartedAtMs: 10000 };
  expect(workingVerb(session, 10000)).toBe(WORKING_VERBS[0]);
  expect(workingVerb(session, 17999)).toBe(WORKING_VERBS[1]);
  expect(workingVerb(session, 18000)).toBe(WORKING_VERBS[2]);
  expect(workingVerb(session, 10000 + (WORKING_VERBS.length + 2) * 4000 + 1)).toBe(WORKING_VERBS[2]);
  expect(workingVerb(session, 9999)).toBe(WORKING_VERBS[0]);
  expect(workingVerb({}, 10000)).toBe(WORKING_VERBS[0]);
  expect(workingVerb({ runStartedAtMs: 0 }, 10000)).toBe(WORKING_VERBS[0]);
});

test('activityText follows the resolution order', () => {
  // idle → nothing
  expect(activityText({ state: 'idle' })).toBe(null);
  // working with a tool → the synthesized action
  expect(activityText(running('edit', {}))).toBe('Editing code');
  expect(activityText(running('bash', { command: 'go test ./...' }))).toBe('Running tests');
  // working between tools → rotating verb anchored to the run start
  expect(activityText({ state: 'running', runStartedAtMs: 10000, messages: [] }, 18000)).toBe('Noodling');
  // special phases keep fixed copy, ignoring any tool
  expect(activityText({ state: 'running', thinkingText: 'x' })).toBe('Thinking');
  expect(activityText({ state: 'permission' })).toBe('Waiting for you');
  expect(activityText({ state: 'running', compacting: true })).toBe('Compacting context');
  expect(activityText({ state: 'running', autoVerifying: true })).toBe('Running auto-verify');
});

// ── liveVerb (live-row verb table, RUNNING-TOOL-SPEC-FABLE.md §2) ──────────
test('liveVerb maps the closed tool table to present-continuous verbs', () => {
  expect(liveVerb('read')).toBe('Reading');
  expect(liveVerb('ls')).toBe('Reading');
  expect(liveVerb('bash')).toBe('Running');
  expect(liveVerb('grep')).toBe('Searching');
  expect(liveVerb('find')).toBe('Searching');
  expect(liveVerb('web_search')).toBe('Searching');
  expect(liveVerb('edit')).toBe('Editing');
  expect(liveVerb('multiedit')).toBe('Editing');
  expect(liveVerb('apply_patch')).toBe('Editing');
  expect(liveVerb('write')).toBe('Writing');
  expect(liveVerb('fetch_content')).toBe('Fetching');
  expect(liveVerb('subagent')).toBe('Delegating');
  expect(liveVerb('send_file')).toBe('Sending');
});

test('liveVerb falls back to "Calling" for unmapped/MCP tools', () => {
  expect(liveVerb('some_mcp_tool')).toBe('Calling');
  expect(liveVerb('')).toBe('Calling');
  expect(liveVerb(undefined)).toBe('Calling');
});

test('liveVerb is case-insensitive', () => {
  expect(liveVerb('Read')).toBe('Reading');
  expect(liveVerb('BASH')).toBe('Running');
});
