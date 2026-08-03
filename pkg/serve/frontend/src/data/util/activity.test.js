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
  expect(activityAction(running('bash_wait', {}))).toBe('Waiting on a command');
  expect(activityAction(running('bash_status', {}))).toBe('Checking a command');
  expect(activityAction(running('subagent_wait', {}))).toBe('Waiting on a subagent');
  expect(activityAction(running('subagent_status', {}))).toBe('Checking a subagent');
  expect(activityAction(running('tasks', {}))).toBe('Updating tasks');
  expect(activityAction(running('memory', {}))).toBe('Consulting memory');
  expect(activityAction(running('verify', {}))).toBe('Running checks');
  expect(activityAction(running('moa_docs', {}))).toBe('Reading the docs');
  expect(activityAction(running('ask_user', {}))).toBe('Asking you');
  expect(activityAction(running('bash_cancel', {}))).toBe('Cancelling a command');
  expect(activityAction(running('subagent_cancel', {}))).toBe('Cancelling a subagent');
  expect(activityAction(running('mcp__playwright__browser_click', {}))).toBe('Using playwright');
});

test('activityAction pluralizes simultaneous subagent waits from running tool calls', () => {
  const session = running('subagent_wait', {});
  session.messages.unshift(
    { _type: 'tool_start', tool_name: 'subagent_wait', args: {}, status: 'running' },
    { _type: 'tool_start', tool_name: 'subagent_wait', args: {}, status: 'running' },
  );
  expect(activityAction(session)).toBe('Waiting on 3 subagents');
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

test('working verbs are distinct, single-word gerunds', () => {
  expect(new Set(WORKING_VERBS).size).toBe(WORKING_VERBS.length);
  for (const verb of WORKING_VERBS) expect(verb).toMatch(/^[A-Z][a-z]+$/);
});

test('workingVerb stays fixed through clock ticks within one episode', () => {
  const session = { state: 'running', runStartedAtMs: 10000, messages: [] };
  const text = activityText(session, 10000);
  expect(activityText(session, 14000)).toBe(text);
  expect(activityText(session, 130000)).toBe(text);
  expect(workingVerb({}, 10000)).toBe(WORKING_VERBS[0]);
  expect(workingVerb({ runStartedAtMs: 0 }, 10000)).toBe(WORKING_VERBS[0]);
});

test('workingVerb changes on a new tool episode without repeating the previous verb', () => {
  const session = { state: 'running', runStartedAtMs: 10000, messages: [] };
  const first = workingVerb(session);
  session.messages.push({ _type: 'tool_start', tool_name: 'read', status: 'done' });
  const second = workingVerb(session);
  expect(second).not.toBe(first);
  session.messages.push({ _type: 'tool_start', tool_name: 'edit', status: 'done' });
  expect(workingVerb(session)).not.toBe(second);
});

test('different runs use different seeded episode sequences', () => {
  const sequence = (runStartedAtMs) => {
    const session = { runStartedAtMs, messages: [] };
    return Array.from({ length: 4 }, () => {
      const verb = workingVerb(session);
      session.messages.push({ _type: 'tool_start' });
      return verb;
    });
  };
  expect(sequence(10000)).not.toEqual(sequence(20000));
});

test('activityText follows the resolution order', () => {
  // idle → nothing
  expect(activityText({ state: 'idle' })).toBe(null);
  // working with a tool → the synthesized action, ellipsized (work in progress)
  expect(activityText(running('edit', {}))).toBe('Editing code…');
  expect(activityText(running('bash', { command: 'go test ./...' }))).toBe('Running tests…');
  // working between tools → a seeded verb anchored to the run and tool episode
  const betweenTools = { state: 'running', runStartedAtMs: 10000, messages: [] };
  expect(activityText(betweenTools, 18000)).toBe(`${workingVerb(betweenTools)}…`);
  // special phases keep fixed copy, ignoring any tool
  expect(activityText({ state: 'running', thinkingText: 'x' })).toBe('Thinking…');
  expect(activityText({ state: 'running', compacting: true })).toBe('Compacting context…');
  expect(activityText({ state: 'running', autoVerifying: true })).toBe('Running auto-verify…');
});

// The ellipsis is a PROGRESS claim, so it is attached only where something is
// actually happening. Waiting parks the run on a human: no motion, no ellipsis.
test('activityText withholds the ellipsis while waiting on the user', () => {
  expect(activityText({ state: 'permission' })).toBe('Waiting for you');
  expect(activityText({ state: 'running', pendingAsk: { id: 'a' } })).toBe('Waiting for you');
});

test('machine waits remain distinct from waiting for the user', () => {
  expect(activityText(running('bash_wait', {}))).toBe('Waiting on a command…');
  expect(activityText(running('subagent_wait', {}))).toBe('Waiting on a subagent…');
  expect(activityText({ state: 'permission' })).toBe('Waiting for you');
});

// The tables stay punctuation-free: the ellipsis is added by the presentation
// layer (activityText), never baked into the data.
test('the activity data tables carry no ellipsis', () => {
  for (const verb of WORKING_VERBS) expect(verb.endsWith('…')).toBe(false);
  expect(activityAction(running('grep', {}))).toBe('Searching the code');
  expect(activityLabel('thinking')).toBe('Thinking');
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

test('a verify names the repository when it targets another checkout', () => {
  // Multi-repo work: the interesting part is which checkout is being verified.
  expect(activityLabel('verifying', { verifyDir: '/home/me/dev/other-repo', verifyManual: true }))
    .toBe('Verifying other-repo');
  expect(activityLabel('verifying', { verifyDir: '/home/me/dev/other-repo/', verifyManual: true }))
    .toBe('Verifying other-repo');
});

test('a manual verify of the session directory is not called auto-verify', () => {
  expect(activityLabel('verifying', { verifyManual: true })).toBe('Verifying');
});

test('an automatic post-edit verify keeps its label', () => {
  expect(activityLabel('verifying', { verifyManual: false })).toBe('Running auto-verify');
  // No session at all (older callers) must not change behaviour.
  expect(activityLabel('verifying')).toBe('Running auto-verify');
});
