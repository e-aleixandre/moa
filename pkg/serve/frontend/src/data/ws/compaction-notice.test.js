import { describe, it, expect } from 'bun:test';
import { normalizeHistory } from './history.js';
import { projectStream } from '../stream-model.js';

// The compaction notice is written for the model, but the user reads the
// transcript: it is the reason the model stops mid-task to save work. Persisted
// (pkg/bus/treesync.go no longer hides it), it must come back as the discreet
// system line, never as words attributed to the user.
describe('a persisted compaction notice', () => {
  const rows = normalizeHistory([
    {
      role: 'user',
      msg_id: 'n-1',
      custom: { source: 'compaction_notice', internal: true },
      content: [{ type: 'text', text: '<system-reminder>close to the threshold</system-reminder>' }],
    },
  ]);

  it('reloads as a system line, not as a user turn', () => {
    expect(rows.length).toBe(1);
    expect(rows[0]._type).toBe('system');
    expect(rows[0].text).toContain('Context filling up');
  });

  it('keeps the raw system-reminder text out of the transcript', () => {
    expect(rows[0].text).not.toContain('system-reminder');
  });
});

// When it happened is what lets a session reopened hours later place the
// compaction against the surrounding work. The tree entry carries epoch
// seconds; the projection must not drop them on the way to the card.
describe('the compaction marker', () => {
  it('carries its timestamp through to the rendered block', () => {
    const rows = normalizeHistory([
      {
        role: 'session_event',
        msg_id: 'c-1',
        timestamp: 1788359520,
        custom: { type: 'compaction_marker', summary: 's', tokens_before: 307000, read_files: [], modified_files: [] },
        content: [{ type: 'text', text: 'compacted' }],
      },
    ]);
    expect(rows[0].timestamp).toBe(1788359520);

    const block = projectStream({ messages: rows, subagents: {} }).find(b => b.kind === 'compaction');
    expect(block.timestamp).toBe(1788359520);
  });
});
