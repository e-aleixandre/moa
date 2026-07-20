// stream-model.test.js — run with `bun test`
//
// Exhaustive coverage of the stream projection: this is the piece of most
// judgment in phase 5, so the grouping rules (ledgers, fanout, background,
// streaming, truncation) are pinned here with plain session fixtures.
import { test, expect } from 'bun:test';
import { projectStream, liveTrayAgents } from './stream-model.js';

// ── fixture helpers ──────────────────────────────────────────────────────────
const user = (text, extra = {}) => ({ role: 'user', content: [{ type: 'text', text }], ...extra });
const assistant = (text) => ({ role: 'assistant', content: [{ type: 'text', text }] });
const tool = (id, name, args, status = 'done', result = 'ok', extra = {}) => ({
  _type: 'tool_start', tool_call_id: id, tool_name: name, args, status, result, ...extra,
});
const system = (text) => ({ _type: 'system', text });
const session = (messages, extra = {}) => ({ messages, ...extra });

// ── 1. consecutive tool calls → one ledger of N rows ─────────────────────────
test('consecutive tool calls without prose form a single ledger', () => {
  const s = session([
    tool('t1', 'read', { path: 'a.js' }),
    tool('t2', 'grep', { pattern: 'foo' }),
    tool('t3', 'bash', { command: 'ls' }),
  ]);
  const blocks = projectStream(s);
  expect(blocks).toHaveLength(1);
  expect(blocks[0].kind).toBe('document');
  expect(blocks[0].blocks).toHaveLength(1);
  const ledger = blocks[0].blocks[0];
  expect(ledger.type).toBe('ledger');
  expect(ledger.rows.map(r => r.id)).toEqual(['t1', 't2', 't3']);
  expect(ledger.rows.map(r => r.tool)).toEqual(['read', 'grep', 'bash']);
});

// ── 2. prose splits ledgers ──────────────────────────────────────────────────
test('prose between tool calls splits into two ledgers', () => {
  const s = session([
    tool('t1', 'read', { path: 'a.js' }),
    assistant('Now I will edit it.'),
    tool('t2', 'grep', { pattern: 'foo' }),
  ]);
  const blocks = projectStream(s);
  expect(blocks).toHaveLength(1);
  const doc = blocks[0];
  expect(doc.blocks.map(b => b.type)).toEqual(['ledger', 'prose', 'ledger']);
  expect(doc.blocks[0].rows).toHaveLength(1);
  expect(doc.blocks[1].text).toBe('Now I will edit it.');
  expect(doc.blocks[1].caret).toBeUndefined();
  expect(doc.blocks[2].rows).toHaveLength(1);
});

// ── 3. edit with diff → full-width sibling diff block ─────────────────────────
test('edit with a server diff emits a full-width diff sibling', () => {
  const diffResult = '@@ -10,2 +10,2 @@\n-old line\n+new line';
  const s = session([
    tool('e1', 'edit', { path: 'src/x.js' }, 'done', diffResult, { start_line: 10 }),
  ]);
  const blocks = projectStream(s);
  const doc = blocks[0];
  expect(doc.blocks.map(b => b.type)).toEqual(['ledger', 'diff']);
  const diff = doc.blocks[1];
  expect(diff.filename).toBe('src/x.js');
  expect(diff.startLine).toBe(10);
  expect(diff.diffText).toBe(diffResult);
});

test('a diff closes its ledger so later tools open a new one', () => {
  const s = session([
    tool('e1', 'edit', { path: 'x.js' }, 'done', '@@ -1,1 +1,1 @@\n-a\n+b', { start_line: 1 }),
    tool('t2', 'read', { path: 'y.js' }),
  ]);
  const doc = projectStream(s)[0];
  expect(doc.blocks.map(b => b.type)).toEqual(['ledger', 'diff', 'ledger']);
  expect(doc.blocks[0].rows[0].id).toBe('e1');
  expect(doc.blocks[2].rows[0].id).toBe('t2');
});

test('an edit WITHOUT a server unified diff emits no diff block (only its ledger row)', () => {
  // oldText/newText + start_line but no server diff: toolPreview builds a
  // formatDiff fallback that is NOT a unified diff (DiffBlock would render it
  // empty), so we must not emit a diff block — just the ledger row.
  const s = session([
    tool('e1', 'edit', { path: 'x.js', oldText: 'foo', newText: 'bar' }, 'done', '', { start_line: 5 }),
  ]);
  const doc = projectStream(s)[0];
  expect(doc.blocks.map(b => b.type)).toEqual(['ledger']);
  expect(doc.blocks[0].rows[0].id).toBe('e1');
  expect(doc.blocks.some(b => b.type === 'diff')).toBe(false);
});

test('an edit WITH a server unified diff emits a diff block', () => {
  const s = session([
    tool('e1', 'edit', { path: 'src/x.js', oldText: 'a', newText: 'b' }, 'done', '--- a/src/x.js\n+++ b/src/x.js\n@@ -1,1 +1,1 @@\n-a\n+b', { start_line: 3 }),
  ]);
  const doc = projectStream(s)[0];
  expect(doc.blocks.map(b => b.type)).toEqual(['ledger', 'diff']);
  const diff = doc.blocks[1];
  expect(diff.filename).toBe('src/x.js');
  expect(diff.startLine).toBe(3);
  expect(diff.diffText).toContain('@@');
});

// ── 4. status mapping ────────────────────────────────────────────────────────
test('tool status maps done→ok, error→err, rejected→warn', () => {
  const s = session([
    tool('t1', 'bash', { command: 'a' }, 'done', 'ok'),
    tool('t2', 'bash', { command: 'b' }, 'error', 'boom'),
    tool('t3', 'bash', { command: 'c' }, 'rejected', 'denied'),
  ]);
  const rows = projectStream(s)[0].blocks[0].rows;
  expect(rows.map(r => r.status)).toEqual(['ok', 'err', 'warn']);
});

test('bash ledger rows retain the full command while their row summary stays shortened', () => {
  const command = `printf '${'x'.repeat(110)}' && \\
  printf '\nsecond line'`;
  const s = session([tool('t1', 'bash', JSON.stringify({ command }), 'done', 'second line')]);
  const row = projectStream(s)[0].blocks[0].rows[0];
  expect(row.command).toBe(command);
  expect(row.command).toContain('\n  printf');
  expect(row.arg.text).toBe(`${command.split('\n')[0].slice(0, 100)}…`);
});

test('object-form bash args retain the full multiline command while the summary is shortened', () => {
  const command = `printf '${'x'.repeat(110)}' && \\
  printf '\nsecond line'`;
  const s = session([tool('t1', 'bash', { command }, 'done', 'second line')]);
  const row = projectStream(s)[0].blocks[0].rows[0];
  expect(row.command).toBe(command);
  expect(row.arg.text).toBe(`${command.split('\n')[0].slice(0, 100)}…`);
  expect(row.arg.text).not.toBe(command);
});

test('non-bash ledger rows do not retain a command property', () => {
  const s = session([tool('t1', 'read', { path: 'src/x.js' })]);
  const row = projectStream(s)[0].blocks[0].rows[0];
  expect(row.command).toBeUndefined();
});

// ── 5. LIVE subagents: merged into a delegation block ────────────────────────
// Only subagents still running/cancelling are read from session.subagents;
// terminated ones live in session.messages (see section 5b below). A wave of
// subagents in one turn is a single { type:'delegation' } block, not fanout.
test('two live subagents form a delegation block, one running agent row each', () => {
  const s = session([assistant('Delegating.')], {
    subagents: {
      j1: { jobId: 'j1', task: 'Analyze auth', model: 'anthropic/sonnet', status: 'running', messages: [] },
      j2: { jobId: 'j2', task: 'Analyze db', model: 'anthropic/sonnet', status: 'running', messages: [assistant('working')] },
    },
  });
  const blocks = projectStream(s);
  const doc = blocks[blocks.length - 1];
  expect(doc.kind).toBe('streaming'); // live subagents make the turn streaming
  const delegation = doc.blocks.find(b => b.type === 'delegation');
  expect(delegation).toBeTruthy();
  expect(delegation.settled).toBe(false); // still running
  expect(delegation.summary).toEqual({ total: 2, done: 0, failed: 0 });
  expect(delegation.agents).toHaveLength(2);
  const byId = Object.fromEntries(delegation.agents.map(a => [a.id, a]));
  expect(byId.j1.state).toBe('running');
  expect(byId.j1.name).toBe('Analyze auth');
  expect(byId.j1.accent).toBeTruthy();
  expect(byId.j1.action).toBeTruthy();
  expect(byId.j2.state).toBe('running');
  // accents cycle by index → the two agents differ
  expect(byId.j1.accent).not.toBe(byId.j2.accent);
  // no fanout anymore
  expect(doc.blocks.some(b => b.type === 'fanout')).toBe(false);
});

test('a single live subagent is a delegation block with one agent, no header caseness', () => {
  const s = session([assistant('Delegating.')], {
    subagents: {
      j1: { jobId: 'j1', task: 'Do the thing', model: 'anthropic/sonnet', status: 'running', messages: [] },
    },
  });
  const blocks = projectStream(s);
  const doc = blocks[blocks.length - 1];
  expect(doc.blocks.some(b => b.type === 'fanout')).toBe(false);
  const delegation = doc.blocks.find(b => b.type === 'delegation');
  expect(delegation).toBeTruthy();
  expect(delegation.agents).toHaveLength(1);
  expect(delegation.agents[0].state).toBe('running');
  expect(delegation.agents[0].name).toBe('Do the thing');
});

test('an originating tool call is suppressed in favor of its live synchronous subagent', () => {
  const s = session([
    tool('toolu_1', 'subagent', { task: 'Analyze auth' }, 'running'),
  ], {
    subagents: {
      j1: { jobId: 'j1', originToolCallId: 'toolu_1', task: 'Analyze auth', status: 'running', async: false, messages: [] },
    },
  });
  const agents = projectStream(s).flatMap(block => (block.blocks || []).flatMap(inner => inner.agents || []));
  expect(agents).toEqual([expect.objectContaining({ id: 'j1', state: 'running' })]);
});

test('a synchronous originating tool call becomes one done row after its subagent ends', () => {
  const s = session([
    tool('toolu_1', 'subagent', { task: 'Analyze auth' }, 'done', 'Found three issues'),
  ], {
    subagents: {
      j1: { jobId: 'j1', originToolCallId: 'toolu_1', task: 'Analyze auth', status: 'completed', async: false, messages: [] },
    },
  });
  const agents = projectStream(s).flatMap(block => (block.blocks || []).flatMap(inner => inner.agents || []));
  expect(agents).toEqual([expect.objectContaining({ id: 'j1', state: 'done', chip: 'Found three issues' })]);
});

test('an async originating tool call appears once in the dock and its canonical completion appears once inline', () => {
  const running = session([
    tool('call_1', 'subagent', { task: 'Review ws.go' }, 'done'),
  ], {
    subagents: {
      j1: { jobId: 'j1', originToolCallId: 'call_1', task: 'Review ws.go', status: 'running', async: true, messages: [] },
    },
  });
  const runningAgents = projectStream(running).flatMap(block => (block.blocks || []).flatMap(inner => inner.agents || []));
  expect(runningAgents).toHaveLength(0);
  expect(liveTrayAgents(running)).toEqual([expect.objectContaining({ id: 'j1', kind: 'subagent' })]);

  const completed = session([
    tool('call_1', 'subagent', { task: 'Review ws.go' }, 'done'),
    tool('subagent-j1', 'subagent', { task: 'Review ws.go' }, 'done', 'Looks good'),
  ], {
    subagents: {
      j1: { jobId: 'j1', originToolCallId: 'call_1', task: 'Review ws.go', status: 'completed', async: true, messages: [] },
    },
  });
  const completedAgents = projectStream(completed).flatMap(block => (block.blocks || []).flatMap(inner => inner.agents || []));
  expect(completedAgents).toEqual([expect.objectContaining({ id: 'j1', state: 'done', chip: 'Looks good' })]);
  expect(liveTrayAgents(completed)).toHaveLength(0);
});

test('parallel subagents with identical tasks remain distinct by origin tool call ID', () => {
  const s = session([
    tool('toolu_1', 'subagent', { task: 'Review the same file' }, 'running'),
    tool('toolu_2', 'subagent', { task: 'Review the same file' }, 'running'),
  ], {
    subagents: {
      j1: { jobId: 'j1', originToolCallId: 'toolu_1', task: 'Review the same file', status: 'running', async: false, messages: [] },
      j2: { jobId: 'j2', originToolCallId: 'toolu_2', task: 'Review the same file', status: 'running', async: false, messages: [] },
    },
  });
  const agents = projectStream(s).flatMap(block => (block.blocks || []).flatMap(inner => inner.agents || []));
  expect(agents.map(agent => agent.id).sort()).toEqual(['j1', 'j2']);
});

test('an empty subagent origin ID does not suppress an unrelated empty-ID tool card', () => {
  const s = session([
    tool('', 'subagent', { task: 'Legacy delegation' }, 'done', 'finished'),
    tool('', 'read', { path: 'still-visible.txt' }, 'done', 'contents'),
  ], {
    subagents: {
      legacy: { jobId: 'legacy', originToolCallId: '', task: 'Legacy delegation', status: 'completed', async: false, messages: [] },
    },
  });
  const blocks = projectStream(s);
  const ledgers = blocks.flatMap(block => (block.blocks || []).filter(inner => inner.type === 'ledger'));

  expect(ledgers).toHaveLength(1);
  expect(ledgers[0].rows).toEqual([expect.objectContaining({ id: '', tool: 'read' })]);
});

test('a terminal subagent origin card remains once when its live entry is gone', () => {
  const s = session([
    tool('toolu_sync1', 'subagent', { task: 'Review auth' }, 'done', 'Found an issue'),
  ]);
  const agents = projectStream(s)
    .flatMap(block => (block.blocks || []).flatMap(inner => inner.agents || []));

  expect(agents).toHaveLength(1);
  expect(agents[0]).toEqual(expect.objectContaining({ id: 'toolu_sync1', state: 'done', chip: 'Found an issue' }));
});

// ── 5b. TERMINATED subagents come from messages, not session.subagents ───────
test('a terminated subagent in messages folds into a delegation block, not a ledger', () => {
  const s = session([
    assistant('Delegating.'),
    tool('subagent-j1', 'subagent', { task: 'Analyze auth' }, 'done', 'Found 3 issues in auth'),
    assistant('The subagent found issues.'),
  ]);
  const doc = projectStream(s)[0];
  // prose, delegation (the subagent card), prose — in chronological order
  expect(doc.blocks.map(b => b.type)).toEqual(['prose', 'delegation', 'prose']);
  const delegation = doc.blocks[1];
  expect(delegation.settled).toBe(true); // all terminated → auto-collapses
  expect(delegation.summary).toEqual({ total: 1, done: 1, failed: 0 });
  expect(delegation.agents[0].id).toBe('j1');
  expect(delegation.agents[0].state).toBe('done');
  expect(delegation.agents[0].name).toBe('Analyze auth');
  expect(delegation.agents[0].chip).toBe('Found 3 issues in auth');
  expect(doc.blocks.some(b => b.type === 'fanout')).toBe(false);
});

test('a completed subagent lingering in session.subagents is not duplicated', () => {
  // It is already in messages as a subagent card; the map still holds it as
  // completed → must appear exactly once (from messages), never re-emitted.
  const s = session([
    assistant('Delegating.'),
    tool('subagent-j1', 'subagent', { task: 'Analyze auth' }, 'done', 'done text'),
  ], {
    subagents: {
      j1: { jobId: 'j1', task: 'Analyze auth', status: 'completed', messages: [] },
    },
  });
  const blocks = projectStream(s);
  // exactly one delegation agent references j1, and there is no fanout/background
  const agents = blocks.flatMap(b => (b.blocks || []).flatMap(x => x.agents || []));
  expect(agents.filter(a => a.id === 'j1')).toHaveLength(1);
  expect(blocks.some(b => (b.blocks || []).some(x => x.type === 'fanout' || x.type === 'background'))).toBe(false);
});

test('a still-running map entry already carded in messages is deduped by job id', () => {
  // Race: the completion card reached messages (tool_call_id `subagent-j1`)
  // but the map entry still reads `running`. The status filter alone would
  // NOT drop it — only seenJobIds does. So this pins the seenJobIds guard.
  const s = session([
    assistant('Delegating.'),
    tool('subagent-j1', 'subagent', { task: 'Analyze auth' }, 'done', 'done text'),
  ], {
    subagents: {
      j1: { jobId: 'j1', task: 'Analyze auth', status: 'running', messages: [] },
    },
  });
  const blocks = projectStream(s);
  const agents = blocks.flatMap(b => (b.blocks || []).flatMap(x => x.agents || []));
  expect(agents.filter(a => a.id === 'j1')).toHaveLength(1);
  expect(blocks.some(b => (b.blocks || []).some(x => x.type === 'fanout' || x.type === 'background'))).toBe(false);
});

test('a still-running bash map entry already carded in messages is deduped by job id', () => {
  // handleWsBashComplete uses the `bash-complete-<id>` prefix in messages.
  const s = session([
    assistant('Building.'),
    tool('bash-complete-b1', 'bash', { command: 'go build ./...' }, 'done', 'ok'),
  ], {
    subagents: {
      b1: { jobId: 'b1', kind: 'bash', task: 'go build ./...', status: 'running', messages: [] },
    },
  });
  const blocks = projectStream(s);
  const rows = blocks.flatMap(b => (b.blocks || []).flatMap(x => x.rows || []));
  expect(rows.filter(r => r.id === 'bash-complete-b1')).toHaveLength(1);
  expect(blocks.some(b => (b.blocks || []).some(x => x.type === 'background'))).toBe(false);
});

test('two sequential terminated subagents form one delegation block, two agents', () => {
  const s = session([
    assistant('Delegating two tasks.'),
    tool('subagent-j1', 'subagent', { task: 'Analyze auth' }, 'done', 'found issues'),
    tool('subagent-j2', 'subagent', { task: 'Analyze db' }, 'done', 'looks fine'),
  ]);
  const doc = projectStream(s)[0];
  expect(doc.blocks.map(b => b.type)).toEqual(['prose', 'delegation']);
  const delegation = doc.blocks[1];
  expect(delegation.agents.map(a => a.id)).toEqual(['j1', 'j2']);
  expect(delegation.summary).toEqual({ total: 2, done: 2, failed: 0 });
  expect(delegation.settled).toBe(true);
  expect(doc.blocks.some(b => b.type === 'fanout')).toBe(false);
});

// ── 6. LIVE async bash job → dock (liveTrayAgents), NOT an inline block ──────
// Async bash used to render an inline `background` block; that block is retired
// ("async in the dock, sync inline"). A live bash now only surfaces through
// liveTrayAgents() for the LiveDock, and never appears in the stream projection.
test('a live async bash job goes to the dock, not an inline background block', () => {
  const s = session([assistant('Running build.')], {
    subagents: {
      b1: {
        jobId: 'b1', kind: 'bash', task: 'go build ./...', status: 'running',
        messages: [tool('b1', 'bash', { command: 'go build ./...' }, 'running', null, { streamingResult: 'line1\nline2\ncompiling…' })],
      },
    },
  });
  const blocks = projectStream(s);
  const doc = blocks[blocks.length - 1];
  // No inline background block anymore.
  expect(doc.blocks.some(b => b.type === 'background')).toBe(false);
  // It surfaces in the dock instead.
  const tray = liveTrayAgents(s);
  expect(tray).toHaveLength(1);
  expect(tray[0]).toMatchObject({ id: 'b1', kind: 'bash', name: 'bash' });
});

test('a live async subagent goes to the dock; a sync one stays inline', () => {
  const s = session([assistant('x')], {
    subagents: {
      b1: { jobId: 'b1', kind: 'bash', task: 'sleep 1', status: 'running', messages: [] },
      async1: { jobId: 'async1', task: 'analyze', status: 'running', async: true, messages: [] },
      sync1: { jobId: 'sync1', task: 'review', status: 'running', async: false, messages: [] },
    },
  });
  const blocks = projectStream(s);
  const doc = blocks[blocks.length - 1];
  // No fanout, no background — async work is dock-only now.
  expect(doc.blocks.some(b => b.type === 'fanout')).toBe(false);
  expect(doc.blocks.some(b => b.type === 'background')).toBe(false);
  // Only the SYNC subagent appears inline in the delegation block.
  const delegation = doc.blocks.find(b => b.type === 'delegation');
  expect(delegation.agents).toHaveLength(1);
  expect(delegation.agents[0].id).toBe('sync1');
  // The async subagent + the bash go to the dock.
  const tray = liveTrayAgents(s);
  expect(tray.map(t => t.id).sort()).toEqual(['async1', 'b1']);
});

// A COMPLETED async subagent must remain reachable: while running it's in the
// dock, but once it ends its card (in messages) folds INLINE into the delegation
// block like any other terminated subagent — otherwise its result would vanish
// (the dock only holds RUNNING work). Regression for the async-history drop.
test('a completed async subagent folds inline (not dropped)', () => {
  const s = session(
    [
      assistant('Delegated the review async.'),
      // terminated async subagent card, as pushed by handleWsSubagentEnd
      tool('subagent-async1', 'subagent', { task: 'review ws.go' }, 'done', 'looks good'),
    ],
    {
      // its entry lingers in the map, still flagged async, now completed
      subagents: { async1: { jobId: 'async1', task: 'review ws.go', status: 'completed', async: true, messages: [] } },
    },
  );
  const doc = projectStream(s)[0];
  const delegation = doc.blocks.find(b => b.type === 'delegation');
  expect(delegation).toBeTruthy();
  expect(delegation.agents.map(a => a.id)).toContain('async1');
  // and it's NOT still shown as live in the dock
  expect(liveTrayAgents(s)).toHaveLength(0);
});

// ── 7. live turn → streaming, not document ───────────────────────────────────
test('streamingText plus a running tool yields a streaming block', () => {
  const s = session([
    assistant('Let me check.'),
    tool('t1', 'bash', { command: 'ls' }, 'running', null),
  ], { streamingText: 'Still working on it' });
  const blocks = projectStream(s);
  const last = blocks[blocks.length - 1];
  expect(last.kind).toBe('streaming');
  // the streamed prose is appended to the live document
  const liveProse = last.blocks.find(b => b.type === 'prose' && b.text === 'Still working on it');
  expect(liveProse).toBeTruthy();
  expect(liveProse.caret).toBe(true);
  // streamingText means the model is writing → textLive true (the doc caret blinks)
  expect(last.textLive).toBe(true);
});

test('a running tool with no streaming text is not textLive (no doc caret)', () => {
  const s = session([
    assistant('Let me check.'),
    tool('t1', 'bash', { command: 'go test ./...' }, 'running', null, { startedAt: 1 }),
  ]);
  const blocks = projectStream(s);
  const last = blocks[blocks.length - 1];
  expect(last.kind).toBe('streaming'); // still a live turn (the tool runs)
  expect(last.textLive).toBe(false); // but the model isn't writing → no doc caret
});

// ── 7b. trailing running/generating tool → its ledger row is marked `live` ───
// The tool currently in flight is the LAST row of the LAST ledger, carrying
// `live:true` and its `startedAt` timestamp so the ActivityLedger card renders
// it as the tinted running row instead of a normal terminated row.
test('a trailing running tool_start is marked live on its ledger row', () => {
  const s = session([
    tool('t1', 'read', { path: 'a.js' }, 'done', '10 lines'),
    tool('t2', 'bash', { command: 'go test ./...' }, 'running', null, { startedAt: 12345 }),
  ]);
  const blocks = projectStream(s);
  const doc = blocks[blocks.length - 1];
  const ledger = doc.blocks.find(b => b.type === 'ledger');
  expect(ledger.rows).toHaveLength(2);
  expect(ledger.rows[0].live).toBeUndefined();
  expect(ledger.rows[1].live).toBe(true);
  expect(ledger.rows[1].startedAt).toBe(12345);
});

test('a live tool row carries its streamingResult as liveTail (last 3 lines)', () => {
  const s = session([
    tool('t1', 'bash', { command: 'go test ./...' }, 'running', null, {
      startedAt: 1, streamingResult: 'l1\nl2\nl3\nl4\nl5',
    }),
  ]);
  const blocks = projectStream(s);
  const doc = blocks[blocks.length - 1];
  const ledger = doc.blocks.find(b => b.type === 'ledger');
  expect(ledger.rows[0].live).toBe(true);
  expect(ledger.rows[0].liveTail).toBe('l3\nl4\nl5'); // last 3 lines only
});

test('a live tool row with no streamingResult has no liveTail', () => {
  const s = session([
    tool('t1', 'read', { path: 'a.js' }, 'running', null, { startedAt: 1 }),
  ]);
  const blocks = projectStream(s);
  const doc = blocks[blocks.length - 1];
  const ledger = doc.blocks.find(b => b.type === 'ledger');
  expect(ledger.rows[0].live).toBe(true);
  expect(ledger.rows[0].liveTail).toBeUndefined();
});

test('a generating tool_start (args still streaming) is also marked live', () => {
  const s = session([
    tool('t1', 'edit', {}, 'generating', null, { startedAt: 999 }),
  ]);
  const blocks = projectStream(s);
  const doc = blocks[blocks.length - 1];
  const ledger = doc.blocks.find(b => b.type === 'ledger');
  expect(ledger.rows[0].live).toBe(true);
  expect(ledger.rows[0].startedAt).toBe(999);
});

test('a terminated tool_start is never marked live', () => {
  const s = session([tool('t1', 'bash', { command: 'ls' }, 'done', 'ok')]);
  const blocks = projectStream(s);
  const doc = blocks[0];
  const ledger = doc.blocks.find(b => b.type === 'ledger');
  expect(ledger.rows[0].live).toBeUndefined();
});

test('a finished turn is a document, never streaming', () => {
  const s = session([assistant('All done.'), tool('t1', 'bash', { command: 'ls' }, 'done', 'ok')]);
  const blocks = projectStream(s);
  expect(blocks.every(b => b.kind !== 'streaming')).toBe(true);
  expect(blocks[0].kind).toBe('document');
});

// ── 8. thinking text → live turn, but NOT painted ────────────────────────────
// Thinking is deliberately not projected to a rendered block (it streams then
// vanishes when the turn ends, which read as a block appearing and disappearing
// unrecoverably). The turn is still "streaming" (live caret / auto-scroll key
// off session.thinkingText), we just don't draw a thinking block.
test('thinkingText keeps the turn live but produces no thinking block', () => {
  const s = session([assistant('hmm')], { thinkingText: 'considering options' });
  const blocks = projectStream(s);
  const last = blocks[blocks.length - 1];
  expect(last.kind).toBe('streaming');
  expect(last.textLive).toBe(true);
  expect(last.blocks.find(b => b.type === 'thinking')).toBeUndefined();
});

test('a thinking-only running turn has no inline prose caret', () => {
  const s = session([assistant('hmm')], {
    state: 'running',
    thinkingText: 'considering options',
  });
  const blocks = projectStream(s);
  const doc = blocks[blocks.length - 1];
  expect(doc.kind).toBe('streaming');
  expect(doc.blocks.some(b => b.type === 'prose' && b.caret === true)).toBe(false);
});

// ── 9. history truncation notice ─────────────────────────────────────────────
test('historyTruncated emits a leading system block', () => {
  const s = session([user('hi')], { historyTruncated: true });
  const blocks = projectStream(s);
  expect(blocks[0].kind).toBe('system');
  expect(blocks[0].text).toBe('Older messages…');
});

test('more than 200 messages also triggers the truncation notice', () => {
  const many = [];
  for (let i = 0; i < 201; i++) many.push(assistant('x'));
  const blocks = projectStream(session(many));
  expect(blocks[0].kind).toBe('system');
  expect(blocks[0].text).toBe('Older messages…');
});

// ── 10. multi-turn separation ────────────────────────────────────────────────
test('a steered user message carries a steer flag on its waypoint', () => {
  const s = session([
    user('do the thing'),
    assistant('working on it'),
    user('actually, focus on tests', { _steer_id: 'st-1' }),
    assistant('ok, tests it is'),
  ]);
  const blocks = projectStream(s);
  const wps = blocks.filter((b) => b.kind === 'waypoint');
  expect(wps[0].steer).toBeUndefined(); // a normal turn opener
  expect(wps[1].steer).toBe(true); // the injected steer
});

test('two user turns produce two waypoints and two separate documents', () => {
  const s = session([
    user('first question'),
    assistant('first answer'),
    tool('t1', 'read', { path: 'a.js' }),
    assistant('more'),
    user('second question'),
    assistant('second answer'),
  ]);
  const blocks = projectStream(s);
  const kinds = blocks.map(b => b.kind);
  expect(kinds).toEqual(['waypoint', 'document', 'waypoint', 'document']);
  expect(blocks[0].text).toBe('first question');
  expect(blocks[2].text).toBe('second question');
  // first document has prose, ledger, prose in order
  expect(blocks[1].blocks.map(b => b.type)).toEqual(['prose', 'ledger', 'prose']);
  expect(blocks[3].blocks.map(b => b.type)).toEqual(['prose']);
});

test('a system line closes the current document', () => {
  const s = session([
    assistant('before'),
    system('✂ Context compacted'),
    assistant('after'),
  ]);
  const blocks = projectStream(s);
  expect(blocks.map(b => b.kind)).toEqual(['document', 'system', 'document']);
  expect(blocks[1].text).toBe('✂ Context compacted');
});

// ── 11. robustness ───────────────────────────────────────────────────────────
test('null session returns an empty array', () => {
  expect(projectStream(null)).toEqual([]);
});

test('empty session returns an empty array', () => {
  expect(projectStream(session([]))).toEqual([]);
  expect(projectStream({})).toEqual([]);
});

test('user message with an image attachment does not break and is preserved', () => {
  const msg = {
    role: 'user',
    content: [
      { type: 'text', text: 'look at this' },
      { type: 'image', source: { data: 'xxx' } },
    ],
  };
  const blocks = projectStream(session([msg]));
  expect(blocks).toHaveLength(1);
  expect(blocks[0].kind).toBe('waypoint');
  expect(blocks[0].text).toBe('look at this');
  expect(blocks[0].attachments).toHaveLength(1);
  expect(blocks[0].attachments[0].type).toBe('image');
});

test('user message time is carried through when present', () => {
  const blocks = projectStream(session([user('hi', { ts: 1234 })]));
  expect(blocks[0].time).toBe(1234);
});

test('output blocks are plain serializable objects (no functions)', () => {
  const s = session([user('q'), assistant('a'), tool('t1', 'read', { path: 'a.js' })]);
  const blocks = projectStream(s);
  // round-trips through JSON without loss → confirms pure/serializable
  expect(JSON.parse(JSON.stringify(blocks))).toEqual(blocks);
});

test('a tool with undefined args and result does not break', () => {
  const s = session([{ _type: 'tool_start', tool_call_id: 't1', tool_name: 'read' }]);
  const blocks = projectStream(s);
  const row = blocks[0].blocks[0].rows[0];
  expect(row.id).toBe('t1');
  expect(row.tool).toBe('read');
  expect(row.arg.text).toBe('');
  expect(row.out).toBe('');
});

test('a live subagent without messages does not break', () => {
  const s = session([assistant('x')], {
    subagents: { j1: { jobId: 'j1', task: 'do', status: 'running' } },
  });
  const doc = projectStream(s)[projectStream(s).length - 1];
  const delegation = doc.blocks.find(b => b.type === 'delegation');
  expect(delegation).toBeTruthy();
  expect(delegation.agents[0].name).toBe('do');
  expect(delegation.agents[0].state).toBe('running');
});

test('a failed subagent is summarised as failed with an error chip', () => {
  const s = session([
    assistant('Delegating.'),
    tool('subagent-j1', 'subagent', { task: 'Break things' }, 'error', 'panic: nil map'),
  ]);
  const doc = projectStream(s)[0];
  const delegation = doc.blocks.find(b => b.type === 'delegation');
  expect(delegation.agents[0].state).toBe('failed');
  expect(delegation.agents[0].chip).toBe('panic: nil map');
  expect(delegation.summary).toEqual({ total: 1, done: 0, failed: 1 });
  expect(delegation.settled).toBe(true);
});

test('a live subagent joins the same turn\'s already-terminated agents in one block', () => {
  // One subagent already finished (card in messages) while another is still
  // running (map) in the SAME turn → one delegation block, two agents, unsettled.
  const s = session([
    assistant('Delegating two.'),
    tool('subagent-j1', 'subagent', { task: 'first' }, 'done', 'ok'),
  ], {
    subagents: {
      j2: { jobId: 'j2', task: 'second', status: 'running', messages: [] },
    },
  });
  const doc = projectStream(s)[projectStream(s).length - 1];
  const delegation = doc.blocks.find(b => b.type === 'delegation');
  expect(delegation.agents.map(a => a.id)).toEqual(['j1', 'j2']);
  expect(delegation.agents[0].state).toBe('done');
  expect(delegation.agents[1].state).toBe('running');
  expect(delegation.settled).toBe(false);
  expect(delegation.summary).toEqual({ total: 2, done: 1, failed: 0 });
});

test('a cancelled subagent keeps a distinct cancelled state', () => {
  const s = session([
    assistant('Delegating.'),
    tool('subagent-j1', 'subagent', { task: 'Long job' }, 'cancelled', 'stopped by user'),
  ]);
  const doc = projectStream(s)[0];
  const delegation = doc.blocks.find(b => b.type === 'delegation');
  expect(delegation.agents[0].state).toBe('cancelled');
  // cancelled counts with failed for the header summary
  expect(delegation.summary).toEqual({ total: 1, done: 0, failed: 1 });
  expect(delegation.settled).toBe(true);
});

test('subagent_wait is dropped entirely — never a ledger row', () => {
  const s = session([
    assistant('Waiting on the subagent.'),
    tool('wait-1', 'subagent_wait', { job_id: 'j1' }, 'done', 'joined'),
    tool('subagent-j1', 'subagent', { task: 'work' }, 'done', 'ok'),
  ]);
  const doc = projectStream(s)[0];
  // No ledger holding a subagent_wait row; only the delegation block.
  const ledgers = doc.blocks.filter(b => b.type === 'ledger');
  expect(ledgers).toHaveLength(0);
  const delegation = doc.blocks.find(b => b.type === 'delegation');
  expect(delegation.agents.map(a => a.id)).toEqual(['j1']);
});

test('a terminated card carries its saved accentIndex over a jobId-hash fallback', () => {
  const s = session([
    assistant('Delegating.'),
    tool('subagent-j1', 'subagent', { task: 'work' }, 'done', 'ok', { accentIndex: 3 }),
  ]);
  const doc = projectStream(s)[0];
  const delegation = doc.blocks.find(b => b.type === 'delegation');
  // FANOUT_ACCENTS[3] === 'peach'
  expect(delegation.agents[0].accent).toBe('peach');
});

test('a terminated card with no accent source is stable (not always slot 0)', () => {
  const mk = (jobId) => {
    const s = session([
      assistant('Delegating.'),
      tool(`subagent-${jobId}`, 'subagent', { task: 'work' }, 'done', 'ok'),
    ]);
    const doc = projectStream(s)[0];
    return doc.blocks.find(b => b.type === 'delegation').agents[0].accent;
  };
  // Deterministic per jobId, and the same job twice yields the same color.
  expect(mk('alpha')).toBe(mk('alpha'));
});

test('projectStream does not mutate the input session', () => {
  const s = session([assistant('x')], {
    subagents: { j1: { jobId: 'j1', task: 'do', status: 'running', messages: [] } },
  });
  const snapshot = JSON.parse(JSON.stringify(s));
  projectStream(s);
  expect(JSON.parse(JSON.stringify(s))).toEqual(snapshot);
});

// ── 12. stable block ids (Terra 5C: keys must survive re-projection) ─────────
test('block ids are stable across re-projection and unique', () => {
  const s = session([
    user('do it', { msg_id: 'u1' }),
    assistant('working'),
    tool('t1', 'read', { path: 'a.js' }),
    assistant('done'),
  ]);
  const a = projectStream(s);
  const b = projectStream(s); // same input, projected again
  const idsA = a.map(x => x.id);
  const idsB = b.map(x => x.id);
  expect(idsA).toEqual(idsB); // top-level ids identical run to run
  expect(new Set(idsA).size).toBe(idsA.length); // and unique
  // sub-block ids inside the document are also present and stable
  const docA = a.find(x => x.kind === 'document');
  const docB = b.find(x => x.kind === 'document');
  expect(docA.blocks.map(x => x.id)).toEqual(docB.blocks.map(x => x.id));
  expect(docA.blocks.every(x => x.id != null)).toBe(true);
});

test('growing the conversation keeps earlier block ids unchanged', () => {
  const base = [
    user('first', { msg_id: 'u1' }),
    assistant('reply one'),
  ];
  const before = projectStream(session([...base]));
  const after = projectStream(session([...base, user('second', { msg_id: 'u2' }), assistant('reply two')]));
  // the first waypoint + first document keep the same ids after new turns arrive
  expect(after[0].id).toBe(before[0].id);
  expect(after[1].id).toBe(before[1].id);
});
