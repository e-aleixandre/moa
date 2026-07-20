// subagent-view-model.test.js — run with `bun test`
//
// Covers the SubagentView projection: identity accent, sibling rail (only for a
// real fanout), terminal outcome mapping + error/result extraction, live now-
// line segments, the rebound (pruned job → null), and canPromote gating. Plus
// the AgentTray's liveTrayAgents (bash without identity accent — INC-22).
import { test, expect } from 'bun:test';
import { subagentView, canPromote } from './subagent-view-model.js';
import { liveTrayAgents } from './stream-model.js';

const sub = (over = {}) => ({
  jobId: 'j1', task: 'Investigate the flow', model: 'openai/GPT-5 Sol',
  status: 'running', async: false, messages: [], ...over,
});

// ── rebound ──────────────────────────────────────────────────────────────
test('subagentView returns null when the job no longer exists (rebound)', () => {
  const session = { id: 's1', messages: [], subagents: {} };
  expect(subagentView(session, 'gone')).toBeNull();
});

// ── identity accent + task card ────────────────────────────────────────────
test('a lone live subagent gets the first fanout accent and no siblings', () => {
  const session = { id: 's1', messages: [], subagents: { j1: sub() } };
  const v = subagentView(session, 'j1');
  expect(v.accent).toBe('sky');
  expect(v.siblings).toEqual([]);
  expect(v.name).toBe('Investigate the flow');
  expect(v.terminal).toBe(false);
});

// ── model label is the short alias, not the raw id ─────────────────────────
test('the model label is the short codename alias (never the full provider id)', () => {
  const session = { id: 's1', messages: [], subagents: { j1: sub({ model: 'openai/GPT-5.6 Terra' }) } };
  // matches the parent header (modelCodename): "Terra", not "GPT-5.6 Terra".
  expect(subagentView(session, 'j1').model).toBe('Terra');
});

test('the model label falls back to the provider-stripped id when there is no known codename', () => {
  const session = { id: 's1', messages: [], subagents: { j1: sub({ model: 'openai/GPT-5.5' }) } };
  expect(subagentView(session, 'j1').model).toBe('GPT-5.5');
});

// ── sibling rail only for 2+ live ─────────────────────────────────────────
test('two live subagents produce a sibling rail with the active one flagged', () => {
  const session = {
    id: 's1', messages: [],
    subagents: {
      j1: sub({ jobId: 'j1' }),
      j2: sub({ jobId: 'j2', task: 'Write the docs' }),
    },
  };
  const v = subagentView(session, 'j2');
  expect(v.siblings).toHaveLength(2);
  expect(v.siblings.map((s) => s.id)).toEqual(['j1', 'j2']);
  const active = v.siblings.find((s) => s.active);
  expect(active.id).toBe('j2');
  // Each sibling carries its own identity accent (index-cycled).
  expect(v.siblings[0].accent).toBe('sky');
  expect(v.siblings[1].accent).toBe('teal');
  // The viewed subagent's accent matches its live index.
  expect(v.accent).toBe('teal');
});

// ── terminal outcomes ─────────────────────────────────────────────────────
test('a completed subagent maps to the completed outcome and drops the rail', () => {
  const session = {
    id: 's1', messages: [],
    subagents: { j1: sub({ status: 'completed', result: 'ok · 412 tests\nmore detail' }) },
  };
  const v = subagentView(session, 'j1');
  expect(v.terminal).toBe(true);
  expect(v.outcome).toBe('completed');
  expect(v.resultChip).toBe('ok · 412 tests');
  expect(v.action).toBeUndefined(); // no live now-line on terminal
});

test('a failed subagent extracts the last errored tool output', () => {
  const messages = [
    { _type: 'tool_start', tool_call_id: 't1', tool_name: 'bash', status: 'error', result: 'exit 1: boom' },
  ];
  const session = {
    id: 's1', messages: [],
    subagents: { j1: sub({ status: 'failed', messages }) },
  };
  const v = subagentView(session, 'j1');
  expect(v.outcome).toBe('failed');
  expect(v.error).toBe('exit 1: boom');
});

test('a cancelled subagent maps to the cancelled outcome', () => {
  const session = {
    id: 's1', messages: [],
    subagents: { j1: sub({ status: 'cancelled' }) },
  };
  expect(subagentView(session, 'j1').outcome).toBe('cancelled');
});

// ── live now-line ─────────────────────────────────────────────────────────
test('a running subagent surfaces its in-flight tool as the now-line action', () => {
  const messages = [
    { _type: 'tool_start', tool_call_id: 't1', tool_name: 'grep', status: 'running' },
  ];
  const session = { id: 's1', messages: [], subagents: { j1: sub({ messages }) } };
  const v = subagentView(session, 'j1');
  expect(v.action).toBe('grep');
  expect(v.elapsed).toBeUndefined(); // no startedAtMs → segment omitted
});

test('elapsed is surfaced only when startedAtMs is known, derived live from now()', () => {
  const startedAtMs = Date.now() - 72000; // 1m12s ago
  const session = {
    id: 's1', messages: [],
    subagents: { j1: sub({ startedAtMs }) },
  };
  const v = subagentView(session, 'j1');
  expect(v.elapsed).toBe('1m12s');
});

// ── canPromote ────────────────────────────────────────────────────────────
test('canPromote is true only for a running synchronous subagent', () => {
  const base = { id: 's1', messages: [] };
  expect(canPromote(subagentView({ ...base, subagents: { j1: sub({ async: false }) } }, 'j1'))).toBe(true);
  expect(canPromote(subagentView({ ...base, subagents: { j1: sub({ async: true }) } }, 'j1'))).toBe(false);
  expect(canPromote(subagentView({ ...base, subagents: { j1: sub({ status: 'completed' }) } }, 'j1'))).toBe(false);
  // Not terminal, but not promotable either — the backend only accepts the
  // promote call while status is exactly 'running'.
  expect(canPromote(subagentView({ ...base, subagents: { j1: sub({ status: 'cancelling' }) } }, 'j1'))).toBe(false);
});

// ── stable identity accent ─────────────────────────────────────────────────
test("subagent B's accent stays stable when sibling A (a lower index) finishes", () => {
  const session = {
    id: 's1', messages: [],
    subagents: {
      j1: sub({ jobId: 'j1' }),
      j2: sub({ jobId: 'j2', task: 'Write the docs' }),
    },
  };
  const before = subagentView(session, 'j2');
  expect(before.accent).toBe('teal');

  // A finishes: it's no longer live, but stays in session.subagents (map
  // insertion order is preserved), so B's accent must not shift.
  const after = {
    id: 's1', messages: [],
    subagents: {
      j1: { ...session.subagents.j1, status: 'completed' },
      j2: session.subagents.j2,
    },
  };
  const view = subagentView(after, 'j2');
  expect(view.accent).toBe('teal');
});

// ── stable identity accent across a WS reconnect ────────────────────────────
// The reconnect snapshot (GetSubagents / init) omits already-terminated
// subagents and is built from a Go map (non-deterministic iteration order), so
// deriving the accent from position-in-map is unstable across a reconnect.
// The backend now stamps each subagent with its per-session creation ordinal
// (accentIndex), transported over WS, which must survive exactly this
// scenario: sibling A (created first, accentIndex 0) has already finished and
// dropped out of the reconnect snapshot, and the surviving map is rebuilt in a
// different key order.
test("subagent B's accent survives a reconnect that drops a finished sibling and reorders the map", () => {
  const beforeReconnect = {
    id: 's1', messages: [],
    subagents: {
      j1: sub({ jobId: 'j1', accentIndex: 0 }),
      j2: sub({ jobId: 'j2', task: 'Write the docs', accentIndex: 1 }),
    },
  };
  const before = subagentView(beforeReconnect, 'j2');
  expect(before.accent).toBe('teal');

  // Reconnect: j1 already finished, so the snapshot omits it entirely, and a
  // NEW subagent j3 (created after j2, accentIndex 2) is now live too. The map
  // is rebuilt in a different order (j2 before j3) — position-based indexing
  // would give j2 index 0 ('sky'), but its stable accentIndex must still win.
  const afterReconnect = {
    id: 's1', messages: [],
    subagents: {
      j2: beforeReconnect.subagents.j2,
      j3: sub({ jobId: 'j3', task: 'Ship the release', accentIndex: 2 }),
    },
  };
  const view = subagentView(afterReconnect, 'j2');
  expect(view.accent).toBe('teal');
});

// ── session.subagents as array (minor #9) ─────────────────────────────────
test('subagentView tolerates session.subagents as an array', () => {
  const session = { id: 's1', messages: [], subagents: [sub({ jobId: 'j1' })] };
  const v = subagentView(session, 'j1');
  expect(v).not.toBeNull();
  expect(v.jobId).toBe('j1');
  expect(v.accent).toBe('sky');
});

// ── liveTrayAgents (AgentTray) ────────────────────────────────────────────
test('liveTrayAgents keeps async subagent identity accents and strips bash accents', () => {
  const session = {
    id: 's1', messages: [],
    subagents: {
      // Only ASYNC subagents reach the dock (sync ones stay inline).
      j1: sub({ jobId: 'j1', async: true }),
      b1: { jobId: 'b1', kind: 'bash', task: 'go test ./...', status: 'running', messages: [] },
    },
  };
  const chips = liveTrayAgents(session);
  const subChip = chips.find((c) => c.id === 'j1');
  const bashChip = chips.find((c) => c.id === 'b1');
  expect(subChip.kind).toBe('subagent');
  expect(subChip.accent).toBe('sky');
  expect(bashChip.kind).toBe('bash');
  expect(bashChip.name).toBe('bash');
  expect(bashChip.accent).toBeUndefined(); // no identity color for bash (INC-22)
});

test('liveTrayAgents excludes SYNC subagents (they stay inline)', () => {
  const session = {
    id: 's1', messages: [],
    subagents: { j1: sub({ jobId: 'j1', async: false }) },
  };
  expect(liveTrayAgents(session)).toHaveLength(0);
});
